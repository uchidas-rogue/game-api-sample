// 違反: var の後に宣言が無い（ファイル末尾）。sqlc 生成の querier.go と同じ形だが、
// こちらは生成マーカーが無いので検査対象になる。
package fixture

type ifaceE interface{ DoE() }

var _ ifaceE = (*implE)(nil)
