package redis

import (
	"context"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
)

// RankingStore は Redis Sorted Set を使用したランキングストア。
var _ rankingusecase.RankingStore = (*RankingStore)(nil)

type RankingStore struct {
	client *redis.Client
}

// NewRankingStore は RankingStore を生成する。
func NewRankingStore(client *redis.Client) *RankingStore {
	return &RankingStore{client: client}
}

// IncrementGuildScore はギルドのスコアを加算する。
func (r *RankingStore) IncrementGuildScore(ctx context.Context, guildID int64, score int64) error {
	member := strconv.FormatInt(guildID, 10)
	if err := r.client.ZIncrBy(ctx, rankingdomain.GuildRankingKey, float64(score), member).Err(); err != nil {
		return fmt.Errorf("zincrby guild score failed: %w", err)
	}
	return nil
}

// SetGuildScore はギルドのスコアを上書きする（バッチ同期用）。
func (r *RankingStore) SetGuildScore(ctx context.Context, guildID int64, score int64) error {
	member := strconv.FormatInt(guildID, 10)
	if err := r.client.ZAdd(ctx, rankingdomain.GuildRankingKey, redis.Z{
		Score:  float64(score),
		Member: member,
	}).Err(); err != nil {
		return fmt.Errorf("zadd guild score failed: %w", err)
	}
	return nil
}

// GetGuildRank はギルドの順位を取得する（1-indexed）。
func (r *RankingStore) GetGuildRank(ctx context.Context, guildID int64) (int64, error) {
	member := strconv.FormatInt(guildID, 10)
	rank, err := r.client.ZRevRank(ctx, rankingdomain.GuildRankingKey, member).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("zrevrank guild failed: %w", err)
	}
	return rank + 1, nil
}

// GetGuildScore はギルドのスコアを取得する。
func (r *RankingStore) GetGuildScore(ctx context.Context, guildID int64) (int64, bool, error) {
	member := strconv.FormatInt(guildID, 10)
	score, err := r.client.ZScore(ctx, rankingdomain.GuildRankingKey, member).Result()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("zscore guild failed: %w", err)
	}
	return int64(score), true, nil
}

// GetGuildRankings は上位ギルドランキングを取得する。
func (r *RankingStore) GetGuildRankings(ctx context.Context, offset, limit int) ([]rankingdomain.RankEntry, error) {
	start := int64(offset)
	stop := int64(offset + limit - 1)

	results, err := r.client.ZRevRangeWithScores(ctx, rankingdomain.GuildRankingKey, start, stop).Result()
	if err != nil {
		return nil, fmt.Errorf("zrevrangewithscores guild failed: %w", err)
	}

	entries := make([]rankingdomain.RankEntry, 0, len(results))
	for i, z := range results {
		id, err := strconv.ParseInt(z.Member.(string), 10, 64)
		if err != nil {
			continue
		}
		entries = append(entries, rankingdomain.RankEntry{
			Rank:  int64(offset + i + 1),
			ID:    id,
			Score: int64(z.Score),
		})
	}
	return entries, nil
}

// GetGuildTotalCount はランキング登録済みのギルド総数を取得する。
func (r *RankingStore) GetGuildTotalCount(ctx context.Context) (int64, error) {
	count, err := r.client.ZCard(ctx, rankingdomain.GuildRankingKey).Result()
	if err != nil {
		return 0, fmt.Errorf("zcard guild failed: %w", err)
	}
	return count, nil
}

// IncrementUserPoints はユーザーのポイントを加算する。
func (r *RankingStore) IncrementUserPoints(ctx context.Context, userID int64, points int64) error {
	member := strconv.FormatInt(userID, 10)
	if err := r.client.ZIncrBy(ctx, rankingdomain.UserRankingKey, float64(points), member).Err(); err != nil {
		return fmt.Errorf("zincrby user points failed: %w", err)
	}
	return nil
}

// ApplyScoreDeltas はユーザー・ギルド双方の加算をパイプラインで1往復にまとめて反映する。
// outbox-worker がバッチ単位トランザクション内から呼ぶため、往復回数がそのまま
// MySQL 側のロック保持時間になる。件数ぶん往復すると加算件数に比例して
// トランザクションが長くなるため、必ず1往復に集約する。
//
// パイプラインは各コマンドを個別に評価するため（MULTI/EXEC と違い原子的ではない）、
// 途中失敗時は一部だけ適用されうる。Redis は元々 at-least-once 前提のキャッシュであり、
// ずれは RankingSyncer による再構築で回復する。
func (r *RankingStore) ApplyScoreDeltas(
	ctx context.Context,
	users []rankingdomain.UserPointDelta,
	guilds []rankingdomain.GuildScoreDelta,
) error {
	if len(users) == 0 && len(guilds) == 0 {
		return nil
	}

	pipe := r.client.Pipeline()
	for _, u := range users {
		pipe.ZIncrBy(ctx, rankingdomain.UserRankingKey, float64(u.Points), strconv.FormatInt(u.UserID, 10))
	}
	for _, g := range guilds {
		pipe.ZIncrBy(ctx, rankingdomain.GuildRankingKey, float64(g.Points), strconv.FormatInt(g.GuildID, 10))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("pipeline zincrby failed (users=%d, guilds=%d): %w", len(users), len(guilds), err)
	}
	return nil
}

// SetUserPoints はユーザーのポイントを上書きする（バッチ同期用）。
func (r *RankingStore) SetUserPoints(ctx context.Context, userID int64, points int64) error {
	member := strconv.FormatInt(userID, 10)
	if err := r.client.ZAdd(ctx, rankingdomain.UserRankingKey, redis.Z{
		Score:  float64(points),
		Member: member,
	}).Err(); err != nil {
		return fmt.Errorf("zadd user points failed: %w", err)
	}
	return nil
}

// GetUserRank はユーザーの順位を取得する（1-indexed）。
func (r *RankingStore) GetUserRank(ctx context.Context, userID int64) (int64, error) {
	member := strconv.FormatInt(userID, 10)
	rank, err := r.client.ZRevRank(ctx, rankingdomain.UserRankingKey, member).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("zrevrank user failed: %w", err)
	}
	return rank + 1, nil
}

// GetUserPoints はユーザーのポイントを取得する。
func (r *RankingStore) GetUserPoints(ctx context.Context, userID int64) (int64, bool, error) {
	member := strconv.FormatInt(userID, 10)
	score, err := r.client.ZScore(ctx, rankingdomain.UserRankingKey, member).Result()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("zscore user failed: %w", err)
	}
	return int64(score), true, nil
}

// GetUserRankings は上位ユーザーランキングを取得する。
func (r *RankingStore) GetUserRankings(ctx context.Context, offset, limit int) ([]rankingdomain.RankEntry, error) {
	start := int64(offset)
	stop := int64(offset + limit - 1)

	results, err := r.client.ZRevRangeWithScores(ctx, rankingdomain.UserRankingKey, start, stop).Result()
	if err != nil {
		return nil, fmt.Errorf("zrevrangewithscores user failed: %w", err)
	}

	entries := make([]rankingdomain.RankEntry, 0, len(results))
	for i, z := range results {
		id, err := strconv.ParseInt(z.Member.(string), 10, 64)
		if err != nil {
			continue
		}
		entries = append(entries, rankingdomain.RankEntry{
			Rank:  int64(offset + i + 1),
			ID:    id,
			Score: int64(z.Score),
		})
	}
	return entries, nil
}

// GetUserTotalCount はランキング登録済みのユーザー総数を取得する。
func (r *RankingStore) GetUserTotalCount(ctx context.Context) (int64, error) {
	count, err := r.client.ZCard(ctx, rankingdomain.UserRankingKey).Result()
	if err != nil {
		return 0, fmt.Errorf("zcard user failed: %w", err)
	}
	return count, nil
}
