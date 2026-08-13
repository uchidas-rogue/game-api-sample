// 他パッケージの型を対象にした assertion は対象外。型定義がこのファイルに無いため
// 「直前」が構造的に定義できない。internal/di/container.go のコンポジションルート用
// ブロックがこの形にあたる。
package fixture

import (
	"io"
	"strings"
)

var (
	_ io.Reader     = (*strings.Reader)(nil)
	_ io.ByteReader = (*strings.Reader)(nil)
)

type unrelated struct{}
