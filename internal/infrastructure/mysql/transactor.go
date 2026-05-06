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

// SQLTx は usecase 層の Tx インターフェースに対する MySQL 実装。
// 内部の *sql.Tx は infrastructure 層の Repository 実装側でのみ取り出して使う。
var _ shared.Tx = (*SQLTx)(nil)

type SQLTx struct {
	tx *sql.Tx
}

// IsTx は usecase.Tx を満たすマーカー。
func (*SQLTx) IsTx() {}

// Raw は内部の *sql.Tx を返す。infrastructure 層の Repository 実装からのみ呼び出す。
func (s *SQLTx) Raw() *sql.Tx { return s.tx }

// Transactor は *sql.DB を用いたトランザクション境界制御の実装。
// usecase 層は内部の DoInTx を介してロジックを与え、本実装が
// BEGIN/COMMIT/ROLLBACK を保証する。
var _ shared.Transactor = (*Transactor)(nil)

type Transactor struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewTransactor は Transactor を生成する。logger は呼び出し側で必ず初期化済みのものを渡す。
func NewTransactor(db *sql.DB, logger *slog.Logger) *Transactor {
	return &Transactor{db: db, logger: logger}
}

// DoInTx は与えられた fn をトランザクション内で実行する。
// fn が error を返した場合や panic した場合は ROLLBACK を保証する。
func (t *Transactor) DoInTx(ctx context.Context, fn func(tx shared.Tx) error) (err error) {
	tx, beginErr := t.db.BeginTx(ctx, nil)
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
