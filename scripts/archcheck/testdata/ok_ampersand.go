// &T{} 形式も型名が一意に決まるので配置を検証できる。本検査のスコープは配置であって
// 書式ではないため、規約が明示していない書式を配置違反として落とさない。
package fixture

type ifaceJ interface{ DoJ() }

var _ ifaceJ = &implJ{}

type implJ struct{}

func (*implJ) DoJ() {}
