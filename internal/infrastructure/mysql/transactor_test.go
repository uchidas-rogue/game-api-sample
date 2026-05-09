// Package mysql_test は Transactor の外部テスト。
// DATA-DOG/go-sqlmock を用いて BeginTx / Commit / Rollback の制御を検証する。
package mysql_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

func newTransactor(t *testing.T, db *sql.DB) *infraMysql.Transactor {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
	return infraMysql.NewTransactor(db, logger)
}

func TestTransactor_DoInTx_正常系_fnがnilを返すとCOMMITされる(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectCommit()

	tr := newTransactor(t, db)
	err = tr.DoInTx(context.Background(), func(_ shared.Tx) error {
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTransactor_DoInTx_異常系_fnがerrorを返すとROLLBACKされる(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	fnErr := errors.New("business logic error")

	mock.ExpectBegin()
	mock.ExpectRollback()

	tr := newTransactor(t, db)
	err = tr.DoInTx(context.Background(), func(_ shared.Tx) error {
		return fnErr
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, fnErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTransactor_DoInTx_異常系_BeginTx失敗はラップされる(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	beginErr := errors.New("tcp connection refused")
	mock.ExpectBegin().WillReturnError(beginErr)

	tr := newTransactor(t, db)
	err = tr.DoInTx(context.Background(), func(_ shared.Tx) error {
		return nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, beginErr)
	assert.Contains(t, err.Error(), "failed to begin tx")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTransactor_DoInTx_異常系_Commit失敗はラップされる(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	commitErr := errors.New("commit timeout")
	mock.ExpectBegin()
	mock.ExpectCommit().WillReturnError(commitErr)

	tr := newTransactor(t, db)
	err = tr.DoInTx(context.Background(), func(_ shared.Tx) error {
		return nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, commitErr)
	assert.Contains(t, err.Error(), "failed to commit tx")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTransactor_DoInTx_異常系_panic時にROLLBACKして再panic(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectRollback()

	tr := newTransactor(t, db)
	assert.PanicsWithValue(t, "test panic", func() {
		_ = tr.DoInTx(context.Background(), func(_ shared.Tx) error {
			panic("test panic")
		})
	})
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTransactor_DoInTx_正常系_fnにSQLTxが渡される(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectCommit()

	tr := newTransactor(t, db)

	var receivedTx shared.Tx
	err = tr.DoInTx(context.Background(), func(tx shared.Tx) error {
		receivedTx = tx
		// IsTx() がパニックせずに呼べることを確認
		tx.IsTx()
		return nil
	})
	require.NoError(t, err)
	assert.NotNil(t, receivedTx)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTransactor_DoInTx_ROLLBACK_ErrTxDoneはloggerに出力されない(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	fnErr := errors.New("fn error")

	mock.ExpectBegin()
	// ROLLBACK を ErrTxDone で返す（すでにコミットされた扱い）
	mock.ExpectRollback().WillReturnError(sql.ErrTxDone)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	tr := infraMysql.NewTransactor(db, logger)

	err = tr.DoInTx(context.Background(), func(_ shared.Tx) error {
		return fnErr
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, fnErr)
	// ErrTxDone の場合は logger に出力されない
	assert.Empty(t, buf.String(), "ErrTxDone はログ出力されないはず")
	require.NoError(t, mock.ExpectationsWereMet())
}
