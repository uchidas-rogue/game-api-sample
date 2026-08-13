// 規約どおりの形: var _ Iface = (*T)(nil) の直後に type T がある。
package fixture

type ifaceA interface{ DoA() }

var _ ifaceA = (*implA)(nil)

type implA struct{}

func (*implA) DoA() {}
