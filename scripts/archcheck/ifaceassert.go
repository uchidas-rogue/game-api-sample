// Package archcheck は、golangci-lint では表現できないアーキテクチャ規約を
// AST を直接読んで検査する。
//
// golangci-lint 側との役割分担は次のとおり。
//   - 関数名だけで判定できる禁止 API → .golangci.yml の forbidigo
//   - 引数の内容まで見る式単位の規約 → scripts/ruleguard/rules.go（gocritic/ruleguard）
//   - 「文の並び」に関する規約 → 本パッケージ（ruleguard は式単位のマッチなので表現できない）
//
// 検査は go test から実行される（ローカルと CI の等価性 / determ. §7）。
// Makefile の TEST_PKGS が go list ./... から算出するため、追加の配線は不要。
package archcheck

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// Violation は規約違反を1件表す。
type Violation struct {
	// File は検査ルートからの相対パス（スラッシュ区切り）。
	File string
	// Line は違反した宣言の行番号。
	Line int
	// Msg は違反の内容と直し方。
	Msg string
}

// String は "path:line: message" 形式の1行表現を返す。
func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.File, v.Line, v.Msg)
}

// CheckIfaceAssert は AGENTS.md §2「インターフェース実装検証: 実装型の定義直前に
// var _ Iface = (*Type)(nil) を記述する（値レシーバのみは Type{} 可）」の
// **配置**を検査する。root 配下の .go ファイルを再帰的に走査する。
//
// 【この検査が捕捉する既知の実例】
// 検証用の var を型から離して置くと、型を移動・リネーム・削除したときに
// 検証だけが取り残されても誰も気づけない。実際 defaultRandomizer（gacha）では
// 型の doc コメントが検証用 var に付いてしまう誤りが 5 件発生しており
// （.golangci.yml の revive 採用理由を参照）、これは var と型が隣接している
// ことを前提に書かれた規約が守られているかを見る手段が無いことに起因する。
//
// 【なぜ ruleguard では書けないか】
// 「直前にあるか」は宣言リスト上の並びであって式の形ではない。ruleguard は
// 式単位のマッチなので「この宣言の次の宣言が何か」を条件にできない。
// そのため AST を直接読む（determ. §3）。順序を見る検査としては
// TestNew_MiddlewareOrder（ミドルウェア登録順）に次いで 2 つ目にあたる。
//
// 【規則】
// 検査するのは package スコープの `var _ Iface = <式>` のみ。右辺から実装型名を
// 取り出し、その var 宣言の直後の宣言が当該型の type 宣言であることを要求する。
//   - 右辺が他パッケージの型（(*pkg.T)(nil) 等）→ 対象外。型定義がそのファイルに
//     無いので「直前」が構造的に定義できない。internal/di/container.go の
//     コンポジションルート用ブロックがこれに当たり、パス名の例外を作らずに外れる
//   - 右辺から型名を特定できない書き方 → 違反として扱う（フェイルクローズド）
//   - // Code generated ... DO NOT EDIT. 付きのファイル → 対象外
//
// 【検出できないもの（既知の検出漏れ / determ. §3）】
//   - interface を実装しているのに assertion が無い型。go/types による型解決が必要で、
//     DI 用でない偶然の実装まで拾う誤検出リスクがあるため本検査の対象外
//   - 他パッケージ型を対象にした assertion の配置（上記のとおり構造上判定できない）。
//     internal/di については配線漏れがビルドエラーになることで担保される
//   - doc コメントの誤配置（型の説明が検証用 var に付いている）。配置そのものは
//     正しいので検出しない。判定には自然言語の解釈が要り、文字列ヒューリスティックは
//     determ. §3 に反する
//   - 実装型が type (...) ブロックの 2 番目以降にある場合。先頭の TypeSpec しか見ないため
//     違反として報告される（フェイルクローズド）
//   - ビルドタグで除外されたファイル。parser はタグを解釈しないため、むしろ検査対象に入る
//
// 【違反が出たときの直し方】
// //nolint 相当の抜け道は用意していない（determ. §2）。検証用の var を実装型の
// 直前へ移動する。他パッケージの型を検証したい場合は internal/di の
// コンポジションルート用ブロックへ寄せる。
func CheckIfaceAssert(root string) ([]Violation, error) {
	fset := token.NewFileSet()

	var violations []Violation
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && isSkippedDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found, err := checkFile(fset, path, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		violations = append(violations, found...)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return violations, nil
}

// isSkippedDir は走査対象外のディレクトリ名を判定する。
//
// testdata を外すのは、本検査自身のフィクスチャが意図的な違反を含むため。
// `.` / `_` 始まりと testdata を無視するのは Go ツールチェーン自身の慣習に沿う。
func isSkippedDir(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}
	switch name {
	case "bin", "logs", "vendor", "node_modules", "testdata",
		"terraform", "loadtest", "deployments":
		return true
	default:
		return false
	}
}

// checkFile は1ファイルを検査する。rel は失敗メッセージに出す相対パス。
func checkFile(fset *token.FileSet, path, rel string) ([]Violation, error) {
	// ParseComments は ast.IsGenerated が生成マーカーを読むために必須。
	// 型解決は不要なので SkipObjectResolution を付ける。
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("%s のパースに失敗しました: %w", rel, err)
	}
	// 生成物はパスではなく生成マーカーで判定する。パスで持つと mock / sqlc の
	// 除外リスト（make/app.mk・.golangci.yml・.testignore で書式が全部違う）に
	// 4 つ目を足すことになり、同期すべき箇所が増えるため。
	if ast.IsGenerated(file) {
		return nil, nil
	}

	var violations []Violation
	for i, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || !isBlankAssertion(value) {
				continue
			}

			line := fset.Position(value.Pos()).Line
			kind, typeName := assertedType(value.Values[0])
			switch kind {
			case targetExternal:
				// 他パッケージの型。構造上「直前」を定義できないので対象外。
				continue
			case targetUnknown:
				violations = append(violations, Violation{
					File: rel,
					Line: line,
					Msg: fmt.Sprintf(
						"%s の右辺から実装型を特定できない。(*T)(nil) / T{} / &T{} のいずれかで書くこと"+
							"（AGENTS.md §2）。特定できない書き方は配置を検証できないため違反として扱う",
						declText(fset, value)),
				})
			case targetLocal:
				if next, ok := nextTypeName(file.Decls, i); ok && next == typeName {
					continue
				}
				violations = append(violations, Violation{
					File: rel,
					Line: line,
					Msg: fmt.Sprintf(
						"%s の直後が型 %s の定義ではない（直後にあるのは %s）。実装型の定義直前に置くこと"+
							"（AGENTS.md §2）。離れていると、型の移動・削除で検証だけが取り残されても気づけない",
						declText(fset, value), typeName, describeNextDecl(file.Decls, i)),
				})
			}
		}
	}
	return violations, nil
}

// isBlankAssertion は `_ Iface = <式>` の形かを判定する。
// var ( ... ) ブロック内のエントリも同じ形で拾える。
func isBlankAssertion(value *ast.ValueSpec) bool {
	return len(value.Names) == 1 &&
		value.Names[0].Name == "_" &&
		value.Type != nil &&
		len(value.Values) == 1
}

// targetKind は assertion の右辺から取り出した実装型の素性。
type targetKind int

const (
	// targetUnknown は右辺から実装型名を特定できないことを表す。
	targetUnknown targetKind = iota
	// targetLocal は実装型が同一パッケージにあることを表す。
	targetLocal
	// targetExternal は実装型が他パッケージにあることを表す。
	targetExternal
)

// assertedType は assertion の右辺から実装型を取り出す。
//
// &T{} を認識するのは、規約が明示していない形でも型名が一意に決まるため。
// 本検査のスコープは配置であって書式ではないので、書式違いを配置違反にしない。
func assertedType(expr ast.Expr) (targetKind, string) {
	switch node := expr.(type) {
	case *ast.CallExpr: // (*T)(nil)
		paren, ok := node.Fun.(*ast.ParenExpr)
		if !ok {
			return targetUnknown, ""
		}
		star, ok := paren.X.(*ast.StarExpr)
		if !ok {
			return targetUnknown, ""
		}
		return namedType(star.X)
	case *ast.CompositeLit: // T{}
		return namedType(node.Type)
	case *ast.UnaryExpr: // &T{}
		if node.Op != token.AND {
			return targetUnknown, ""
		}
		return assertedType(node.X)
	default:
		return targetUnknown, ""
	}
}

// namedType は型式が同一パッケージの識別子か、他パッケージの修飾名かを判定する。
func namedType(expr ast.Expr) (targetKind, string) {
	switch node := expr.(type) {
	case *ast.Ident:
		return targetLocal, node.Name
	case *ast.SelectorExpr:
		return targetExternal, node.Sel.Name
	default:
		return targetUnknown, ""
	}
}

// nextTypeName は decls[i] の直後の宣言が type 宣言なら、その先頭の型名を返す。
//
// type ( ... ) ブロックの場合に先頭の TypeSpec しか見ないのは、「直前」を
// 曖昧さなく定義するため。2 番目以降に置いた場合は違反として報告される。
func nextTypeName(decls []ast.Decl, i int) (string, bool) {
	if i+1 >= len(decls) {
		return "", false
	}
	gen, ok := decls[i+1].(*ast.GenDecl)
	if !ok || gen.Tok != token.TYPE || len(gen.Specs) == 0 {
		return "", false
	}
	spec, ok := gen.Specs[0].(*ast.TypeSpec)
	if !ok {
		return "", false
	}
	return spec.Name.Name, true
}

// describeNextDecl は失敗メッセージ用に、decls[i] の直後にある宣言を短く説明する。
func describeNextDecl(decls []ast.Decl, i int) string {
	if i+1 >= len(decls) {
		return "宣言が無い（ファイル末尾）"
	}
	switch node := decls[i+1].(type) {
	case *ast.FuncDecl:
		return "func " + node.Name.Name
	case *ast.GenDecl:
		// type 宣言なら型名まで出す。それ以外（var / const / import）は
		// Tok の文字列表現をそのまま使う。import は Go の文法上 var の後ろに
		// 来ないが、分岐を増やさないためここで一緒に扱う。
		if name, ok := nextTypeName(decls, i); ok {
			return "type " + name
		}
		return node.Tok.String() + " 宣言"
	default:
		// ast.Decl の実装は FuncDecl / GenDecl / BadDecl の3つ。BadDecl は
		// パースエラー時にしか現れず、その場合は checkFile が先に error を返す。
		return "判別できない宣言"
	}
}

// declText は失敗メッセージ用に ValueSpec を元のソース表記へ戻す。
func declText(fset *token.FileSet, value *ast.ValueSpec) string {
	var buf strings.Builder
	if err := format.Node(&buf, fset, value); err != nil {
		return "<表示できない宣言>"
	}
	return "var " + buf.String()
}
