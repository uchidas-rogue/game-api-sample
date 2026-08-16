package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRun_出力の形 は索引 JSON のキーと構造を固定する。
//
// web/app.js と web/worker/src/index.ts はこのキー名で索引を読む。索引は生成物なので、
// キーを変えても make site/check（差分検知）では気づけず、サイトが黙って壊れる。
// 「生成物の形はテストで固定する」ことでその穴を塞ぐ。
func TestRun_出力の形(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "README.md", "# 概要\nサンプル\n")
	writeFixture(t, root, "AGENTS.md", "# 規約\n本文\n")
	writeFixture(t, root, "CLAUDE.md", "# Claude\n本文\n")
	writeFixture(t, root, "ROADMAP.md", "# 計画\n本文\n")
	writeFixture(t, root, "terraform/ARCHITECTURE.md", "# 構成\n本文\n")
	writeFixture(t, root, "loadtest/README.md", "# 負荷試験\n本文\n")
	writeFixture(t, root, "docs/testing/README.md", "# テスト\n本文\n")

	require.NoError(t, run(root, outputPath))

	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(outputPath)))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, repoSlug, got["repo"], "出典リンクの組み立てに使うキー")
	assert.Equal(t, defaultBranch, got["branch"])

	// 上限値はブラウザとプロキシが索引から読む。キーが変わると両方が既定値も持たないまま
	// undefined を掴むので、形をここで固定する（値そのものは本ファイルの const が正本）。
	gotLimits, ok := got["limits"].(map[string]any)
	require.True(t, ok, "limits はオブジェクトであること")
	assert.EqualValues(t, topK, gotLimits["topK"], "web/app.js と worker が読むキー")
	assert.EqualValues(t, maxQuestionChars, gotLimits["maxQuestionChars"])

	chunks, ok := got["chunks"].([]any)
	require.True(t, ok, "chunks は配列であること")
	require.NotEmpty(t, chunks)

	first, ok := chunks[0].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"id", "file", "anchor", "title", "trail", "text"} {
		assert.Contains(t, first, key, "web/app.js と worker が読むキー")
	}
}

// TestRun_取り込み対象の欠落は失敗させる は、生成が黙って部分的な索引を作らないことを検証する。
// 欠けたまま生成されると、サイトはその文書について「見当たりません」と答え続ける。
func TestRun_取り込み対象の欠落は失敗させる(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "README.md", "# 概要\n本文\n")

	err := run(root, outputPath)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "AGENTS.md")
}

// writeFixture は root 配下に相対パスでファイルを作る。
func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
