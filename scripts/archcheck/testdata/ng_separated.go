// 違反: var と実装型のあいだに別の宣言が挟まっている。
package fixture

type ifaceD interface{ DoD() }

var _ ifaceD = (*implD)(nil)

func helperD() {}

type implD struct{}

func (*implD) DoD() {}
