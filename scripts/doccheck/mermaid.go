package doccheck

import (
	"fmt"
	"sort"
	"strings"
)

// diagram は mermaid のフローチャート1つぶん。
type diagram struct {
	// line はコードフェンスの開始行。
	line int
	// edges は有向辺の集合（"A→B" をキーにする）。
	edges map[string]bool
	// outDegree はノードごとの出次数。終端ノードの判定に使う。
	outDegree map[string]int
	// nodes は図に現れた全ノード（順序を保つ）。
	nodes []string
}

// arrow は本リポジトリの図が使う唯一の辺記法。
//
// mermaid には --o / --x / -.-> / ==> もあるが、docs/testing の全図が `-->` だけを
// 使っている。増やすときは実例が出てからにする（実例を伴わない一般化は、
// 検査の意図を読み取りにくくするだけで検出力を上げない）。
const arrow = "-->"

// parseDiagram はフローチャートの本文からノードと辺を取り出す。
//
// 【解釈の規則】
//   - ノードラベル（[] () {} の中身）は先に落とす。ラベルには `>` や `-` が含まれ
//     （`ParseInt かつ > 0`、`<br/>`）、そのままだと辺記法と区別できない
//   - 1行に `-->` が複数あるチェーン（`B -- 実エラー --> W[...] --> P[...]`）に対応する
//   - 辺ラベル（`B -- No --> E1` の `No`）は、矢印の手前の `--` 以降を落として除く
func parseDiagram(startLine int, body []string) diagram {
	d := diagram{line: startLine, edges: map[string]bool{}, outDegree: map[string]int{}}
	seen := map[string]bool{}

	addNode := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		d.nodes = append(d.nodes, name)
	}

	for _, raw := range body {
		line := stripLabels(raw)
		if isDirectiveLine(line) {
			continue
		}
		if !strings.Contains(line, arrow) {
			continue
		}

		segments := strings.Split(line, arrow)
		names := make([]string, 0, len(segments))
		for _, segment := range segments {
			// 辺ラベルは `--` の後ろに書かれるので、最初の `--` 以降を落とす。
			head, _, _ := strings.Cut(segment, "--")
			names = append(names, firstIdent(head))
		}
		for i := 0; i+1 < len(names); i++ {
			from, to := names[i], names[i+1]
			addNode(from)
			addNode(to)
			if from == "" || to == "" {
				continue
			}
			// 出次数は**行き先の種類**で数える。同じ辺を条件ラベル違いで2行に分けて
			// 書く形（`C -- listed == 0 --> Z` と `C -- listed < batchSize --> Z`）が
			// あるため、辺の出現回数で数えると分岐が無いノードまで「分岐あり」になり、
			// closeCoverage が止まって終端ノードを取りこぼす。
			key := edgeKey(from, to)
			if !d.edges[key] {
				d.edges[key] = true
				d.outDegree[from]++
			}
		}
	}
	return d
}

// terminals は出次数 0 のノード（正常終了・各エラーの終端）を返す。
func (d diagram) terminals() []string {
	var found []string
	for _, node := range d.nodes {
		if d.outDegree[node] == 0 {
			found = append(found, node)
		}
	}
	sort.Strings(found)
	return found
}

// edgeKey は辺の集合キー。
func edgeKey(from, to string) string { return from + "→" + to }

// isDirectiveLine は mermaid の描画指示行（辺を持たない行）かを判定する。
func isDirectiveLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	for _, prefix := range []string{"%%", "flowchart", "graph", "subgraph", "end", "direction", "classDef", "class ", "style", "linkStyle"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// stripLabels はノードラベル（[] () {} の中身）を落とす。入れ子の括弧に対応する。
func stripLabels(line string) string {
	var out strings.Builder
	depth := 0
	for _, r := range line {
		switch r {
		case '[', '(', '{':
			depth++
		case ']', ')', '}':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

// firstIdent は文字列の先頭に現れる識別子（ノード ID）を返す。無ければ空。
func firstIdent(s string) string {
	start := -1
	for i, r := range s {
		if isIdentRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			return s[start:i]
		}
	}
	if start >= 0 {
		return s[start:]
	}
	return ""
}

// isIdentRune はノード ID に使える文字かを判定する。
func isIdentRune(r rune) bool {
	return r == '_' ||
		('a' <= r && r <= 'z') ||
		('A' <= r && r <= 'Z') ||
		('0' <= r && r <= '9')
}

// checkDiagramCoverage は図と表の対応を検査する。
//
//   - 表の「図のパス」に書かれた隣接ノード対が、図の辺として実在するか
//   - 図の終端ノードが、いずれかのケースのパスに現れているか（README §6 の項目3）
//
// 辺も終端も**ファイル内の全図の和集合**で判定する。1つの文書に「本体のフロー」と
// 「共通のエラー分類フロー」が別々の図として置かれ、パスが両者をまたぐため
// （例: http-ranking.md の `…→X→R8` は X が本体側・R8 が handleError 側）。
// 図をまたぐ遷移は辺として存在しないので、両端が既知のノードでありさえすれば通す。
// これによりノードのリネーム・削除は捕まえつつ、文書の省略記法を壊さずに済む。
func checkDiagramCoverage(doc *document) []Violation {
	if len(doc.diagrams) == 0 {
		return nil
	}

	edges := map[string]bool{}
	owner := map[string]int{}
	for i, d := range doc.diagrams {
		for key := range d.edges {
			edges[key] = true
		}
		for _, node := range d.nodes {
			if _, ok := owner[node]; !ok {
				owner[node] = i
			}
		}
	}

	var violations []Violation
	covered := map[string]bool{}
	for _, table := range doc.tables {
		if table.anchor.kind == anchorSkip {
			continue
		}
		for _, row := range table.rows {
			for _, node := range mentionedNodes(row.path) {
				covered[node] = true
			}
			// 辺を検査するのはセル内の**最初のパス**だけ。補足の括弧には
			// 別のサブフローのパスに加えて散文も入る（実例: gacha.md の
			// 「`D6` が No→Yes」。矢印は使っているがノード ID ではない）。
			nodes := firstPathNodes(row.path)
			for i := 1; i < len(nodes); i++ {
				from, to := nodes[i-1], nodes[i]
				// 省略記号をまたぐ区間は復元しないので検査しない。
				if from == ellipsis || to == ellipsis {
					continue
				}
				if edges[edgeKey(from, to)] {
					continue
				}
				fromDiagram, fromKnown := owner[from]
				toDiagram, toKnown := owner[to]
				if fromKnown && toKnown && fromDiagram != toDiagram {
					continue
				}
				violations = append(violations, Violation{
					File: doc.rel,
					Line: row.line,
					Msg: fmt.Sprintf("ケース %d のパスにある `%s→%s` が図に無い。"+
						"図を変えたら表も直すこと（docs/testing/README.md §5）", row.num, from, to),
				})
			}
		}
	}

	closeCoverage(doc.diagrams, covered)

	for _, d := range doc.diagrams {
		for _, node := range d.terminals() {
			if covered[node] || doc.documentedAsUnimplemented(node) {
				continue
			}
			violations = append(violations, Violation{
				File: doc.rel,
				Line: d.line,
				Msg: fmt.Sprintf("終端ノード `%s` を通るケースが仕様表に無い。"+
					"分岐の網羅はパスカバレッジで担保しているため、終端が1つでも空くと担保が抜ける"+
					"（docs/testing/README.md §4・§6）。テストを足せないなら表に `未対応` の行として残すこと", node),
			})
		}
	}

	return violations
}

// closeCoverage は「分岐の無い続き」を覆われたものとして広げる。
//
// 仕様表のパスは、分岐が終わった時点で書き止めることが多い（`…→R→RT` の続きに
// 出口ノードが1つしか無いなら、そこへ進むことは自明）。出次数1のノードだけを
// たどるので、分岐の見落としは覆い隠さない。
func closeCoverage(diagrams []diagram, covered map[string]bool) {
	for _, d := range diagrams {
		for changed := true; changed; {
			changed = false
			for _, node := range d.nodes {
				if !covered[node] || d.outDegree[node] != 1 {
					continue
				}
				for key := range d.edges {
					from, to, _ := strings.Cut(key, "→")
					if from == node && !covered[to] {
						covered[to] = true
						changed = true
					}
				}
			}
		}
	}
}

// documentedAsUnimplemented は、終端ノードが `未対応` として明記されているかを判定する。
//
// テストが無いこと自体は README §7 が許している（穴を消さずに残す運用）。
// 判定は「同じ行にノード ID と `未対応` の両方がある」ことで行う。実例は
// http-ranking.md の `R4`（到達不可能なデッドコードで、仕様判断待ち）。
func (d *document) documentedAsUnimplemented(node string) bool {
	needle := "`" + node + "`"
	for _, line := range d.lines {
		if strings.Contains(line, needle) && strings.Contains(line, "未対応") {
			return true
		}
	}
	return false
}
