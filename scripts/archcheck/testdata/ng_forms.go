// 違反: 右辺の形から実装型を特定できないパターン。(*T)(nil) / T{} / &T{} の
// いずれにも当てはまらない書き方は、配置を検証できないためすべて違反になる。
package fixture

type ifaceK interface{ DoK() }

var implKCh chan implK

var _ ifaceK = newImplK()

var _ ifaceK = (ifaceK)(nil)

var _ ifaceK = (*[]implK)(nil)

var _ ifaceK = &implKValue

var _ ifaceK = <-implKCh

type implK struct{}
