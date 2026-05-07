// Package redis_test は Redis インフラ実装の外部テスト。
package redis_test

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	infraRedis "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/redis"
)

// newTestStore は miniredis を起動し、RankingStore と miniredis サーバを返す。
// t.Cleanup により miniredis はテスト終了時に自動停止される。
func newTestStore(t *testing.T) (*infraRedis.RankingStore, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return infraRedis.NewRankingStore(client), s
}
