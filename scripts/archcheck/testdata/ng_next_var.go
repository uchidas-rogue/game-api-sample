// 違反: var の直後が型定義ではなく別の var 宣言。
package fixture

type ifaceL interface{ DoL() }

var _ ifaceL = (*implL)(nil)

var implLDefault implL

type implL struct{}
