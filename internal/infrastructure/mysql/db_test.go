// Package mysql_test は Open / Ping の外部テスト。
package mysql_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
)

// TestOpen_lazy は Open が遅延接続で I/O を起こさず、
// 到達不能な DSN でも *sql.DB を返すことを確認する。
func TestOpen_lazy(t *testing.T) {
	t.Parallel()

	db, err := infraMysql.Open("game:game@tcp(127.0.0.1:1)/game_db")
	require.NoError(t, err)
	require.NotNil(t, db)
	t.Cleanup(func() { _ = db.Close() })
}

// TestConfigurePool は ConfigurePool がプール上限を *sql.DB に適用することを確認する。
// I/O は起こさず db.Stats() で反映を検証する。
func TestConfigurePool(t *testing.T) {
	t.Parallel()

	db, err := infraMysql.Open("game:game@tcp(127.0.0.1:1)/game_db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	infraMysql.ConfigurePool(db, infraMysql.PoolConfig{
		MaxOpenConns:    25,
		MaxIdleConns:    10,
		ConnMaxLifetime: 5 * time.Minute,
		ConnMaxIdleTime: time.Minute,
	})

	assert.Equal(t, 25, db.Stats().MaxOpenConnections)
}

// TestPing_unreachable は到達不能な DB に対し Ping が
// deadline 内に確定エラーを返すことを確認する（起動時 fail fast）。
func TestPing_unreachable(t *testing.T) {
	t.Parallel()

	db, err := infraMysql.Open("game:game@tcp(127.0.0.1:1)/game_db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err = infraMysql.Ping(ctx, db)
	elapsed := time.Since(start)

	assert.Error(t, err, "到達不能な DB への Ping はエラーを返すべき")
	assert.Less(t, elapsed, 5*time.Second, "Ping は有限時間内に確定すべき")
}
