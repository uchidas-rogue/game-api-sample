// Package doccheck は docs/testing/<機能>.md のテスト仕様表と、対応する Go の
// テストコードが 1 対 1 で保たれているかを検査する。
//
// docs/testing/README.md §6 のレビューゲートのうち、次の3項目を機械判定へ移したもの。
//   - 項目1: 仕様表のケース番号とテストコードのケース順が一致している
//   - 項目2: 「図のパス」の数とテストコードのケース数が一致している
//   - 項目3: 図中のすべての終端ノードに、対応するケースが最低1件ある
//
// 【この検査が捕捉する既知の実例】
// ガチャの「ListItems をトランザクションの外へ出す」変更で、docs/testing/gacha.md と
// 実装は更新されたが internal/usecase/gacha/usecase_test.go の並び順と `// #N` マーカーだけが
// 旧構造のまま取り残された（#3〜#8 の6件が不一致）。同ファイルには
// 「表と 1 対 1 で対応する」「並び順は図のパスが短い順」と書かれており、宣言と実態が
// 食い違ったまま CI を通過していた。もう1件、docs/testing/outbox-worker.md の
// テスト関数名に余分な半角スペースが入り、文書から実関数へ辿れなくなっていた。
//
// 【なぜ既存の仕組みでは足りないか】
//   - go test -cover は文カバレッジしか出さない。分岐の網羅は docs/testing の
//     パスカバレッジで代替する設計（README §4）だが、その代替が効くのは表とコードが
//     対応している間だけで、対応の検証だけが人手に残っていた
//   - scripts/docs-ssot-check.sh は bash で、Go の AST を読めない。マーカーの所属関数や
//     テーブル要素との位置関係は構文解析が要る（determ. §3）
//
// 検査は go test から実行される（ローカルと CI の等価性 / determ. §7）。
// Makefile の TEST_PKGS が go list ./... から算出するため、追加の配線は不要。
//
// ---- アンカー記法（本コメントが記法の正本）--------------------------------------
//
// どの仕様表がどのテストへ対応するかは、表の直前の HTML コメントで宣言する。
// `#` 列を持つ表はいずれかの宣言を必ず持たなければならない（ホワイトリスト方式 / determ. §4）。
//
//	<!-- testcases: <テストファイル>#<関数名>[+<関数名>...] -->
//	    表の各行を、指定した関数の中の `// #N <図のパス>` マーカーと突合する。
//	    関数を + で連ねた場合は、宣言順に連結して1つの並びとして扱う
//	    （1つの表が複数のテスト関数へ分かれている場合）。
//
//	<!-- testcases-funcs: <テストファイル> -->
//	    表の最終列にテスト関数名が書かれている形式。各セルの `Func` / `Func/サブテスト名`
//	    が実在することだけを検査する（ケース順は突合しない）。
//
//	<!-- testcases-skip: '<理由>' -->
//	    突合の対象外にする。理由は必須（ssot-assert の manual と同じ考え方）。
//
// テストコード側のマーカーは、テーブル駆動の要素の直前に置く。
//
//	{
//	    // #1 A→B→E1
//	    name: "...",
//	}
//
// 1つのマーカーは「次のマーカーまでの要素すべて」を覆う。表の1行が意図的に複数の
// テストケースへ展開される場合（構造が同じで入力だけ違う等）に例外宣言を要らなくするため。
//
// 【検出できないもの（既知の検出漏れ / determ. §3）】
//   - t.Run 名と表の「条件」列の一致。コード側は意図的に情報量を足しており
//     （表「pullCount が範囲外（0）」／コード「pullCount が下限未満（0）: DoInTx に入らない」）、
//     一致を強制すると README §3 の「条件 = 短い説明」という設計を壊す
//   - パス末尾の補足（`…→I→J→E7`（`D1→D2→DE`）の括弧内）は、エッジ実在と終端網羅では
//     読むがコードとの一致比較では読まない。別サブフローの図を指すため
//   - `12 と同一` のような参照形のパス指定。コードとの一致比較は行わない（行数と番号は突合する）
//   - パスの `…`（省略記号）をまたぐ隣接ノード対。省略の復元は行わないためエッジ検査から外れる
//   - エッジの実在は「同一ファイル内の全フローチャートの和集合」で判定する。別の図に
//     同名のエッジがあると通ってしまう（図ごとの帰属までは見ない）
//   - testcases-funcs のサブテスト名は、ファイル内に同じ文字列が現れるかで判定する。
//     t.Run の引数であることまでは確認しない
//
// 【違反が出たときの直し方】
// //nolint 相当の抜け道は用意していない（determ. §2）。表とテストのどちらが古いかを
// 判断して直す。テストが無いことが分かっている穴は、表の行に `未対応` と書いて残す
// （docs/testing/README.md §7）。
package doccheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Violation は規約違反を1件表す。
type Violation struct {
	// File は検査ルートからの相対パス（スラッシュ区切り）。
	File string
	// Line は違反した箇所の行番号。
	Line int
	// Msg は違反の内容と直し方。
	Msg string
}

// String は "path:line: message" 形式の1行表現を返す。
func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.File, v.Line, v.Msg)
}

// specDir は検査対象の文書を置くディレクトリ（検査ルートからの相対パス）。
const specDir = "docs/testing"

// CheckSpecTables は root 配下の docs/testing/*.md と、そこから参照される
// テストコードを突合する。パッケージ doc の記法に従う。
//
// root にはリポジトリルートを渡す。文書からテストファイルへの参照は
// リポジトリルートからの相対パスで書かれているため。
func CheckSpecTables(root string) ([]Violation, error) {
	entries, err := os.ReadDir(filepath.Join(root, specDir))
	if err != nil {
		return nil, fmt.Errorf("%s の読み取りに失敗しました: %w", specDir, err)
	}

	var (
		violations []Violation
		// referenced は「アンカーから参照されたテスト関数」の集合。
		// 参照されないマーカー（表が消えた・関数がリネームされた）の検出に使う。
		referenced = map[funcRef]bool{}
	)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		// README.md は運用ルールであって仕様表ではない。principles/ はディレクトリなので
		// 上の IsDir で既に外れている。
		if entry.Name() == "README.md" {
			continue
		}

		rel := specDir + "/" + entry.Name()
		doc, err := parseDoc(filepath.Join(root, rel), rel)
		if err != nil {
			return nil, err
		}

		found, refs, err := checkDoc(root, doc)
		if err != nil {
			return nil, err
		}
		violations = append(violations, found...)
		for _, ref := range refs {
			referenced[ref] = true
		}
	}

	orphans, err := checkOrphanMarkers(root, referenced)
	if err != nil {
		return nil, err
	}
	violations = append(violations, orphans...)

	sortViolations(violations)
	return violations, nil
}

// funcRef はテストファイルと関数名の組。
type funcRef struct {
	file string // リポジトリルートからの相対パス
	name string
}

// checkDoc は1つの文書に含まれる全ての仕様表を検査し、参照したテスト関数を返す。
func checkDoc(root string, doc *document) ([]Violation, []funcRef, error) {
	var (
		violations []Violation
		refs       []funcRef
	)

	for _, table := range doc.tables {
		// `#` 列を持たない表は仕様表ではない（起動経路の一覧、設計判断の対比表など）。
		// 突合の対象にはしないが、「図のパス」列があれば終端ノードの網羅判定には使う。
		if !table.numbered {
			continue
		}
		switch table.anchor.kind {
		case anchorNone:
			violations = append(violations, Violation{
				File: doc.rel,
				Line: table.line,
				Msg: "仕様表にアンカーが無い。表の直前に <!-- testcases: <テストファイル>#<関数名> --> を置くこと" +
					"（対象外にするなら <!-- testcases-skip: '<理由>' -->）。" +
					"アンカーが無いと、表とテストのどちらが古いかを機械判定できない",
			})
		case anchorInvalid:
			violations = append(violations, Violation{
				File: doc.rel,
				Line: table.anchor.line,
				Msg:  table.anchor.msg,
			})
		case anchorSkip:
			// 理由付きの明示的な除外。parseAnchor が理由の有無を検査済み。
		case anchorMarkers:
			found, err := checkMarkerTable(root, doc, table)
			if err != nil {
				return nil, nil, err
			}
			violations = append(violations, found...)
			for _, name := range table.anchor.funcs {
				refs = append(refs, funcRef{file: table.anchor.file, name: name})
			}
		case anchorFuncs:
			found, err := checkFuncTable(root, doc, table)
			if err != nil {
				return nil, nil, err
			}
			violations = append(violations, found...)
		}
	}

	violations = append(violations, checkDiagramCoverage(doc)...)
	return violations, refs, nil
}

// checkMarkerTable は testcases アンカーの表を `// #N` マーカーと突合する。
func checkMarkerTable(root string, doc *document, table specTable) ([]Violation, error) {
	anchor := table.anchor
	absPath := filepath.Join(root, filepath.FromSlash(anchor.file))
	if _, err := os.Stat(absPath); err != nil {
		return []Violation{{
			File: doc.rel,
			Line: anchor.line,
			Msg:  fmt.Sprintf("アンカーが指すテストファイル %s が存在しない", anchor.file),
		}}, nil
	}

	markers, missing, err := testMarkers(absPath, anchor.file, anchor.funcs)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return []Violation{{
			File: doc.rel,
			Line: anchor.line,
			Msg: fmt.Sprintf("アンカーが指すテスト関数 %s が %s に無い。リネームしたら文書側も直すこと",
				strings.Join(missing, " / "), anchor.file),
		}}, nil
	}

	var violations []Violation
	if len(markers) != len(table.rows) {
		violations = append(violations, Violation{
			File: doc.rel,
			Line: table.line,
			Msg: fmt.Sprintf("表の行数（%d）と %s のマーカー数（%d）が違う。表の1行 = テストコードの1ケース"+
				"（docs/testing/README.md §3）。行を足し引きしたら `// #N` も同じ数にすること",
				len(table.rows), anchor.describeTarget(), len(markers)),
		})
	}

	for i, row := range table.rows {
		if i >= len(markers) {
			break
		}
		marker := markers[i]
		if marker.num != row.num {
			violations = append(violations, Violation{
				File: marker.file,
				Line: marker.line,
				Msg: fmt.Sprintf("%d 番目のケースのマーカーが `// #%d` だが、%s:%d の表では `%d`。"+
					"ケース番号と並び順は表と一致させること（docs/testing/README.md §6）",
					i+1, marker.num, doc.rel, row.line, row.num),
			})
			continue
		}
		if v, ok := comparePath(doc, row, marker); !ok {
			violations = append(violations, v)
		}
	}

	return violations, nil
}

// comparePath は表の「図のパス」列とマーカーのパスを比較する。
// 片方でもパスを読み取れない場合（`12 と同一` のような参照形）は比較しない。
func comparePath(doc *document, row specRow, marker marker) (Violation, bool) {
	docPath := firstPath(row.path)
	codePath := firstPath(marker.path)
	if docPath == "" || codePath == "" || docPath == codePath {
		return Violation{}, true
	}
	return Violation{
		File: marker.file,
		Line: marker.line,
		Msg: fmt.Sprintf("ケース %d の図のパスが不一致。コード `%s` / %s:%d の表 `%s`。"+
			"実装を変えたら 図 → 表 → テスト の順で更新すること（docs/testing/README.md §5）",
			marker.num, codePath, doc.rel, row.line, docPath),
	}, false
}

// checkFuncTable は testcases-funcs アンカーの表を、最終列のテスト関数名の実在で検査する。
func checkFuncTable(root string, doc *document, table specTable) ([]Violation, error) {
	anchor := table.anchor
	absPath := filepath.Join(root, filepath.FromSlash(anchor.file))
	// ファイル不在は検査の失敗ではなく検査結果（文書が古い）として報告する。
	source, readErr := os.ReadFile(absPath)
	if readErr != nil {
		return []Violation{{
			File: doc.rel,
			Line: anchor.line,
			Msg:  fmt.Sprintf("アンカーが指すテストファイル %s が存在しない", anchor.file),
		}}, nil
	}
	names, err := testFuncNames(absPath)
	if err != nil {
		return nil, err
	}

	var violations []Violation
	for _, row := range table.rows {
		if row.unimplemented {
			continue
		}
		lastCell := row.cells[len(row.cells)-1]
		refs := testNameRefs(lastCell)
		if len(refs) == 0 {
			// 直前の行と同じテストを指す場合は `同上` と書く運用（表の可読性のため）。
			if strings.Contains(lastCell, "同上") {
				continue
			}
			violations = append(violations, Violation{
				File: doc.rel,
				Line: row.line,
				Msg: "最終列にテスト関数名が無い。`TestXxx` か `TestXxx/サブテスト名` を書くこと" +
					"（同じ関数を指すなら `同上`、テストが無いなら `未対応`）",
			})
			continue
		}
		for _, ref := range refs {
			fn, sub, _ := strings.Cut(ref, "/")
			if !names[fn] {
				violations = append(violations, Violation{
					File: doc.rel,
					Line: row.line,
					Msg: fmt.Sprintf("テスト関数 `%s` が %s に無い。リネームしたら文書側も直すこと"+
						"（全角・半角スペースの混入も同じ結果になる）", fn, anchor.file),
				})
				continue
			}
			if sub != "" && !strings.Contains(string(source), sub) {
				violations = append(violations, Violation{
					File: doc.rel,
					Line: row.line,
					Msg:  fmt.Sprintf("サブテスト名 `%s` が %s に無い", sub, anchor.file),
				})
			}
		}
	}
	return violations, nil
}

// checkOrphanMarkers は、どのアンカーからも参照されていない `// #N` マーカーを報告する。
//
// 表が消えた・関数がリネームされたのにマーカーだけが残ると、パスカバレッジの根拠が
// 静かに失われる。アンカー側だけを見ていると気づけないため、コード側からも突き合わせる。
func checkOrphanMarkers(root string, referenced map[funcRef]bool) ([]Violation, error) {
	found, err := allMarkerFuncs(root)
	if err != nil {
		return nil, err
	}

	var violations []Violation
	for _, fn := range found {
		if referenced[funcRef{file: fn.file, name: fn.name}] {
			continue
		}
		violations = append(violations, Violation{
			File: fn.file,
			Line: fn.line,
			Msg: fmt.Sprintf("%s の `// #N` マーカーが、どの仕様表からも参照されていない。"+
				"docs/testing/<機能>.md の表に <!-- testcases: %s#%s --> を置くか、マーカーを消すこと",
				fn.name, fn.file, fn.name),
		})
	}
	return violations, nil
}

// sortViolations は出力順を安定させる（ファイル名 → 行番号）。
func sortViolations(violations []Violation) {
	sort.SliceStable(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})
}
