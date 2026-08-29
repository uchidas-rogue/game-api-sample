package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// chunk は索引の1単位。markdown の見出し1つぶんの本文を持つ。
//
// json タグは web/app.js と web/worker/src/index.ts が読む。変更したら両方を直すこと
// （索引は生成物なので、フィールド名の変更は make site/check では検知できない）。
type chunk struct {
	// ID は索引内で一意。クライアントはこの ID だけをプロキシへ送る。
	ID string `json:"id"`
	// File はリポジトリルートからの相対パス。
	File string `json:"file"`
	// Anchor は GitHub の見出しリンク（`#` の後ろ）。見出しより前の本文では空になる。
	Anchor string `json:"anchor"`
	// Title は見出しそのもの。
	Title string `json:"title"`
	// Trail は上位見出しを ` > ` で連ねたパンくず。回答の出典表示に使う。
	Trail string `json:"trail"`
	// Text は見出し直下の本文。
	Text string `json:"text"`

	// preamble は「見出しより前の本文」であることを示す。JSON には出さない。
	// 対応する見出しが無いのでアンカーを作れず、作るとリンク先が存在しない
	// アンカー（AGENTS.md#agentsmd 等）になるため、空アンカーで区別する。
	preamble bool
}

const (
	// maxChunkChars は1チャンクの本文の上限（rune 数）。超えたら段落・行境界で分割する。
	//
	// 上位6チャンクをモデルへ渡す前提の上限。日本語ではおおむね 1 rune ≒ 1 トークンなので、
	// 最悪ケースの入力は 6 × 1200 ≒ 7k トークン。コストの内訳は web/worker/README.md を参照。
	//
	// バイト数ではなく rune 数で数える。len() で数えていた頃は日本語の文書で実効 400 文字に
	// なっており、定数名・コメントの「文字」と実装が食い違っていた。
	maxChunkChars = 1200
	// maxHeadingLevel は見出しとして扱う `#` の最大数。
	maxHeadingLevel = 6
	// asciiMax は ASCII の最大コードポイント。これを超える文字（日本語など）は原則そのまま残す。
	asciiMax = 0x7F
	// cjkPunctFirst / cjkPunctLast は 、。「」【】〜 などの CJK 記号の範囲。
	cjkPunctFirst = 0x3000
	cjkPunctLast  = 0x303F
	// katakanaMiddleDot は ・（中黒）。カタカナの範囲にあるが記号なので落とす。
	katakanaMiddleDot = 0x30FB
	// fullwidthFirst / fullwidthLast は （）：／ などの全角記号・全角英数の範囲。
	fullwidthFirst = 0xFF01
	fullwidthLast  = 0xFF60
)

// headingPattern は markdown の見出し行。
var headingPattern = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// fencePattern はコードフェンスの開始・終了行（言語指定を含む）。
var fencePattern = regexp.MustCompile("^\\s*(```|~~~)\\s*([A-Za-z0-9_-]*)\\s*$")

// linkPattern は markdown のリンク。表示文字列だけを残す
// （URL は出典リンクが別に持つため、本文には要らない）。
var linkPattern = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// blankRunPattern は3行以上続く空行。1行へ畳む。
var blankRunPattern = regexp.MustCompile(`\n{3,}`)

// commentPattern は1行で閉じている HTML コメント。
//
// 複数行にまたがるコメントは扱わない。現在の取り込み対象に存在しないため
// （`-->` の出現はすべて mermaid の矢印で、mermaid ブロックごと落としている）。
// 将来現れた場合は開始行だけが落ちて残りが本文に混ざる、という検出漏れになる。
var commentPattern = regexp.MustCompile(`<!--.*?-->`)

// spaceRunPattern は行の途中に連なる空白。行頭の字下げは箇条書きの階層を表すので対象にしない。
var spaceRunPattern = regexp.MustCompile(`(\S) {2,}`)

// chunkDocument は markdown を見出し単位のチャンクへ分割する。
//
// 【落とすもの】
//   - mermaid ブロック（図はテキストにすると意味を成さず、トークンだけ食う）
//   - HTML コメント（ssot-assert / testcases アンカーは読み手向けの情報ではない）
//   - リンクの URL 部分（表示文字列だけ残す）
//
// 【落とさないもの】
//   - mermaid 以外のコードフェンス（コマンド例や Go の断片は回答の根拠になる）。
//     フェンス記号・言語指定も含めて逐語で残す
//   - 表（そのままの行で残す。表形式の情報は本リポジトリの文書の主要な形）
//   - コードフェンスの中身（装飾もコメントも落とさない。中身は「記法の説明」でありうるため、
//     scripts/docs-ssot-check.sh の検査3a がフェンス内の ssot-assert を評価対象外にするのと揃える）
//
// 行単位の変換をこのループの中で済ませるのは、フェンスの内外を知っているのがここだけだから。
// 後段（normalizeBody）で装飾を落としていた頃は、フェンス記号のバッククォートまで消えて
// ```bash が裸の `bash` という行に化けていた。
func chunkDocument(rel, source string) []chunk {
	var (
		chunks  []chunk
		trail   [maxHeadingLevel + 1]string
		current = chunk{File: rel, Title: rel, Trail: rel, preamble: true}
		body    []string
		inFence bool
		inSkip  bool
	)

	flush := func() {
		text := normalizeBody(body)
		body = nil
		if text == "" {
			return
		}
		current.Text = text
		chunks = append(chunks, current)
	}

	for _, line := range strings.Split(source, "\n") {
		if match := fencePattern.FindStringSubmatch(line); match != nil {
			if inFence {
				if !inSkip {
					body = append(body, line)
				}
				inFence, inSkip = false, false
				continue
			}
			inFence = true
			inSkip = match[2] == "mermaid"
			if !inSkip {
				body = append(body, line)
			}
			continue
		}
		if inFence {
			if !inSkip {
				body = append(body, line)
			}
			continue
		}

		match := headingPattern.FindStringSubmatch(line)
		if match == nil {
			if cleaned, keep := cleanLine(line); keep {
				body = append(body, cleaned)
			}
			continue
		}

		flush()
		level := len(match[1])
		title := stripInline(match[2])
		trail[level] = title
		for deeper := level + 1; deeper <= maxHeadingLevel; deeper++ {
			trail[deeper] = ""
		}
		current = chunk{File: rel, Title: title, Trail: buildTrail(rel, trail)}
	}
	flush()

	return splitLong(assignIDs(rel, chunks))
}

// cleanLine はフェンス外の1行から装飾と HTML コメントを落とす。
// 落とした結果、引用記号や空白しか残らない行は捨てる（keep = false）。
//
// 「行頭が <!-- なら捨てる」だけだった頃は、文中や引用記号の後ろに置かれたコメント
// （`表の直前の <!-- testcases: … --> を置く`、`> <!-- ssot-assert: … -->`）が
// 本文に残り、読み手向けでないアンカーがモデルへの抜粋に混ざっていた。
func cleanLine(line string) (string, bool) {
	if strings.Contains(line, "<!--") {
		line = commentPattern.ReplaceAllString(line, "")
		// 引用記号（>）だけが残るのはコメント専用行だったということ。
		// 閉じていないコメント（複数行コメントの開始行）もここで捨てる。
		if strings.Trim(line, "> \t") == "" || strings.Contains(line, "<!--") {
			return "", false
		}
		// コメントを抜いた跡に空白が二重に残るので詰める（字下げは保つため行頭は触らない）。
		line = strings.TrimRight(line, " \t")
		line = spaceRunPattern.ReplaceAllString(line, "$1 ")
	}
	return stripInline(line), true
}

// buildTrail はファイル名と上位見出しからパンくずを組み立てる。
func buildTrail(rel string, trail [maxHeadingLevel + 1]string) string {
	parts := []string{rel}
	for _, title := range trail {
		if title != "" {
			parts = append(parts, title)
		}
	}
	return strings.Join(parts, " > ")
}

// assignIDs は見出しからアンカーを作り、ファイル内で一意な ID を振る。
// 同名の見出しには GitHub と同じく連番を足す。
//
// 連番は「未使用になるまで」進める。単純に出現回数を足すだけだと
// 見出しが `X`, `X`, `X-1` の順に並んだときに 2 つ目と 3 つ目がどちらも `X-1` になり、
// ID が衝突する。衝突すると Worker が別チャンクの本文を根拠として解決してしまう。
func assignIDs(rel string, chunks []chunk) []chunk {
	used := map[string]bool{}
	for i := range chunks {
		base := slugify(chunks[i].Title)
		if chunks[i].preamble {
			base = ""
		}
		anchor := base
		for n := 1; used[anchor]; n++ {
			anchor = fmt.Sprintf("%s-%d", base, n)
		}
		used[anchor] = true
		chunks[i].Anchor = anchor
		chunks[i].ID = rel + "#" + anchor
	}
	return chunks
}

// splitLong は maxChunkChars を超えるチャンクを段落境界で分割する。
// 分割後のチャンクは同じアンカー（= 同じ出典リンク）を持ち、ID だけが `@2` 以降で区別される。
func splitLong(chunks []chunk) []chunk {
	var out []chunk
	for _, c := range chunks {
		parts := splitBlocks(c.Text)
		if len(parts) == 1 {
			out = append(out, c)
			continue
		}
		for i, part := range parts {
			piece := c
			piece.Text = part
			if i > 0 {
				piece.ID = fmt.Sprintf("%s@%d", c.ID, i+1)
			}
			out = append(out, piece)
		}
	}
	return out
}

// splitBlocks は本文を maxChunkChars（rune 数）以下の塊へ畳む。
//
// まず段落（空行区切り）を単位に束ね、それでも上限を超える段落は行境界でさらに分割する。
// 行までしか下げないのは、文の途中で切ると抜粋として読めなくなるため。
// 1行だけで上限を超える場合はその行を単独の塊にする（これが唯一の上限超え）。
//
// 段落だけを単位にしていた頃は、この索引の対象文書がほぼ改行1つ区切りの箇条書きなので
// 1節がまるごと1段落として扱われ、上限が事実上まったく効いていなかった
// （最大 3,130 文字 / 上位6件の合計 12,247 文字）。
func splitBlocks(text string) []string {
	if utf8.RuneCountInString(text) <= maxChunkChars {
		return []string{text}
	}

	var (
		parts   []string
		current strings.Builder
		length  int
	)
	// add は unit を現在の塊へ足す。入らなければ塊を確定してから足す。
	// sep は直前の unit との区切り（段落境界なら空行、行境界なら改行）。
	add := func(unit, sep string) {
		size := utf8.RuneCountInString(unit)
		if length > 0 && length+size > maxChunkChars {
			parts = append(parts, current.String())
			current.Reset()
			length = 0
		}
		if length > 0 {
			current.WriteString(sep)
			length += utf8.RuneCountInString(sep)
		}
		current.WriteString(unit)
		length += size
	}

	for _, para := range strings.Split(text, "\n\n") {
		if utf8.RuneCountInString(para) <= maxChunkChars {
			add(para, "\n\n")
			continue
		}
		for i, line := range strings.Split(para, "\n") {
			sep := "\n"
			if i == 0 {
				sep = "\n\n"
			}
			add(line, sep)
		}
	}
	if length > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// normalizeBody は積んだ行を1つの本文へまとめる。
// 行単位の変換は chunkDocument が済ませてあるので、ここは連結と空行の整理だけを行う。
func normalizeBody(lines []string) string {
	text := strings.Join(lines, "\n")
	text = blankRunPattern.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// stripInline はインラインの装飾を落とす。強調とバッククォートは読みやすさに影響しないので消す。
func stripInline(text string) string {
	text = linkPattern.ReplaceAllString(text, "$1")
	return strings.NewReplacer("**", "", "`", "").Replace(text)
}

// slugify は見出しから GitHub 互換のアンカーを作る。
// 日本語はそのまま残す（GitHub も残したうえで URL エンコードする）。
func slugify(title string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r == ' ':
			out.WriteRune('-')
		case r == '-' || r == '_':
			out.WriteRune(r)
		case isSlugRune(r):
			out.WriteRune(r)
		}
	}
	return strings.Trim(out.String(), "-")
}

// isSlugRune はアンカーに残す文字かを判定する。
//
// GitHub は見出しから記号を落として文字だけを残す。日本語の記号（`（）`『【】`、。・` や
// 全角英数）を残すと、生成したリンクが実際の見出しへ飛ばなくなるため同じ規則で落とす。
func isSlugRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	case r <= asciiMax: // 上で拾わなかった ASCII 記号は落とす
		return false
	case r >= cjkPunctFirst && r <= cjkPunctLast:
		return false
	case r == katakanaMiddleDot:
		return false
	case r >= fullwidthFirst && r <= fullwidthLast:
		return false
	default:
		return true
	}
}
