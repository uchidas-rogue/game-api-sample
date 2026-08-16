// var と型のあいだに型の doc コメントが挟まっていても「直前」とみなす。
// doc コメントは GenDecl.Doc に吸収され、宣言リスト上は隣接しているため。
package fixture

type ifaceC interface{ DoC() }

// implC が ifaceC を満たすことをコンパイル時に検証する。
var _ ifaceC = (*implC)(nil)

// implC は ifaceC の既定実装。
type implC struct{}

func (*implC) DoC() {}
