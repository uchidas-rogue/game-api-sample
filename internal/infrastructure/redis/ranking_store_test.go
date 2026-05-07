// Package redis_test は RankingStore の外部テスト。
// miniredis を用いて実際の Redis Sorted Set 操作を検証する。
package redis_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
)

func TestRankingStore_IncrementGuildScore_スコア累積(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.IncrementGuildScore(ctx, 1, 100))
	require.NoError(t, store.IncrementGuildScore(ctx, 1, 200))

	score, found, err := store.GetGuildScore(ctx, 1)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(300), score)
}

func TestRankingStore_SetGuildScore_スコア上書き(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.SetGuildScore(ctx, 1, 500))
	require.NoError(t, store.SetGuildScore(ctx, 1, 200))

	score, found, err := store.GetGuildScore(ctx, 1)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(200), score)
}

func TestRankingStore_GetGuildScore_未登録メンバーはfoundFalse(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	score, found, err := store.GetGuildScore(ctx, 999)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, int64(0), score)
}

func TestRankingStore_GetGuildRank_ヒット(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.SetGuildScore(ctx, 1, 100))
	require.NoError(t, store.SetGuildScore(ctx, 2, 200))
	require.NoError(t, store.SetGuildScore(ctx, 3, 300))

	rank, err := store.GetGuildRank(ctx, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(1), rank)

	rank, err = store.GetGuildRank(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(3), rank)
}

func TestRankingStore_GetGuildRank_未登録メンバーは0を返す(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	rank, err := store.GetGuildRank(ctx, 999)
	require.NoError(t, err)
	assert.Equal(t, int64(0), rank)
}

func TestRankingStore_GetGuildRankings_複数件(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.SetGuildScore(ctx, 1, 100))
	require.NoError(t, store.SetGuildScore(ctx, 2, 300))
	require.NoError(t, store.SetGuildScore(ctx, 3, 200))

	entries, err := store.GetGuildRankings(ctx, 0, 3)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, int64(2), entries[0].ID)
	assert.Equal(t, int64(300), entries[0].Score)
	assert.Equal(t, int64(1), entries[0].Rank)
}

func TestRankingStore_GetGuildRankings_0件(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	entries, err := store.GetGuildRankings(ctx, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRankingStore_GetGuildTotalCount(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	count, err := store.GetGuildTotalCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	require.NoError(t, store.SetGuildScore(ctx, 1, 100))
	require.NoError(t, store.SetGuildScore(ctx, 2, 200))

	count, err = store.GetGuildTotalCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestRankingStore_IncrementUserPoints_ポイント累積(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.IncrementUserPoints(ctx, 10, 50))
	require.NoError(t, store.IncrementUserPoints(ctx, 10, 75))

	points, found, err := store.GetUserPoints(ctx, 10)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(125), points)
}

func TestRankingStore_SetUserPoints_ポイント上書き(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.SetUserPoints(ctx, 10, 1000))
	require.NoError(t, store.SetUserPoints(ctx, 10, 500))

	points, found, err := store.GetUserPoints(ctx, 10)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(500), points)
}

func TestRankingStore_GetUserPoints_未登録メンバーはfoundFalse(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	points, found, err := store.GetUserPoints(ctx, 9999)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, int64(0), points)
}

func TestRankingStore_GetUserRank_ヒット(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.SetUserPoints(ctx, 1, 100))
	require.NoError(t, store.SetUserPoints(ctx, 2, 300))
	require.NoError(t, store.SetUserPoints(ctx, 3, 200))

	rank, err := store.GetUserRank(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), rank)
}

func TestRankingStore_GetUserRank_未登録メンバーは0を返す(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	rank, err := store.GetUserRank(ctx, 9999)
	require.NoError(t, err)
	assert.Equal(t, int64(0), rank)
}

func TestRankingStore_GetUserRankings_複数件(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.SetUserPoints(ctx, 1, 100))
	require.NoError(t, store.SetUserPoints(ctx, 2, 300))
	require.NoError(t, store.SetUserPoints(ctx, 3, 200))

	entries, err := store.GetUserRankings(ctx, 0, 3)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, int64(2), entries[0].ID)
	assert.Equal(t, int64(300), entries[0].Score)
	assert.Equal(t, int64(1), entries[0].Rank)
}

func TestRankingStore_GetUserRankings_0件(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	entries, err := store.GetUserRankings(ctx, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRankingStore_GetUserTotalCount(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	count, err := store.GetUserTotalCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	require.NoError(t, store.SetUserPoints(ctx, 1, 100))
	require.NoError(t, store.SetUserPoints(ctx, 2, 200))

	count, err = store.GetUserTotalCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestRankingStore_IncrementGuildScore_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	err := store.IncrementGuildScore(ctx, 1, 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zincrby guild score failed")
}

func TestRankingStore_SetGuildScore_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	err := store.SetGuildScore(ctx, 1, 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zadd guild score failed")
}

func TestRankingStore_GetGuildTotalCount_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	_, err := store.GetGuildTotalCount(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zcard guild failed")
}

func TestRankingStore_IncrementUserPoints_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	err := store.IncrementUserPoints(ctx, 1, 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zincrby user points failed")
}

func TestRankingStore_SetUserPoints_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	err := store.SetUserPoints(ctx, 1, 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zadd user points failed")
}

func TestRankingStore_GetUserTotalCount_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	_, err := store.GetUserTotalCount(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zcard user failed")
}

func TestRankingStore_GetGuildRank_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	_, err := store.GetGuildRank(ctx, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zrevrank guild failed")
}

func TestRankingStore_GetGuildScore_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	_, _, err := store.GetGuildScore(ctx, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zscore guild failed")
}

func TestRankingStore_GetUserRank_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	_, err := store.GetUserRank(ctx, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zrevrank user failed")
}

func TestRankingStore_GetUserPoints_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	_, _, err := store.GetUserPoints(ctx, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zscore user failed")
}

func TestRankingStore_GetGuildRankings_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	_, err := store.GetGuildRankings(ctx, 0, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zrevrangewithscores guild failed")
}

func TestRankingStore_GetUserRankings_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	_, err := store.GetUserRankings(ctx, 0, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zrevrangewithscores user failed")
}

func TestRankingStore_GetGuildRankings_ParseInt失敗時はスキップされる(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	// miniredis に不正な member を直接 zadd
	s.ZAdd(rankingdomain.GuildRankingKey, 100, "not-a-number")
	s.ZAdd(rankingdomain.GuildRankingKey, 200, "2")

	entries, err := store.GetGuildRankings(ctx, 0, 10)
	require.NoError(t, err)
	// not-a-number は ParseInt 失敗でスキップされ、有効な "2" のみ返る
	assert.Len(t, entries, 1)
	assert.Equal(t, int64(2), entries[0].ID)
}

func TestRankingStore_GetUserRankings_ParseInt失敗時はスキップされる(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.ZAdd(rankingdomain.UserRankingKey, 100, "invalid")
	s.ZAdd(rankingdomain.UserRankingKey, 200, "5")

	entries, err := store.GetUserRankings(ctx, 0, 10)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, int64(5), entries[0].ID)
}

func TestRankingStore_GuildKeyとUserKeyの分離(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	// ギルドと同じ ID でユーザーを登録しても別キーに書き込まれる
	require.NoError(t, store.SetGuildScore(ctx, 1, 1000))
	require.NoError(t, store.SetUserPoints(ctx, 1, 500))

	gScore, gFound, err := store.GetGuildScore(ctx, 1)
	require.NoError(t, err)
	assert.True(t, gFound)
	assert.Equal(t, int64(1000), gScore)

	uPoints, uFound, err := store.GetUserPoints(ctx, 1)
	require.NoError(t, err)
	assert.True(t, uFound)
	assert.Equal(t, int64(500), uPoints)

	// miniredis で直接キーの存在確認
	assert.True(t, s.Exists(rankingdomain.GuildRankingKey))
	assert.True(t, s.Exists(rankingdomain.UserRankingKey))

	gCount, err := store.GetGuildTotalCount(ctx)
	require.NoError(t, err)
	uCount, err := store.GetUserTotalCount(ctx)
	require.NoError(t, err)
	// 各キーに1件ずつ
	assert.Equal(t, int64(1), gCount)
	assert.Equal(t, int64(1), uCount)
}
