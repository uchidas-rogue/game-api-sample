// Command sitegen は GitHub Pages 用サイトの知識源を、リポジトリの文書から生成する。
//
// 出力は web/data/index.json（見出し単位のチャンク配列）。サイトのチャットは、この索引を
// ブラウザ側で検索して上位チャンクの **ID だけ** をプロキシへ送り、プロキシが同じ索引から
// 本文を解決してモデルへ渡す。索引を正本（AGENTS.md / docs/** など）から生成しているので、
// 文書を直せばサイトも追随し、サイト専用の写しを二重管理せずに済む。
//
// 【なぜ手書きの JSON にしないか】
// このリポジトリが一貫して避けてきた失敗形（同じ情報を2箇所に書いて片方だけ古くなる）を、
// サイトで作り直さないため。再生成漏れは make site/check が差分で検知する。
//
// 【決定論】
// 出力にタイムスタンプや実行環境の値を含めない。入力が同じなら出力も同じになるので、
// 「再生成して差分が出たら失敗」という検査が成立する。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// outputPath は索引の出力先（リポジトリルートからの相対パス）。
	outputPath = "web/data/index.json"
	// docsDir は再帰的に取り込む文書ディレクトリ。
	docsDir = "docs"
	// repoSlug は出典リンクの組み立てに使う GitHub のリポジトリ。
	// terraform/environments/dev/variables.tf の github_owner / github_repo と同じ値。
	repoSlug = "uchidas-rogue/game-api-sample"
	// defaultBranch は出典リンクが指すブランチ。
	defaultBranch = "main"
	// filePerm は生成した索引のパーミッション。
	filePerm = 0o644
	// dirPerm は出力先ディレクトリのパーミッション。
	dirPerm = 0o755

	// topK と maxQuestionChars は、ブラウザ（web/app.js）とプロキシ
	// （web/worker/src/index.ts）の両方が使う上限。**本 const がその正本**。
	//
	// 索引に載せて両方に読ませるのは、同じ値を2つのランタイムへ手で書き写すと
	// ズレるため（以前は app.js の TOP_K / MAX_QUESTION と worker の
	// MAX_CHUNKS / MAX_QUESTION_CHARS、さらに index.html の maxlength に同じ数字が
	// 散らばっていて、担保は「クライアント側と同じ値」というコメントだけだった）。
	// worker も同じ索引をバンドルするので、生成物を1つ配れば両者は原理的にズレない。

	// topK はモデルへ渡すチャンク数。
	topK = 6
	// maxQuestionChars は質問文の最大文字数。
	maxQuestionChars = 500
)

// rootDocs は docs/ の外にある取り込み対象。
//
// logs/ を含めないのは、作業ログが公開向けに書かれていないため。
// .claude/** も含めない（Claude Code 専用の設定であり、規約の正本は AGENTS.md 側にある）。
func rootDocs() []string {
	return []string{
		"README.md",
		"AGENTS.md",
		"CLAUDE.md",
		"ROADMAP.md",
		"terraform/ARCHITECTURE.md",
		"loadtest/README.md",
	}
}

// index は web/data/index.json の中身。
type index struct {
	// Repo と Branch は出典リンクの組み立てに使う。
	Repo   string  `json:"repo"`
	Branch string  `json:"branch"`
	Limits limits  `json:"limits"`
	Chunks []chunk `json:"chunks"`
}

// limits はブラウザとプロキシが共有する上限値。
//
// json タグは web/app.js と web/worker/src/index.ts が読む。変更したら両方を直すこと
// （索引は生成物なので、フィールド名の変更は make site/check では検知できない。
// キーの存在は main_test.go が固定する）。
type limits struct {
	// TopK はモデルへ渡すチャンク数。
	TopK int `json:"topK"`
	// MaxQuestionChars は質問文の最大文字数。
	MaxQuestionChars int `json:"maxQuestionChars"`
}

func main() {
	// 出力先を差し替えられるようにしてあるのは make site/check のため。
	// 一時ファイルへ生成して既存の索引と比べれば、ワークツリーを書き換えずに
	// 再生成漏れを判定できる（git の状態にも依存しなくなる）。
	out := flag.String("o", outputPath, "索引の出力先（リポジトリルートからの相対パス）")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	if err := run(root, *out); err != nil {
		fail(err)
	}
}

// fail はエラーを表示して終了コード1で終わる。
func fail(err error) {
	fmt.Fprintln(os.Stderr, "sitegen:", err)
	os.Exit(1)
}

// run は root 配下の文書から索引を生成し、outRel へ書き出す。
// outRel は root からの相対パス、または絶対パス。
func run(root, outRel string) error {
	files, err := collectFiles(root)
	if err != nil {
		return err
	}

	chunks := make([]chunk, 0, len(files))
	for _, rel := range files {
		source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("%s の読み取りに失敗しました: %w", rel, err)
		}
		chunks = append(chunks, chunkDocument(rel, string(source))...)
	}

	payload, err := json.MarshalIndent(index{
		Repo:   repoSlug,
		Branch: defaultBranch,
		Limits: limits{TopK: topK, MaxQuestionChars: maxQuestionChars},
		Chunks: chunks,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("索引の JSON 化に失敗しました: %w", err)
	}
	payload = append(payload, '\n')

	// 絶対パスも受ける（site/check が一時ディレクトリへ出力するため）。
	out := filepath.FromSlash(outRel)
	if !filepath.IsAbs(out) {
		out = filepath.Join(root, out)
	}
	if err := os.MkdirAll(filepath.Dir(out), dirPerm); err != nil {
		return fmt.Errorf("%s の作成に失敗しました: %w", filepath.Dir(outRel), err)
	}
	if err := os.WriteFile(out, payload, filePerm); err != nil {
		return fmt.Errorf("%s の書き出しに失敗しました: %w", outRel, err)
	}

	fmt.Printf("%s を生成しました（%d ファイル / %d チャンク）\n", outRel, len(files), len(chunks))
	return nil
}

// collectFiles は取り込む markdown をリポジトリルートからの相対パスで返す。
// 並びはパス順に固定する（出力を決定論的にするため）。
func collectFiles(root string) ([]string, error) {
	var files []string
	for _, rel := range rootDocs() {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return nil, fmt.Errorf("取り込み対象 %s が見つかりません: %w", rel, err)
		}
		files = append(files, rel)
	}

	walkErr := filepath.WalkDir(filepath.Join(root, docsDir), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("%s の走査に失敗しました: %w", docsDir, walkErr)
	}

	sort.Strings(files)
	return files, nil
}
