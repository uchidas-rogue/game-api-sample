package repository_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/repository"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
)

// foreignTx は shared.Tx を満たすが *infraMysql.SQLTx ではない実装。
// 多 DB 対応時に他 driver の Tx を MySQL repository に誤って渡したケースを模擬する。
type foreignTx struct{}

func (foreignTx) IsTx() {}

// TestQuerierFactory は newQuerierFactory の分岐を網羅する。
// 本番ファクトリは *SQLTx 以外の shared.Tx 実装を境界で拒否する責務を持つ
// （多 DB 対応時に MySQL repository に他 driver の Tx を誤って渡したバグを叩き落とす）。
func TestQuerierFactory(t *testing.T) {
	t.Parallel()

	// DBTX は factory の type assertion 分岐検証では参照されないため nil で良い。
	var db sqlc.DBTX

	t.Run("nil tx は base querier を返す", func(t *testing.T) {
		t.Parallel()
		factory := repository.NewQuerierFactoryForTest(db)

		q, err := factory(nil)
		require.NoError(t, err)
		assert.NotNil(t, q)
	})

	t.Run("*SQLTx 以外の Tx は unexpected tx type エラー", func(t *testing.T) {
		t.Parallel()
		factory := repository.NewQuerierFactoryForTest(db)

		q, err := factory(foreignTx{})
		require.Error(t, err)
		assert.Nil(t, q)
		assert.Contains(t, err.Error(), "unexpected tx type")
	})
}
