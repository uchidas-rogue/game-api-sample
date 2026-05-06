// Package shared は複数のユースケースで共有するトランザクション抽象を提供する。
package shared

//go:generate mockgen -source=transaction.go -destination=mock/mock_transaction.go -package=mock_shared

import "context"

// Tx はトランザクション境界を表す不透明な型。
// usecase 層は実体（*sql.Tx 等）に依存せず、Transactor から受け取った値を
// Repository メソッドに引き回すだけの不透明トークンとして扱う。
// infrastructure 層が IsTx() を持つ具象型でこのインターフェースを満たす。
//
// nil は「トランザクション境界外で実行する」ことを表す特別な値。
// Repository 実装は nil を受領した場合、トランザクションを開始せず DB 接続から
// 直接クエリを発行する責務を持つ（読み取り専用操作で usecase 層が利用する）。
type Tx interface {
	// IsTx は実装識別用のマーカー。usecase 層からは呼ばない。
	IsTx()
}

// Transactor はトランザクション境界を抽象化する。
// 実装は内部で BEGIN → fn → COMMIT/ROLLBACK を行う。fn が error を返した場合や
// panic 発生時は ROLLBACK を保証する責務を持つ。fn に渡される Tx は Repository
// メソッドへ透過的に引き回す。
type Transactor interface {
	DoInTx(ctx context.Context, fn func(tx Tx) error) error
}
