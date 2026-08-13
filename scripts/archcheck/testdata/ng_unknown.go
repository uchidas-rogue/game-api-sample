// 違反: 右辺が変数参照で、実装型を特定できない。配置を検証できないため
// フェイルクローズドで違反として扱う。
package fixture

type ifaceH interface{ DoH() }

var implHValue implH

var _ ifaceH = implHValue

type implH struct{}

func (implH) DoH() {}
