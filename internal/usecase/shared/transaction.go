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

// IsolationLevel はトランザクションの分離レベル。
// 既定は DB のデフォルト（MySQL では REPEATABLE READ）を用い、
// 変更が必要な場合のみトランザクション開始時に明示する。
type IsolationLevel int

const (
	// IsolationDefault は DB のデフォルト分離レベル（MySQL では REPEATABLE READ）。
	IsolationDefault IsolationLevel = iota

	// IsolationReadCommitted は READ COMMITTED。
	//
	// MySQL の REPEATABLE READ では、範囲を走査する SELECT ... FOR UPDATE が
	// レコードロックに加えてギャップロックを取得する。キュー的なテーブルを
	// 「未処理を先頭から N 件走査して確保する」形で読むと、走査が範囲の末尾に届いたとき
	// 新規 INSERT が入る隙間までロックしてしまい、書き込み側の INSERT をブロックする
	// （SKIP LOCKED はレコードロックを飛ばすだけでギャップロックは回避しない）。
	// READ COMMITTED ではインデックス走査でギャップロックを取らないため、この阻害が消える。
	//
	// 反面、同一トランザクション内での読み取り一貫性（反復可能読み取り）は失われる。
	// スナップショットの一貫性に依存する処理では使わないこと。
	IsolationReadCommitted
)

// TxOptions はトランザクション開始時の設定。
type TxOptions struct {
	Isolation IsolationLevel
}

// TxOption は TxOptions を組み立てる関数。
type TxOption func(*TxOptions)

// WithIsolation は分離レベルを明示する。
func WithIsolation(level IsolationLevel) TxOption {
	return func(o *TxOptions) { o.Isolation = level }
}

// NewTxOptions は TxOption 群を適用した TxOptions を返す。実装側のヘルパー。
func NewTxOptions(opts ...TxOption) TxOptions {
	var o TxOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Transactor はトランザクション境界を抽象化する。
// 実装は内部で BEGIN → fn → COMMIT/ROLLBACK を行う。fn が error を返した場合や
// panic 発生時は ROLLBACK を保証する責務を持つ。fn に渡される Tx は Repository
// メソッドへ透過的に引き回す。
//
// opts を省略した場合は DB のデフォルト分離レベルで開始する。
type Transactor interface {
	DoInTx(ctx context.Context, fn func(tx Tx) error, opts ...TxOption) error
}
