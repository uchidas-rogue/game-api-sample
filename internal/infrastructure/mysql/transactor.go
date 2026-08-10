// Package mysql は MySQL 接続まわりのインフラを提供する。
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// SQLTx が usecase 層の Tx を満たすことをコンパイル時に検証する。
var _ shared.Tx = (*SQLTx)(nil)

// SQLTx は usecase 層の Tx インターフェースに対する MySQL 実装。
// 内部の *sql.Tx は infrastructure 層の Repository 実装側でのみ取り出して使う。
type SQLTx struct {
	tx *sql.Tx
}

// IsTx は usecase.Tx を満たすマーカー。
func (*SQLTx) IsTx() {}

// Raw は内部の *sql.Tx を返す。infrastructure 層の Repository 実装からのみ呼び出す。
func (s *SQLTx) Raw() *sql.Tx { return s.tx }

// Transactor が usecase 層の Transactor を満たすことをコンパイル時に検証する。
var _ shared.Transactor = (*Transactor)(nil)

// Transactor は *sql.DB を用いたトランザクション境界制御の実装。
// usecase 層は内部の DoInTx を介してロジックを与え、本実装が
// BEGIN/COMMIT/ROLLBACK を保証する。
type Transactor struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewTransactor は Transactor を生成する。logger は呼び出し側で必ず初期化済みのものを渡す。
func NewTransactor(db *sql.DB, logger *slog.Logger) *Transactor {
	return &Transactor{db: db, logger: logger}
}

// toSQLTxOptions は usecase 層の TxOptions を database/sql の *sql.TxOptions へ変換する。
// デフォルト分離レベルの場合は nil を返し、driver 既定の挙動をそのまま使う。
func toSQLTxOptions(o shared.TxOptions) *sql.TxOptions {
	switch o.Isolation {
	case shared.IsolationReadCommitted:
		return &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	case shared.IsolationDefault:
		return nil
	default:
		return nil
	}
}

// DoInTx は与えられた fn をトランザクション内で実行する。
// fn が error を返した場合や panic した場合は ROLLBACK を保証する。
// opts で分離レベルを明示できる（省略時は MySQL のデフォルト = REPEATABLE READ）。
func (t *Transactor) DoInTx(ctx context.Context, fn func(tx shared.Tx) error, opts ...shared.TxOption) (err error) {
	tx, beginErr := t.db.BeginTx(ctx, toSQLTxOptions(shared.NewTxOptions(opts...)))
	if beginErr != nil {
		return fmt.Errorf("failed to begin tx: %w", beginErr)
	}
	defer func() {
		if p := recover(); p != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				t.logger.ErrorContext(ctx, "failed to rollback tx on panic", slog.Any("error", rbErr))
			}
			panic(p)
		}
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
				t.logger.ErrorContext(ctx, "failed to rollback tx", slog.Any("error", rbErr))
			}
			return
		}
		if cmErr := tx.Commit(); cmErr != nil {
			err = fmt.Errorf("failed to commit tx: %w", cmErr)
		}
	}()

	if err = fn(&SQLTx{tx: tx}); err != nil {
		return err
	}
	return nil
}
