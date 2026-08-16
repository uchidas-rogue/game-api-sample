package doccheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// marker はテストコードの `// #N <図のパス>` コメント1つぶん。
type marker struct {
	// file はリポジトリルートからの相対パス。
	file string
	line int
	num  int
	// path はマーカーの `#N` に続く本文。パスを含まないこともある。
	path string
}

// markerFunc はマーカーを持つテスト関数。
type markerFunc struct {
	file string
	line int
	name string
}

// markerPattern はマーカー行の書式。コメントグループの**先頭行**にだけ意味を持たせる。
// 2行目以降は補足（`// #1 と同一パスだが…`）に使われており、ケースの宣言ではない。
var markerPattern = regexp.MustCompile(`^//\s*#(\d+)\s*(.*)$`)

// testMarkers は funcs に挙げた関数の中のマーカーを、宣言順・出現順に返す。
// 見つからなかった関数名を missing で返す。
func testMarkers(absPath, rel string, funcs []string) (found []marker, missing []string, err error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, fmt.Errorf("%s のパースに失敗しました: %w", rel, err)
	}

	decls := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil {
			decls[fn.Name.Name] = fn
		}
	}

	for _, name := range funcs {
		fn, ok := decls[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		found = append(found, markersIn(fset, file, rel, fn)...)
	}
	if len(missing) > 0 {
		return nil, missing, nil
	}
	return found, nil, nil
}

// markersIn は関数の本体に含まれるマーカーを出現順に返す。
func markersIn(fset *token.FileSet, file *ast.File, rel string, fn *ast.FuncDecl) []marker {
	var found []marker
	for _, group := range file.Comments {
		if group.Pos() < fn.Pos() || group.End() > fn.End() {
			continue
		}
		match := markerPattern.FindStringSubmatch(strings.TrimSpace(group.List[0].Text))
		if match == nil {
			continue
		}
		num, err := strconv.Atoi(match[1])
		if err != nil {
			// markerPattern が \d+ でしか一致しないため、到達するのは
			// int に収まらない桁数を書いた場合だけ。ケース番号として不正なので無視する。
			continue
		}
		found = append(found, marker{
			file: rel,
			line: fset.Position(group.List[0].Pos()).Line,
			num:  num,
			path: match[2],
		})
	}
	return found
}

// testFuncNames はファイル内のトップレベル関数名の集合を返す。
func testFuncNames(absPath string) (map[string]bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, absPath, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("%s のパースに失敗しました: %w", absPath, err)
	}
	names := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil {
			names[fn.Name.Name] = true
		}
	}
	return names, nil
}

// allMarkerFuncs は root 配下の *_test.go から、マーカーを持つ関数を集める。
func allMarkerFuncs(root string) ([]markerFunc, error) {
	var found []markerFunc
	fset := token.NewFileSet()

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
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("%s のパースに失敗しました: %w", rel, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			markers := markersIn(fset, file, filepath.ToSlash(rel), fn)
			if len(markers) == 0 {
				continue
			}
			found = append(found, markerFunc{
				file: filepath.ToSlash(rel),
				line: fset.Position(fn.Pos()).Line,
				name: fn.Name.Name,
			})
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return found, nil
}

// isSkippedDir は走査対象外のディレクトリ名を判定する。
//
// scripts/archcheck の同名関数と同じ方針（`.` / `_` 始まりと testdata を無視する
// Go ツールチェーンの慣習に沿う）。testdata を外すのは、本検査自身のフィクスチャが
// 意図的な違反を含むため。
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
