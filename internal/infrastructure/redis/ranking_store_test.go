// Package redis_test は RankingStore の外部テスト。
// miniredis を用いて実際の Redis Sorted Set 操作を検証する。
//
// guild 版 / user 版はメソッド名こそ非対称（Score/Points）だが手続きは同一なので、
// internal/usecase/ranking/usecase_test.go の rankingKind と同じ考え方で
// 1 本のテーブル + ランナーに畳んでいる。「どのメソッドを呼ぶか」だけを
// storeOps のディスパッチ表（関数値）に持たせ、組み立て・アサーションは
// ランナー側に1本だけ書く。
package redis_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	infraRedis "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/redis"
)

// rankingKind はギルド版・ユーザー版の区別。
type rankingKind int

const (
	kindGuild rankingKind = iota
	kindUser
)

func (k rankingKind) String() string {
	if k == kindGuild {
		return "guild"
	}
	return "user"
}

// storeOps は kind ごとに呼び分ける RankingStore のメソッドの束。
// メソッド名の非対称（SetGuildScore/SetUserPoints 等）をここで吸収し、
// テストケース側は kind だけを持てばよくする。
type storeOps struct {
	key         string
	setScore    func(store *infraRedis.RankingStore, ctx context.Context, id, score int64) error
	getScore    func(store *infraRedis.RankingStore, ctx context.Context, id int64) (int64, bool, error)
	getRank     func(store *infraRedis.RankingStore, ctx context.Context, id int64) (int64, error)
	getRankings func(store *infraRedis.RankingStore, ctx context.Context, offset, limit int) ([]rankingdomain.RankEntry, error)
	getTotal    func(store *infraRedis.RankingStore, ctx context.Context) (int64, error)
}

func opsFor(kind rankingKind) storeOps {
	if kind == kindGuild {
		return storeOps{
			key: rankingdomain.GuildRankingKey,
			setScore: func(store *infraRedis.RankingStore, ctx context.Context, id, score int64) error {
				return store.SetGuildScore(ctx, id, score)
			},
			getScore: func(store *infraRedis.RankingStore, ctx context.Context, id int64) (int64, bool, error) {
				return store.GetGuildScore(ctx, id)
			},
			getRank: func(store *infraRedis.RankingStore, ctx context.Context, id int64) (int64, error) {
				return store.GetGuildRank(ctx, id)
			},
			getRankings: func(store *infraRedis.RankingStore, ctx context.Context, offset, limit int) ([]rankingdomain.RankEntry, error) {
				return store.GetGuildRankings(ctx, offset, limit)
			},
			getTotal: func(store *infraRedis.RankingStore, ctx context.Context) (int64, error) {
				return store.GetGuildTotalCount(ctx)
			},
		}
	}
	return storeOps{
		key: rankingdomain.UserRankingKey,
		setScore: func(store *infraRedis.RankingStore, ctx context.Context, id, score int64) error {
			return store.SetUserPoints(ctx, id, score)
		},
		getScore: func(store *infraRedis.RankingStore, ctx context.Context, id int64) (int64, bool, error) {
			return store.GetUserPoints(ctx, id)
		},
		getRank: func(store *infraRedis.RankingStore, ctx context.Context, id int64) (int64, error) {
			return store.GetUserRank(ctx, id)
		},
		getRankings: func(store *infraRedis.RankingStore, ctx context.Context, offset, limit int) ([]rankingdomain.RankEntry, error) {
			return store.GetUserRankings(ctx, offset, limit)
		},
		getTotal: func(store *infraRedis.RankingStore, ctx context.Context) (int64, error) {
			return store.GetUserTotalCount(ctx)
		},
	}
}

// forEachKind は guild/user 双方のケースを t.Run(kind.String(), ...) で流す。
func forEachKind(t *testing.T, fn func(t *testing.T, ops storeOps)) {
	t.Helper()
	for _, kind := range []rankingKind{kindGuild, kindUser} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			fn(t, opsFor(kind))
		})
	}
}

func TestRankingStore_SetScore_上書き(t *testing.T) {
	t.Parallel()

	forEachKind(t, func(t *testing.T, ops storeOps) {
		store, _ := newTestStore(t)
		ctx := context.Background()

		require.NoError(t, ops.setScore(store, ctx, 1, 500))
		require.NoError(t, ops.setScore(store, ctx, 1, 200))

		score, found, err := ops.getScore(store, ctx, 1)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int64(200), score)
	})
}

func TestRankingStore_GetScore_未登録メンバーはfoundFalse(t *testing.T) {
	t.Parallel()

	forEachKind(t, func(t *testing.T, ops storeOps) {
		store, _ := newTestStore(t)
		ctx := context.Background()

		score, found, err := ops.getScore(store, ctx, 999)
		require.NoError(t, err)
		assert.False(t, found)
		assert.Equal(t, int64(0), score)
	})
}

func TestRankingStore_GetRank_ヒット(t *testing.T) {
	t.Parallel()

	forEachKind(t, func(t *testing.T, ops storeOps) {
		store, _ := newTestStore(t)
		ctx := context.Background()

		require.NoError(t, ops.setScore(store, ctx, 1, 100))
		require.NoError(t, ops.setScore(store, ctx, 2, 200))
		require.NoError(t, ops.setScore(store, ctx, 3, 300))

		rank, err := ops.getRank(store, ctx, 3)
		require.NoError(t, err)
		assert.Equal(t, int64(1), rank)

		rank, err = ops.getRank(store, ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, int64(3), rank)
	})
}

func TestRankingStore_GetRank_未登録メンバーは0を返す(t *testing.T) {
	t.Parallel()

	forEachKind(t, func(t *testing.T, ops storeOps) {
		store, _ := newTestStore(t)
		ctx := context.Background()

		rank, err := ops.getRank(store, ctx, 999)
		require.NoError(t, err)
		assert.Equal(t, int64(0), rank)
	})
}

func TestRankingStore_GetRankings_複数件(t *testing.T) {
	t.Parallel()

	forEachKind(t, func(t *testing.T, ops storeOps) {
		store, _ := newTestStore(t)
		ctx := context.Background()

		require.NoError(t, ops.setScore(store, ctx, 1, 100))
		require.NoError(t, ops.setScore(store, ctx, 2, 300))
		require.NoError(t, ops.setScore(store, ctx, 3, 200))

		entries, err := ops.getRankings(store, ctx, 0, 3)
		require.NoError(t, err)
		require.Len(t, entries, 3)
		assert.Equal(t, int64(2), entries[0].ID)
		assert.Equal(t, int64(300), entries[0].Score)
		assert.Equal(t, int64(1), entries[0].Rank)
	})
}

// TestRankingStore_GetRankings_offsetは順位採番に反映される は、2 ページ目以降で
// 順位が 1 から振り直されないことを検証する。
//
// Rank は ZREVRANGE が返す添字ではなく offset を加えた通し順位でなければならない。
// offset=0 のケースだけだと `offset + i + 1` から offset を落とす実装ミスが
// 検出できず、ページングした瞬間に全ページの先頭が 1 位になる。
func TestRankingStore_GetRankings_offsetは順位採番に反映される(t *testing.T) {
	t.Parallel()

	forEachKind(t, func(t *testing.T, ops storeOps) {
		store, _ := newTestStore(t)
		ctx := context.Background()

		require.NoError(t, ops.setScore(store, ctx, 1, 100))
		require.NoError(t, ops.setScore(store, ctx, 2, 300))
		require.NoError(t, ops.setScore(store, ctx, 3, 200))

		// スコア降順は 2(300) → 3(200) → 1(100)。offset=1 なので 2 ページ目にあたる。
		entries, err := ops.getRankings(store, ctx, 1, 2)
		require.NoError(t, err)
		require.Len(t, entries, 2)

		assert.Equal(t, int64(3), entries[0].ID)
		assert.Equal(t, int64(2), entries[0].Rank, "offset 分を加えた通し順位であること")
		assert.Equal(t, int64(1), entries[1].ID)
		assert.Equal(t, int64(3), entries[1].Rank)
	})
}

func TestRankingStore_GetRankings_0件(t *testing.T) {
	t.Parallel()

	forEachKind(t, func(t *testing.T, ops storeOps) {
		store, _ := newTestStore(t)
		ctx := context.Background()

		entries, err := ops.getRankings(store, ctx, 0, 10)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestRankingStore_GetRankings_ParseInt失敗時はスキップされる(t *testing.T) {
	t.Parallel()

	forEachKind(t, func(t *testing.T, ops storeOps) {
		store, s := newTestStore(t)
		ctx := context.Background()

		// miniredis に不正な member を直接 zadd
		s.ZAdd(ops.key, 100, "not-a-number")
		s.ZAdd(ops.key, 200, "2")

		entries, err := ops.getRankings(store, ctx, 0, 10)
		require.NoError(t, err)
		// not-a-number は ParseInt 失敗でスキップされ、有効な "2" のみ返る
		assert.Len(t, entries, 1)
		assert.Equal(t, int64(2), entries[0].ID)
	})
}

func TestRankingStore_GetTotalCount(t *testing.T) {
	t.Parallel()

	forEachKind(t, func(t *testing.T, ops storeOps) {
		store, _ := newTestStore(t)
		ctx := context.Background()

		count, err := ops.getTotal(store, ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), count)

		require.NoError(t, ops.setScore(store, ctx, 1, 100))
		require.NoError(t, ops.setScore(store, ctx, 2, 200))

		count, err = ops.getTotal(store, ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})
}

// TestRankingStore_ApplyScoreDeltas_ユーザーとギルド双方が1往復で反映される は
// outbox-worker のバッチ適用が使うパイプライン反映を検証する。
// 既存値がある場合は加算（ZINCRBY）になることも確認する。
//
// ApplyScoreDeltas は users/guilds を同時に扱う操作であり guild 版/user 版に
// 分解できないため、上の storeOps 対称化の対象外として個別に残す。
func TestRankingStore_ApplyScoreDeltas_ユーザーとギルド双方が反映される(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	// 既存値を置いておき、上書きではなく加算されることを見る。
	require.NoError(t, store.SetUserPoints(ctx, 10, 1000))
	require.NoError(t, store.SetGuildScore(ctx, 1, 5000))

	require.NoError(t, store.ApplyScoreDeltas(ctx,
		[]rankingdomain.UserPointDelta{
			{UserID: 10, Points: 150},
			{UserID: 11, Points: 200},
		},
		[]rankingdomain.GuildScoreDelta{
			{GuildID: 1, Points: 300},
			{GuildID: 2, Points: 50},
		},
	))

	points, found, err := store.GetUserPoints(ctx, 10)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(1150), points)

	points, found, err = store.GetUserPoints(ctx, 11)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(200), points)

	score, found, err := store.GetGuildScore(ctx, 1)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(5300), score)

	score, found, err = store.GetGuildScore(ctx, 2)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(50), score)
}

// TestRankingStore_ApplyScoreDeltas_片方のみでも反映される は users / guilds の
// 一方が空のケースを確認する（もう一方のキーは作られない）。
func TestRankingStore_ApplyScoreDeltas_片方のみでも反映される(t *testing.T) {
	t.Parallel()

	t.Run("users のみ", func(t *testing.T) {
		t.Parallel()

		store, s := newTestStore(t)
		ctx := context.Background()

		require.NoError(t, store.ApplyScoreDeltas(ctx,
			[]rankingdomain.UserPointDelta{{UserID: 1, Points: 10}}, nil))

		points, found, err := store.GetUserPoints(ctx, 1)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int64(10), points)
		assert.False(t, s.Exists(rankingdomain.GuildRankingKey))
	})

	t.Run("guilds のみ", func(t *testing.T) {
		t.Parallel()

		store, s := newTestStore(t)
		ctx := context.Background()

		require.NoError(t, store.ApplyScoreDeltas(ctx, nil,
			[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 10}}))

		score, found, err := store.GetGuildScore(ctx, 1)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, int64(10), score)
		assert.False(t, s.Exists(rankingdomain.UserRankingKey))
	})
}

// TestRankingStore_ApplyScoreDeltas_両方空ならコマンドを発行しない は、
// 空バッチで無駄な Redis 往復を発生させないことを確認する。
// miniredis に強制エラーを仕込んでおき、それでも nil が返ること
// （= コマンドが1つも発行されていないこと）で検証する。
func TestRankingStore_ApplyScoreDeltas_両方空ならコマンドを発行しない(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")

	require.NoError(t, store.ApplyScoreDeltas(ctx, nil, nil))
	require.NoError(t, store.ApplyScoreDeltas(ctx,
		[]rankingdomain.UserPointDelta{}, []rankingdomain.GuildScoreDelta{}))
}

func TestRankingStore_ApplyScoreDeltas_エラー伝播(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	s.SetError("ERR forced error")
	err := store.ApplyScoreDeltas(ctx,
		[]rankingdomain.UserPointDelta{{UserID: 1, Points: 10}},
		[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 10}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "pipeline zincrby failed")
	// エラーメッセージに件数が含まれ、障害調査時にバッチ規模が分かる。
	assert.Contains(t, err.Error(), "users=1, guilds=1")
}

// ---------------------------------------------------------------------------
// センチネルキー（揮発検知）
// 設計の意図は docs/testing/ranking.md §0。
// ---------------------------------------------------------------------------

func TestRankingStore_IsInitialized_マーク前はfalse(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	ok, err := store.IsInitialized(context.Background())
	require.NoError(t, err)
	assert.False(t, ok, "マークしていなければ未初期化")
}

func TestRankingStore_MarkInitialized_マーク後はtrue(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.MarkInitialized(ctx))

	ok, err := store.IsInitialized(ctx)
	require.NoError(t, err)
	assert.True(t, ok)
}

// センチネルキーに TTL を付けると、揮発していないのに期限切れで 503 を返す偽陽性になる。
// 「TTL が無いこと」は実装を見ないと分からない不変条件なのでテストで固定する。
func TestRankingStore_MarkInitialized_TTLを付けない(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)

	require.NoError(t, store.MarkInitialized(context.Background()))

	require.True(t, s.Exists(rankingdomain.RankingInitializedKey))
	assert.Zero(t, s.TTL(rankingdomain.RankingInitializedKey), "センチネルキーに TTL を付けてはならない")
}

// 揮発（Redis のデータが丸ごと消える）を FlushAll で模す。
// ZSet と一緒にセンチネルキーも消えることが、この方式が偽陰性を出さない根拠。
func TestRankingStore_IsInitialized_揮発後はfalse(t *testing.T) {
	t.Parallel()

	store, s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.SetUserPoints(ctx, 1, 100))
	require.NoError(t, store.MarkInitialized(ctx))

	s.FlushAll()

	ok, err := store.IsInitialized(ctx)
	require.NoError(t, err)
	assert.False(t, ok, "揮発したらセンチネルキーも消える")
}

// TestRankingStore_エラー伝播 は各操作が Redis コマンド失敗をどうラップするかを検証する。
// (操作名, 操作を呼ぶ関数, 期待するエラーメッセージ断片) のテーブル。
// 期待メッセージは操作ごとに異なる固有の文字列であり、これが唯一の取り違え検出手段
// なので、共通化・Any 化せず1件ずつ厳密に保持する。
//
// ここは guild/user の対称化が目的ではなく「操作ごとに固有のメッセージが付くこと」を
// 見る場所なので、storeOps を経由せず対象メソッドを直接呼ぶ。
// 経由すると name とディスパッチ先が乖離しうるうえ、読み手が opsFor まで辿る必要が出る。
func TestRankingStore_エラー伝播(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		call    func(store *infraRedis.RankingStore, ctx context.Context) error
		wantMsg string
	}{
		{
			name: "SetGuildScore",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				return store.SetGuildScore(ctx, 1, 100)
			},
			wantMsg: "zadd guild score failed",
		},
		{
			name: "GetGuildTotalCount",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				_, err := store.GetGuildTotalCount(ctx)
				return err
			},
			wantMsg: "zcard guild failed",
		},
		{
			name: "IsInitialized",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				_, err := store.IsInitialized(ctx)
				return err
			},
			wantMsg: "exists ranking initialized failed",
		},
		{
			name: "MarkInitialized",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				return store.MarkInitialized(ctx)
			},
			wantMsg: "set ranking initialized failed",
		},
		{
			name: "SetUserPoints",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				return store.SetUserPoints(ctx, 1, 100)
			},
			wantMsg: "zadd user points failed",
		},
		{
			name: "GetUserTotalCount",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				_, err := store.GetUserTotalCount(ctx)
				return err
			},
			wantMsg: "zcard user failed",
		},
		{
			name: "GetGuildRank",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				_, err := store.GetGuildRank(ctx, 1)
				return err
			},
			wantMsg: "zrevrank guild failed",
		},
		{
			name: "GetGuildScore",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				_, _, err := store.GetGuildScore(ctx, 1)
				return err
			},
			wantMsg: "zscore guild failed",
		},
		{
			name: "GetUserRank",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				_, err := store.GetUserRank(ctx, 1)
				return err
			},
			wantMsg: "zrevrank user failed",
		},
		{
			name: "GetUserPoints",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				_, _, err := store.GetUserPoints(ctx, 1)
				return err
			},
			wantMsg: "zscore user failed",
		},
		{
			name: "GetGuildRankings",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				_, err := store.GetGuildRankings(ctx, 0, 10)
				return err
			},
			wantMsg: "zrevrangewithscores guild failed",
		},
		{
			name: "GetUserRankings",
			call: func(store *infraRedis.RankingStore, ctx context.Context) error {
				_, err := store.GetUserRankings(ctx, 0, 10)
				return err
			},
			wantMsg: "zrevrangewithscores user failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store, s := newTestStore(t)
			ctx := context.Background()
			s.SetError("ERR forced error")

			err := tt.call(store, ctx)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
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
