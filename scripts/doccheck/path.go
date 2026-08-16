package doccheck

import (
	"regexp"
	"strings"
)

// ellipsis は仕様表とマーカーの両方で使う「途中省略」の記号。
// 省略をまたぐ区間は復元しないため、辺の検査から外す目印になる。
const ellipsis = "…"

// pathPattern は `A→B→E1` 形式のパスにマッチする。先頭が矢印で始まる形
// （`→BE2`。「どこから来ても」の意味で使われる）も拾う。
var pathPattern = regexp.MustCompile(`(?:[A-Za-z0-9_…]+)?(?:→[A-Za-z0-9_…]+)+`)

// identPattern はセル内に単独で現れるノード ID（`R7` のような分岐先の別記）を拾う。
var identPattern = regexp.MustCompile(`[A-Za-z0-9_]+`)

// firstPath はセルやマーカー本文から最初のパスを取り出し、比較用に正規化する。
// パスが無ければ空文字列を返す（`12 と同一` のような参照形）。
//
// 補足の括弧（`…→G→J→E7`（`D1→D2→DE`））に入っている2つ目以降のパスは、別の
// サブフローを指すため比較には使わない。
func firstPath(cell string) string {
	return pathPattern.FindString(normalizeCell(cell))
}

// firstPathNodes はセル内の最初のパスをノード列として返す。辺の実在検査に使う。
func firstPathNodes(cell string) []string {
	found := firstPath(cell)
	if found == "" {
		return nil
	}
	return strings.Split(strings.Trim(found, "→"), "→")
}

// mentionedNodes はセル内に現れる識別子を全て返す。終端ノードの網羅判定に使う。
//
// パスに含まれないノード（`A→B→C→X→R1` / `R2` と書いたときの `R2` のように、
// 同じ辺の行き先違いを別記したもの）を取りこぼさないため、パス以外の識別子も拾う。
func mentionedNodes(cell string) []string {
	return identPattern.FindAllString(normalizeCell(cell), -1)
}

// normalizeCell は比較の前に装飾を落とす。
func normalizeCell(cell string) string {
	replacer := strings.NewReplacer("`", "", "*", "", " ", "", "　", "")
	return replacer.Replace(cell)
}

// testNamePattern はテスト関数の参照（`TestXxx` / `TestXxx/サブテスト名`）を拾う。
var testNamePattern = regexp.MustCompile("`(Test[^`/]+(?:/[^`]+)?)`")

// testNameRefs は「対応テスト」列からテスト関数の参照を取り出す。
func testNameRefs(cell string) []string {
	var refs []string
	for _, match := range testNamePattern.FindAllStringSubmatch(cell, -1) {
		refs = append(refs, strings.TrimSpace(match[1]))
	}
	return refs
}
