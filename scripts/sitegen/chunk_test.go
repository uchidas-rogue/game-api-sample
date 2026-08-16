package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChunkDocument は markdown の分割規則を検証する。
//
// 索引はサイトのチャットが唯一の根拠にするデータなので、「何を落として何を残すか」が
// 変わると回答の質が直接変わる。規則そのものをここで固定する。
func TestChunkDocument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// source は入力の markdown。
		source string
		// wantIDs は生成されるチャンクの ID（出現順）。
		wantIDs []string
		// wantContains は各チャンクの本文に含まれるべき文字列（wantIDs と同じ順・空文字は検査しない）。
		wantContains []string
		// wantAbsent はどのチャンクにも現れてはいけない文字列。
		wantAbsent []string
	}{
		{
			name:         "見出しごとに1チャンクへ分かれ、パンくずが積み上がる",
			source:       "# 概要\n本文A\n\n## 詳細\n本文B\n",
			wantIDs:      []string{"doc.md#概要", "doc.md#詳細"},
			wantContains: []string{"本文A", "本文B"},
		},
		{
			name:         "mermaid ブロックは落とす",
			source:       "# 図\n説明\n\n```mermaid\nflowchart TD\n    A --> B\n```\n\n続き\n",
			wantIDs:      []string{"doc.md#図"},
			wantContains: []string{"続き"},
			wantAbsent:   []string{"flowchart", "A --> B"},
		},
		{
			// フェンス記号まで検証するのは、中身だけを見ていた頃に「バッククォートを
			// 全部落とす」実装が素通りし、```bash が裸の `bash` 行に化けていたため。
			name:         "mermaid 以外のコードフェンスは記号ごと残す",
			source:       "# 実行\n```bash\nmake test\n```\n",
			wantIDs:      []string{"doc.md#実行"},
			wantContains: []string{"```bash\nmake test\n```"},
		},
		{
			name:         "コードフェンスの中身は装飾もコメントも落とさない（記法の説明でありうるため）",
			source:       "# 記法\n```md\n表の直前に <!-- testcases: x_test.go#TestX --> を置く\n```\n",
			wantIDs:      []string{"doc.md#記法"},
			wantContains: []string{"<!-- testcases: x_test.go#TestX -->"},
		},
		{
			name:         "コードフェンス内の # は見出しにしない",
			source:       "# 手順\n```sh\n# コメント行\nmake run\n```\n",
			wantIDs:      []string{"doc.md#手順"},
			wantContains: []string{"# コメント行"},
		},
		{
			name:         "HTML コメントは落とす",
			source:       "# 規約\n<!-- ssot-assert: absent-grep 'x' internal -->\n本文\n",
			wantIDs:      []string{"doc.md#規約"},
			wantContains: []string{"本文"},
			wantAbsent:   []string{"ssot-assert"},
		},
		{
			// 行頭一致だけを見ていた頃、この形が索引に残っていた（AGENTS.md §3 の実例）。
			name:         "文中の HTML コメントは落とし、前後の本文は残す",
			source:       "# 規約\n- 表の直前に <!-- testcases: x --> を置く\n",
			wantIDs:      []string{"doc.md#規約"},
			wantContains: []string{"- 表の直前に を置く"},
			wantAbsent:   []string{"testcases"},
		},
		{
			// docs/testing/outbox-worker.md の実例。引用記号の後ろにあると行頭一致で拾えない。
			name:         "引用記号の後ろの HTML コメントは行ごと落とす",
			source:       "# 規約\n> <!-- ssot-assert: absent-grep 'x' internal -->\n本文\n",
			wantIDs:      []string{"doc.md#規約"},
			wantContains: []string{"本文"},
			wantAbsent:   []string{"ssot-assert", ">"},
		},
		{
			name:         "リンクは表示文字列だけ残す",
			source:       "# 参照\n詳細は [AGENTS.md](../AGENTS.md) を読む\n",
			wantIDs:      []string{"doc.md#参照"},
			wantContains: []string{"詳細は AGENTS.md を読む"},
			wantAbsent:   []string{"../AGENTS.md"},
		},
		{
			name:         "同名の見出しには連番を振る",
			source:       "# テスト\nA\n\n# テスト\nB\n",
			wantIDs:      []string{"doc.md#テスト", "doc.md#テスト-1"},
			wantContains: []string{"A", "B"},
		},
		{
			// 出現回数をそのまま連番にすると 2つ目と3つ目がどちらも `テスト-1` になる。
			// ID が衝突すると Worker が別チャンクの本文を根拠として解決してしまう。
			name:         "連番付きの見出しが別にあっても ID は衝突しない",
			source:       "# テスト\nA\n\n# テスト\nB\n\n# テスト-1\nC\n",
			wantIDs:      []string{"doc.md#テスト", "doc.md#テスト-1", "doc.md#テスト-1-1"},
			wantContains: []string{"A", "B", "C"},
		},
		{
			name:    "本文の無い見出しはチャンクにしない",
			source:  "# 目次\n\n## 中身\n本文\n",
			wantIDs: []string{"doc.md#中身"},
		},
		{
			name:         "見出しの記号はアンカーから落とす（GitHub のリンクと合わせる）",
			source:       "# 【運用ルール】AWS 環境が無い間は止める（理由）\n本文\n",
			wantIDs:      []string{"doc.md#運用ルールaws-環境が無い間は止める理由"},
			wantContains: []string{"本文"},
		},
		{
			// 見出しより前の本文にはアンカーを作れない。作ると doc.md#docmd のような
			// 存在しないアンカーへのリンクになるので、空アンカー（ファイル先頭）にする。
			name:    "見出しが1つも無ければファイル名のチャンクになり、アンカーは空",
			source:  "前書きだけの文書\n",
			wantIDs: []string{"doc.md#"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := chunkDocument("doc.md", tt.source)

			ids := make([]string, 0, len(got))
			for _, c := range got {
				ids = append(ids, c.ID)
			}
			require.Equal(t, tt.wantIDs, ids)

			for i, want := range tt.wantContains {
				if want == "" {
					continue
				}
				assert.Contains(t, got[i].Text, want)
			}
			for _, absent := range tt.wantAbsent {
				for _, c := range got {
					assert.NotContains(t, c.Text, absent)
				}
			}
		})
	}
}

// TestChunkDocument_パンくずは上位見出しを連ねる は、出典表示に使う Trail が
// 見出しの深さに応じて積み上がり、同じ深さの見出しで置き換わることを検証する。
func TestChunkDocument_パンくずは上位見出しを連ねる(t *testing.T) {
	t.Parallel()

	got := chunkDocument("docs/testing/README.md", "# 運用ルール\nA\n\n## 3. 表の形\nB\n\n## 4. カバレッジ\nC\n")

	require.Len(t, got, 3)
	assert.Equal(t, "docs/testing/README.md > 運用ルール", got[0].Trail)
	assert.Equal(t, "docs/testing/README.md > 運用ルール > 3. 表の形", got[1].Trail)
	assert.Equal(t, "docs/testing/README.md > 運用ルール > 4. カバレッジ", got[2].Trail,
		"同じ深さの見出しは前の見出しを置き換える")
}

// TestChunkDocument_長い節は段落境界で分割する は、上限を超える節が分割され、
// 出典（File / Anchor）は共有したまま ID だけが分かれることを検証する。
//
// 分割しないと1チャンクだけで入力トークンを食い尽くし、上位6件を渡す設計が崩れる。
func TestChunkDocument_長い節は段落境界で分割する(t *testing.T) {
	t.Parallel()

	para := strings.Repeat("あ", 400)
	source := "# 長い節\n" + strings.Join([]string{para, para, para, para}, "\n\n") + "\n"

	got := chunkDocument("doc.md", source)

	require.Greater(t, len(got), 1, "上限を超えたら分割されること")
	assert.Equal(t, "doc.md#長い節", got[0].ID)
	assert.Equal(t, "doc.md#長い節@2", got[1].ID)
	for _, c := range got {
		assert.Equal(t, "長い節", c.Anchor, "分割しても出典リンクは同じ見出しを指す")
		assert.LessOrEqual(t, utf8.RuneCountInString(c.Text), maxChunkChars,
			"上限はバイト数ではなく rune 数で効くこと")
	}
}

// TestChunkDocument_改行1つ区切りの長い節も分割する は、空行の無い箇条書きが
// 上限を超えたときに行境界で分割されることを検証する。
//
// 段落（空行）だけを分割単位にしていた頃、この索引の対象文書はほぼ改行1つ区切りの
// 箇条書きなので1節がまるごと1段落になり、上限が事実上まったく効いていなかった。
func TestChunkDocument_改行1つ区切りの長い節も分割する(t *testing.T) {
	t.Parallel()

	line := "- " + strings.Repeat("あ", 300)
	source := "# 箇条書き\n" + strings.Repeat(line+"\n", 6)

	got := chunkDocument("doc.md", source)

	require.Greater(t, len(got), 1, "空行が無くても行境界で分割されること")
	for _, c := range got {
		assert.LessOrEqual(t, utf8.RuneCountInString(c.Text), maxChunkChars)
	}
}

// TestChunkDocument_1行で上限を超える場合は分割しない は、行より細かく切らないことを検証する。
// 文の途中で切ると抜粋として読めなくなるため、これが唯一の上限超えになる。
func TestChunkDocument_1行で上限を超える場合は分割しない(t *testing.T) {
	t.Parallel()

	got := chunkDocument("doc.md", "# 長い1行\n"+strings.Repeat("あ", maxChunkChars+100)+"\n")

	require.Len(t, got, 1)
	assert.Equal(t, maxChunkChars+100, utf8.RuneCountInString(got[0].Text))
}
