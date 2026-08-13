// 値レシーバのみの型は Type{} 形式が許容される（AGENTS.md §2）。
package fixture

type ifaceB interface{ DoB() }

var _ ifaceB = implB{}

type implB struct{}

func (implB) DoB() {}
