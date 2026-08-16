// 他パッケージの型は複合リテラル形式（pkg.T{}）でも対象外。
package fixture

import "strings"

type ifaceM interface{ Len() int }

var _ ifaceM = strings.Builder{}

type unrelatedM struct{}
