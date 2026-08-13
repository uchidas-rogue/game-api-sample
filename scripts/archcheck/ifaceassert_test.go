package archcheck_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uchidas-rogue/game-api-sample/scripts/archcheck"
)

// TestCheckIfaceAssert_Repository はリポジトリ全体が AGENTS.md §2 の配置規約を
// 満たしていることを検証する。これが本パッケージの存在理由そのもので、
// 違反が1件でもあれば CI が落ちる。
func TestCheckIfaceAssert_Repository(t *testing.T) {
	t.Parallel()

	violations, err := archcheck.CheckIfaceAssert(repoRoot(t))
	require.NoError(t, err)

	assert.Empty(t, render(violations),
		"var _ Iface = (*Type)(nil) は実装型の定義直前に置くこと（AGENTS.md §2）")
}

// TestCheckIfaceAssert_Fixtures は検査ロジック自体を testdata のフィクスチャで検証する。
// testdata 配下はビルド対象外なので、意図的な違反を含むファイルを置ける。
//
// 各ケースは「検出された行」だけでなく「メッセージに何が出るか」も見る。
// 違反を知らせる文面が本検査の成果物であり、直し方が読み取れなければ意味がないため。
func TestCheckIfaceAssert_Fixtures(t *testing.T) {
	t.Parallel()

	violations, err := archcheck.CheckIfaceAssert("testdata")
	require.NoError(t, err)

	got := make(map[string][]archcheck.Violation, len(violations))
	for _, v := range violations {
		got[v.File] = append(got[v.File], v)
	}

	tests := []struct {
		name string
		file string
		// wantLines は検出されるべき違反の行番号（出現順）。
		wantLines []int
		// wantMsgParts は各違反のメッセージに含まれるべき文字列（wantLines と同じ順）。
		wantMsgParts []string
	}{
		{
			name: "(*T)(nil) の直後に type T があれば違反なし",
			file: "ok_pointer.go",
		},
		{
			name: "値レシーバのみの T{} 形式も許容する",
			file: "ok_value.go",
		},
		{
			name: "&T{} 形式も型名が一意なので許容する",
			file: "ok_ampersand.go",
		},
		{
			name: "var と型のあいだの doc コメントは隣接を壊さない",
			file: "ok_doc.go",
		},
		{
			name: "他パッケージの型は対象外（internal/di 相当）",
			file: "ok_external.go",
		},
		{
			name: "他パッケージの型は複合リテラル形式でも対象外",
			file: "ok_external_lit.go",
		},
		{
			name:         "var と型のあいだに別の宣言が挟まると違反",
			file:         "ng_separated.go",
			wantLines:    []int{6},
			wantMsgParts: []string{"直後にあるのは func helperD"},
		},
		{
			name:         "var の後に宣言が無ければ違反",
			file:         "ng_missing.go",
			wantLines:    []int{7},
			wantMsgParts: []string{"宣言が無い（ファイル末尾）"},
		},
		{
			name:         "var ブロック内で直前になれなかったエントリは違反",
			file:         "ng_block_local.go",
			wantLines:    []int{11},
			wantMsgParts: []string{"直後にあるのは type implF"},
		},
		{
			name:         "直後が別の var 宣言でも違反",
			file:         "ng_next_var.go",
			wantLines:    []int{6},
			wantMsgParts: []string{"直後にあるのは var 宣言"},
		},
		{
			name:         "右辺が変数参照なら実装型を特定できず違反",
			file:         "ng_unknown.go",
			wantLines:    []int{9},
			wantMsgParts: []string{"右辺から実装型を特定できない"},
		},
		{
			name:      "(*T)(nil) / T{} / &T{} 以外の書き方はすべて違反",
			file:      "ng_forms.go",
			wantLines: []int{9, 11, 13, 15, 17},
			wantMsgParts: []string{
				"右辺から実装型を特定できない",
				"右辺から実装型を特定できない",
				"右辺から実装型を特定できない",
				"右辺から実装型を特定できない",
				"右辺から実装型を特定できない",
			},
		},
		{
			name: "生成マーカー付きのファイルは対象外",
			file: "skip_generated.go",
		},
	}

	// 表に無いファイルで違反が出ていないことを先に確認する。
	// 各ケースは自分のファイルしか見ないため、これが無いと想定外の検出を見逃す。
	known := make(map[string]bool, len(tests))
	for _, tt := range tests {
		known[tt.file] = true
	}
	for file := range got {
		assert.Truef(t, known[file], "テスト表に無いフィクスチャ %s で違反が出ている", file)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			found := got[tt.file]
			var lines []int
			for _, v := range found {
				lines = append(lines, v.Line)
			}
			require.Equal(t, tt.wantLines, lines,
				"%s で検出された違反の行が期待と異なる: %v", tt.file, render(found))

			for i, part := range tt.wantMsgParts {
				assert.Containsf(t, found[i].String(), part,
					"%s の %d 件目のメッセージに %q が含まれていない", tt.file, i+1, part)
				assert.Containsf(t, found[i].String(), "AGENTS.md §2",
					"%s の %d 件目のメッセージに規約の出典が含まれていない", tt.file, i+1)
			}
		})
	}
}

// TestCheckIfaceAssert_ParseError はパースできないファイルを握り潰さないことを検証する。
// 黙って 0 件を返すと「検査が通った」と区別できず、検出力が静かに失われるため。
func TestCheckIfaceAssert_ParseError(t *testing.T) {
	t.Parallel()

	violations, err := archcheck.CheckIfaceAssert(filepath.Join("testdata", "_broken"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken.go")
	assert.Nil(t, violations)
}

// repoRoot は go.mod を見つけるまで親をたどってリポジトリルートを返す。
// テストの作業ディレクトリはパッケージディレクトリなので相対パスでも届くが、
// パッケージを移動しても壊れないよう go.mod を基準にする。
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
func render(violations []archcheck.Violation) []string {
	out := make([]string, 0, len(violations))
	for _, v := range violations {
		out = append(out, v.String())
	}
	return out
}
