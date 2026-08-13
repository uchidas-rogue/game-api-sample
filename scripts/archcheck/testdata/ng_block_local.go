// 違反: var ( ... ) ブロックに同一パッケージの型のエントリが 2 つある。
// 直前になれるのは 1 つだけなので、残りは必ず違反になる。
package fixture

type ifaceF interface{ DoF() }

type ifaceG interface{ DoG() }

var (
	_ ifaceF = (*implF)(nil)
	_ ifaceG = (*implG)(nil)
)

type implF struct{}

type implG struct{}
