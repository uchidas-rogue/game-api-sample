package doccheck

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// document は1つの設計文書から読み取った、検査に必要な情報。
type document struct {
	// rel はリポジトリルートからの相対パス。
	rel string
	// tables は `#` 列を持つ表（= 仕様表とみなす表）。
	tables []specTable
	// diagrams はファイル内の mermaid フローチャート。
	diagrams []diagram
	// lines はファイルの全行（1 始まりで参照するため先頭にダミーを1つ入れる）。
	lines []string
}

// headerHeight は markdown の表がヘッダに使う行数（ヘッダ行 + 区切り行）。
const headerHeight = 2

// specTable は仕様表1つぶん。
type specTable struct {
	// line は表のヘッダ行の行番号。
	line int
	// header はヘッダ行のセル（前後の空白を除去したもの）。
	header []string
	// pathCol は「図のパス」列のインデックス。無ければ -1。
	pathCol int
	// numbered は `#` 列を持つか。持つ表だけがアンカーとケース突合の対象になる。
	numbered bool
	rows     []specRow
	anchor   anchor
}

// specRow は仕様表の1行。
type specRow struct {
	line  int
	num   int
	cells []string
	// path は「図のパス」列の内容。列が無ければ空。
	path string
	// unimplemented は行に `未対応` を含むか（テスト不在を許す / README §7）。
	unimplemented bool
}

// anchorKind はアンカーの種類。
type anchorKind int

const (
	// anchorNone はアンカーが見つからないことを表す。
	anchorNone anchorKind = iota
	// anchorMarkers は `// #N` マーカーとの突合を行う宣言。
	anchorMarkers
	// anchorFuncs は最終列のテスト関数名の実在だけを見る宣言。
	anchorFuncs
	// anchorSkip は理由付きの対象外宣言。
	anchorSkip
	// anchorInvalid は書式が壊れた宣言。
	anchorInvalid
)

// anchor は表とテストコードの対応づけ宣言。
type anchor struct {
	kind  anchorKind
	line  int
	file  string
	funcs []string
	// msg は anchorInvalid のときの理由。
	msg string
}

// describeTarget は失敗メッセージ用に参照先を短く表す。
func (a anchor) describeTarget() string {
	return fmt.Sprintf("%s の %s", a.file, strings.Join(a.funcs, " + "))
}

// parseDoc は markdown を読み、仕様表・アンカー・フローチャートを取り出す。
//
// markdown の完全なパースはしない。本リポジトリの文書は「行頭が `|` の表」
// 「``` で囲む mermaid ブロック」しか使っていないため、行単位で足りる。
func parseDoc(path, rel string) (*document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s の読み取りに失敗しました: %w", rel, err)
	}

	doc := &document{rel: rel, lines: append([]string{""}, strings.Split(string(raw), "\n")...)}

	inFence := false
	fenceLang := ""
	fenceStart := 0
	var fenceBody []string

	for n := 1; n < len(doc.lines); n++ {
		line := doc.lines[n]
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if inFence {
				if fenceLang == "mermaid" {
					doc.diagrams = append(doc.diagrams, parseDiagram(fenceStart, fenceBody))
				}
				inFence = false
				fenceBody = nil
				continue
			}
			inFence = true
			fenceLang = strings.TrimSpace(strings.TrimLeft(trimmed, "`~"))
			fenceStart = n
			continue
		}
		if inFence {
			fenceBody = append(fenceBody, line)
			continue
		}

		if doc.isTableHeader(n) {
			table, next := doc.parseTable(n)
			doc.tables = append(doc.tables, table)
			n = next
		}
	}

	return doc, nil
}

// isTableHeader は n 行目が表のヘッダ行（次の行が区切り行）かを判定する。
func (d *document) isTableHeader(n int) bool {
	if !strings.HasPrefix(strings.TrimSpace(d.lines[n]), "|") {
		return false
	}
	if n+1 >= len(d.lines) {
		return false
	}
	next := strings.TrimSpace(d.lines[n+1])
	if !strings.HasPrefix(next, "|") {
		return false
	}
	for _, cell := range splitRow(next) {
		if cell == "" || strings.Trim(cell, "-:") != "" {
			return false
		}
	}
	return true
}

// parseTable はヘッダ行 headerLine から表を読み、最後に読んだ行番号を返す。
//
// `#` 列を持たない表も読む。ケースの突合対象にはならないが、「図のパス」列を持つなら
// 終端ノードの網羅判定には効くため（実例: outbox-worker.md の
// 「手続きが異なるため別テスト関数に切り出しているもの」の表）。
func (d *document) parseTable(headerLine int) (specTable, int) {
	table := specTable{
		line:    headerLine,
		header:  splitRow(strings.TrimSpace(d.lines[headerLine])),
		pathCol: -1,
	}
	for i, cell := range table.header {
		if cell == "図のパス" {
			table.pathCol = i
		}
	}
	table.numbered = len(table.header) > 0 && table.header[0] == "#"
	if table.numbered {
		table.anchor = d.findAnchor(headerLine)
	}

	// ヘッダ行と区切り行（| --- | --- |）の次から本文が始まる。
	n := headerLine + headerHeight
	for ; n < len(d.lines); n++ {
		trimmed := strings.TrimSpace(d.lines[n])
		if !strings.HasPrefix(trimmed, "|") {
			break
		}
		cells := splitRow(trimmed)
		row := specRow{
			line:          n,
			cells:         cells,
			unimplemented: strings.Contains(trimmed, "未対応"),
		}
		if table.numbered {
			num, err := strconv.Atoi(cells[0])
			if err != nil {
				// 番号でない行は仕様表の行ではない（表の終わり）。
				break
			}
			row.num = num
		}
		if table.pathCol >= 0 && table.pathCol < len(cells) {
			row.path = cells[table.pathCol]
		}
		table.rows = append(table.rows, row)
	}
	return table, n - 1
}

// findAnchor はヘッダ行の直前にあるアンカー宣言を探す。
//
// 表の直前は「空行」か「HTML コメント」しか許さない。離れた位置のコメントを
// 拾うと、別の表のアンカーを流用しているのか書き忘れなのかが区別できなくなる。
func (d *document) findAnchor(headerLine int) anchor {
	for n := headerLine - 1; n >= 1; n-- {
		trimmed := strings.TrimSpace(d.lines[n])
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "<!--") {
			return anchor{kind: anchorNone, line: headerLine}
		}
		if !strings.Contains(trimmed, "testcases") {
			// ssot-assert 等の別のディレクティブ。読み飛ばして更に上を見る。
			continue
		}
		return parseAnchor(trimmed, n)
	}
	return anchor{kind: anchorNone, line: headerLine}
}

// parseAnchor は1行の HTML コメントをアンカーとして解釈する。
func parseAnchor(line string, lineNo int) anchor {
	body := line
	body = strings.TrimPrefix(body, "<!--")
	body = strings.TrimSuffix(body, "-->")
	body = strings.TrimSpace(body)

	directive, rest, found := strings.Cut(body, ":")
	if !found {
		return anchor{kind: anchorInvalid, line: lineNo, msg: "アンカーの書式が壊れている（`<!-- testcases: <ファイル>#<関数名> -->`）"}
	}
	rest = strings.TrimSpace(rest)

	switch strings.TrimSpace(directive) {
	case "testcases":
		file, funcs, ok := strings.Cut(rest, "#")
		if !ok || strings.TrimSpace(file) == "" || strings.TrimSpace(funcs) == "" {
			return anchor{kind: anchorInvalid, line: lineNo,
				msg: "testcases アンカーは `<ファイル>#<関数名>` の形で書く（複数の関数は + で連ねる）"}
		}
		var names []string
		for _, name := range strings.Split(funcs, "+") {
			name = strings.TrimSpace(name)
			if name != "" {
				names = append(names, name)
			}
		}
		return anchor{kind: anchorMarkers, line: lineNo, file: strings.TrimSpace(file), funcs: names}
	case "testcases-funcs":
		if rest == "" {
			return anchor{kind: anchorInvalid, line: lineNo, msg: "testcases-funcs アンカーはテストファイルのパスを取る"}
		}
		return anchor{kind: anchorFuncs, line: lineNo, file: rest}
	case "testcases-skip":
		reason := strings.TrimSpace(rest)
		if !strings.HasPrefix(reason, "'") || !strings.HasSuffix(reason, "'") || len(reason) < 3 {
			return anchor{kind: anchorInvalid, line: lineNo,
				msg: "testcases-skip は理由を必須にしている（`<!-- testcases-skip: '<理由>' -->`）。" +
					"理由の無い除外は、書き忘れと区別できない"}
		}
		return anchor{kind: anchorSkip, line: lineNo}
	default:
		return anchor{kind: anchorInvalid, line: lineNo,
			msg: fmt.Sprintf("未知のアンカー `%s`。testcases / testcases-funcs / testcases-skip のいずれかを使う",
				strings.TrimSpace(directive))}
	}
}

// splitRow は表の1行をセルへ分割する。前後の `|` は落とし、各セルの空白を除去する。
func splitRow(trimmed string) []string {
	body := strings.TrimSuffix(strings.TrimPrefix(trimmed, "|"), "|")
	parts := strings.Split(body, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}
