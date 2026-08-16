package doccheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uchidas-rogue/game-api-sample/scripts/doccheck"
)

// TestCheckSpecTables_Repository はリポジトリ全体で、テスト仕様表とテストコードが
// 1 対 1 に保たれていることを検証する。これが本パッケージの存在理由そのもので、
// 違反が1件でもあれば CI が落ちる。
func TestCheckSpecTables_Repository(t *testing.T) {
	t.Parallel()

	violations, err := doccheck.CheckSpecTables(repoRoot(t))
	require.NoError(t, err)

	assert.Empty(t, render(violations),
		"テスト仕様表とテストコードは 1 対 1 に保つこと（docs/testing/README.md §6）")
}

// TestCheckSpecTables_OK は、通してよい書き方を落とさないことを検証する。
//
// 検出力の裏返しとして必要な検査。誤検出があると「規約どおりに書いたのに落ちる」状態になり、
// 検査ごと無効化される方向へ圧力がかかる（determ. §2）。
func TestCheckSpecTables_OK(t *testing.T) {
	t.Parallel()

	violations, err := doccheck.CheckSpecTables(filepath.Join("testdata", "ok"))
	require.NoError(t, err)

	assert.Empty(t, render(violations))
}

// TestCheckSpecTables_Detects は、表とテストが食い違う各パターンを検出することを
// フィクスチャで検証する。ここが薄いと「常に空を返す検査」を入れてしまう。
//
// 各ケースは検出された件数だけでなくメッセージの中身も見る。違反の文面が本検査の
// 成果物であり、どちらをどう直すのかが読み取れなければ意味がないため。
func TestCheckSpecTables_Detects(t *testing.T) {
	t.Parallel()

	violations, err := doccheck.CheckSpecTables(filepath.Join("testdata", "ng"))
	require.NoError(t, err)

	tests := []struct {
		name string
		// wantFile / wantMsgParts に**すべて**該当する違反が1件以上あることを求める。
		wantFile     string
		wantMsgParts []string
	}{
		{
			name:         "表の行を足してテストを足し忘れると落ちる",
			wantFile:     "docs/testing/a_row_count.md",
			wantMsgParts: []string{"表の行数（3）", "マーカー数（2）"},
		},
		{
			name:         "ケースの並びが表と入れ替わると落ちる",
			wantFile:     "internal/sample/sample_test.go",
			wantMsgParts: []string{"1 番目のケースのマーカーが `// #2`", "b_order.md"},
		},
		{
			name:         "マーカーのパスが表と食い違うと落ちる",
			wantFile:     "internal/sample/sample_test.go",
			wantMsgParts: []string{"図のパスが不一致", "`A→B→C→Z`", "`A→B→E1`"},
		},
		{
			name:         "図に無い辺を通るパスは落ちる",
			wantFile:     "docs/testing/d_edge.md",
			wantMsgParts: []string{"`A→Z` が図に無い"},
		},
		{
			name:         "終端ノードを通るケースが無いと落ちる",
			wantFile:     "docs/testing/e_terminal.md",
			wantMsgParts: []string{"終端ノード `Z`", "未対応"},
		},
		{
			name:         "アンカーが実在しない関数を指すと落ちる",
			wantFile:     "docs/testing/f_anchor.md",
			wantMsgParts: []string{"TestMissingFunc", "無い"},
		},
		{
			name:         "アンカーが無い表は落ちる",
			wantFile:     "docs/testing/f_anchor.md",
			wantMsgParts: []string{"仕様表にアンカーが無い"},
		},
		{
			name:         "アンカーの書式が壊れていると落ちる",
			wantFile:     "docs/testing/f_anchor.md",
			wantMsgParts: []string{"`<ファイル>#<関数名>` の形"},
		},
		{
			name:         "理由の無い skip は落ちる",
			wantFile:     "docs/testing/f_anchor.md",
			wantMsgParts: []string{"testcases-skip は理由を必須"},
		},
		{
			name:         "関数名列に実在しない関数を書くと落ちる",
			wantFile:     "docs/testing/f_anchor.md",
			wantMsgParts: []string{"テスト関数 `TestNotExist`"},
		},
		{
			name:         "関数名列に実在しないサブテスト名を書くと落ちる",
			wantFile:     "docs/testing/f_anchor.md",
			wantMsgParts: []string{"サブテスト名 `無いサブテスト`"},
		},
		{
			name:         "関数名列が空だと落ちる",
			wantFile:     "docs/testing/f_anchor.md",
			wantMsgParts: []string{"最終列にテスト関数名が無い"},
		},
		{
			name:         "どの表からも参照されないマーカーは落ちる",
			wantFile:     "internal/sample/sample_test.go",
			wantMsgParts: []string{"TestOrphan", "どの仕様表からも参照されていない"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.True(t, matches(violations, tt.wantFile, tt.wantMsgParts),
				"検出されるべき違反が無い。実際の違反:\n%s", strings.Join(render(violations), "\n"))
		})
	}
}

// TestCheckSpecTables_ParseError はパースできないテストファイルを握り潰さないことを検証する。
// 黙って 0 件を返すと「検査が通った」と区別できず、検出力が静かに失われるため。
func TestCheckSpecTables_ParseError(t *testing.T) {
	t.Parallel()

	violations, err := doccheck.CheckSpecTables(filepath.Join("testdata", "_broken"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken_test.go")
	assert.Nil(t, violations)
}

// matches は指定ファイルの違反に、部分文字列をすべて含むものがあるかを判定する。
func matches(violations []doccheck.Violation, file string, parts []string) bool {
	for _, v := range violations {
		if v.File != file {
			continue
		}
		found := true
		for _, part := range parts {
			if !strings.Contains(v.Msg, part) {
				found = false
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}

// repoRoot は go.mod を見つけるまで親をたどってリポジトリルートを返す。
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := filepath.Abs(".")
	require.NoError(t, err)

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod が見つからずリポジトリルートを特定できません")
		dir = parent
	}
}

// render は失敗時に読める形へ違反を畳む。
func render(violations []doccheck.Violation) []string {
	out := make([]string, 0, len(violations))
	for _, v := range violations {
		out = append(out, v.String())
	}
	return out
}
