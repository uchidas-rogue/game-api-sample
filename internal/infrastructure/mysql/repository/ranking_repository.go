package repository

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

var _ rankingusecase.Repository = (*RankingRepository)(nil)

// RankingRepository は rankingusecase.Repository の sqlc/MySQL 実装。
type RankingRepository struct {
	querier querierFactory
	execer  execerFactory
}

// NewRankingRepository は RankingRepository を生成する。
func NewRankingRepository(db sqlc.DBTX) *RankingRepository {
	return &RankingRepository{
		querier: newQuerierFactory(db),
		execer:  newExecerFactory(db),
	}
}

// GetGuild はギルドを取得する。存在しない場合は ErrGuildNotFound へ変換する。
func (r *RankingRepository) GetGuild(ctx context.Context, tx shared.Tx, guildID int64) (rankingdomain.Guild, error) {
	q, err := r.querier(tx)
	if err != nil {
		return rankingdomain.Guild{}, fmt.Errorf("GetGuild: %w", err)
	}
	g, err := q.GetGuild(ctx, guildID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rankingdomain.Guild{}, fmt.Errorf("guild %d: %w", guildID, rankingdomain.ErrGuildNotFound)
		}
		return rankingdomain.Guild{}, fmt.Errorf("get guild: %w", err)
	}
	return rankingdomain.Guild{
		ID:        g.ID,
		Name:      g.Name,
		CreatedAt: g.CreatedAt.Time,
		UpdatedAt: g.UpdatedAt.Time,
	}, nil
}

// GetUser はユーザー名を取得する。存在しない場合は ErrUserNotFound へ変換する。
func (r *RankingRepository) GetUser(ctx context.Context, tx shared.Tx, userID int64) (string, error) {
	q, err := r.querier(tx)
	if err != nil {
		return "", fmt.Errorf("GetUser: %w", err)
	}
	u, err := q.GetUser(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("user %d: %w", userID, rankingdomain.ErrUserNotFound)
		}
		return "", fmt.Errorf("get user: %w", err)
	}
	return u.Name, nil
}

// IncrementGuildScore はギルドスコアを加算する（行が無ければ作成する upsert）。
func (r *RankingRepository) IncrementGuildScore(ctx context.Context, tx shared.Tx, guildID int64, score int64) error {
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("IncrementGuildScore: %w", err)
	}
	if err := q.IncrementGuildScore(ctx, sqlc.IncrementGuildScoreParams{
		GuildID: guildID,
		Score:   score,
	}); err != nil {
		return fmt.Errorf("increment guild score: %w", err)
	}
	return nil
}

// 複数行 INSERT の1行あたりのプレースホルダ数。args のキャパシティ計算に使う。
const (
	// guildScoreColumns は guild_scores の (guild_id, score)。
	guildScoreColumns = 2
	// guildScoreHistoryColumns は guild_score_histories の (guild_id, user_id, score)。
	guildScoreHistoryColumns = 3
)

// BulkIncrementGuildScores は複数ギルドのスコアを1文の複数行 upsert で一括加算する。
// sqlc は可変行数の複数行 INSERT を生成できないため、プレースホルダを組み立てた
// パラメータ化生 SQL を execer 経由で発行する（値の文字列連結はしない）。
//
// deltas は GuildID 昇順にソートしてから発行する。並行するバッチトランザクションが
// 同じ順序で guild_scores の行ロックを取得するようにし、デッドロックを防ぐため。
// この順序保証は必須要件であり、外すと同時実行時にデッドロックが発生しうる。
func (r *RankingRepository) BulkIncrementGuildScores(ctx context.Context, tx shared.Tx, deltas []rankingdomain.GuildScoreDelta) error {
	if len(deltas) == 0 {
		return nil
	}
	ex, err := r.execer(tx)
	if err != nil {
		return fmt.Errorf("BulkIncrementGuildScores: %w", err)
	}

	// 呼び出し元のスライスを書き換えないようコピーしてからソートする。
	sorted := slices.Clone(deltas)
	slices.SortFunc(sorted, func(a, b rankingdomain.GuildScoreDelta) int {
		return cmp.Compare(a.GuildID, b.GuildID)
	})

	valuesClause := strings.TrimSuffix(strings.Repeat("(?, ?),", len(sorted)), ",")
	query := "INSERT INTO guild_scores (guild_id, score) VALUES " + valuesClause +
		" ON DUPLICATE KEY UPDATE score = score + VALUES(score)"
	args := make([]any, 0, len(sorted)*guildScoreColumns)
	for _, d := range sorted {
		args = append(args, d.GuildID, d.Points)
	}
	if _, err := ex.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("failed to bulk increment guild scores (count=%d): %w", len(sorted), err)
	}
	return nil
}

// BulkInsertGuildScoreHistories はスコア履歴を1文の複数行 INSERT で一括記録する。
// スコア加算はギルド単位に集約できるが、履歴は誰がいつ何点入れたかを残す必要があるため
// イベント単位の行をそのまま挿入する。
func (r *RankingRepository) BulkInsertGuildScoreHistories(ctx context.Context, tx shared.Tx, entries []rankingdomain.GuildScoreHistoryEntry) error {
	if len(entries) == 0 {
		return nil
	}
	ex, err := r.execer(tx)
	if err != nil {
		return fmt.Errorf("BulkInsertGuildScoreHistories: %w", err)
	}

	valuesClause := strings.TrimSuffix(strings.Repeat("(?, ?, ?),", len(entries)), ",")
	query := "INSERT INTO guild_score_histories (guild_id, user_id, score) VALUES " + valuesClause
	args := make([]any, 0, len(entries)*guildScoreHistoryColumns)
	for _, e := range entries {
		args = append(args, e.GuildID, e.UserID, e.Points)
	}
	if _, err := ex.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("failed to bulk insert guild score histories (count=%d): %w", len(entries), err)
	}
	return nil
}

// InsertGuildScoreHistory はギルドスコアの加算履歴を1件記録する。
func (r *RankingRepository) InsertGuildScoreHistory(ctx context.Context, tx shared.Tx, guildID int64, userID int64, score int64) error {
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("InsertGuildScoreHistory: %w", err)
	}
	if err := q.InsertGuildScoreHistory(ctx, sqlc.InsertGuildScoreHistoryParams{
		GuildID: guildID,
		UserID:  userID,
		Score:   score,
	}); err != nil {
		return fmt.Errorf("insert guild score history: %w", err)
	}
	return nil
}

// GetUserGuildID はユーザーの所属ギルド ID を取得する。
// 未所属（該当行なし）は ErrUserNotInGuild へ変換する。
func (r *RankingRepository) GetUserGuildID(ctx context.Context, tx shared.Tx, userID int64) (int64, error) {
	q, err := r.querier(tx)
	if err != nil {
		return 0, fmt.Errorf("GetUserGuildID: %w", err)
	}
	guildID, err := q.GetUserGuildID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("user %d: %w", userID, rankingdomain.ErrUserNotInGuild)
		}
		return 0, fmt.Errorf("get user guild id: %w", err)
	}
	return guildID, nil
}

// ListGuildsByIDs は指定 ID のギルドを一括取得し、ID をキーにしたマップで返す。
// 存在しない ID は結果に含まれない（呼び出し側が欠落を許容する前提）。
func (r *RankingRepository) ListGuildsByIDs(ctx context.Context, tx shared.Tx, guildIDs []int64) (map[int64]rankingdomain.Guild, error) {
	q, err := r.querier(tx)
	if err != nil {
		return nil, fmt.Errorf("ListGuildsByIDs: %w", err)
	}
	rows, err := q.ListGuildsByIDs(ctx, guildIDs)
	if err != nil {
		return nil, fmt.Errorf("list guilds by ids: %w", err)
	}
	result := make(map[int64]rankingdomain.Guild, len(rows))
	for _, g := range rows {
		result[g.ID] = rankingdomain.Guild{
			ID:        g.ID,
			Name:      g.Name,
			CreatedAt: g.CreatedAt.Time,
			UpdatedAt: g.UpdatedAt.Time,
		}
	}
	return result, nil
}

// ListAllGuildScores は全ギルドのスコアをスコア降順で返す（バッチ同期用）。
func (r *RankingRepository) ListAllGuildScores(ctx context.Context, tx shared.Tx) ([]rankingdomain.GuildScore, error) {
	q, err := r.querier(tx)
	if err != nil {
		return nil, fmt.Errorf("ListAllGuildScores: %w", err)
	}
	rows, err := q.ListAllGuildScores(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all guild scores: %w", err)
	}
	result := make([]rankingdomain.GuildScore, 0, len(rows))
	for _, gs := range rows {
		result = append(result, rankingdomain.GuildScore{
			GuildID:   gs.GuildID,
			Score:     gs.Score,
			UpdatedAt: gs.UpdatedAt.Time,
		})
	}
	return result, nil
}

// GetUserPoints はユーザーの累計ポイントを取得する。
// 未登録（該当行なし）は ErrPointsNotFound へ変換する。
func (r *RankingRepository) GetUserPoints(ctx context.Context, tx shared.Tx, userID int64) (rankingdomain.UserPoint, error) {
	q, err := r.querier(tx)
	if err != nil {
		return rankingdomain.UserPoint{}, fmt.Errorf("GetUserPoints: %w", err)
	}
	up, err := q.GetUserPoints(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound
		}
		return rankingdomain.UserPoint{}, fmt.Errorf("get user points: %w", err)
	}
	return rankingdomain.UserPoint{
		UserID:    up.UserID,
		Points:    up.Points,
		UpdatedAt: up.UpdatedAt.Time,
	}, nil
}

// IncrementUserPoints はユーザーの累計ポイントを加算する（行が無ければ作成する upsert）。
func (r *RankingRepository) IncrementUserPoints(ctx context.Context, tx shared.Tx, userID int64, points int64) error {
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("IncrementUserPoints: %w", err)
	}
	if err := q.IncrementUserPoints(ctx, sqlc.IncrementUserPointsParams{
		UserID: userID,
		Points: points,
	}); err != nil {
		return fmt.Errorf("increment user points: %w", err)
	}
	return nil
}

// InsertUserPointHistory は個人ポイントの加算履歴を1件記録する。
func (r *RankingRepository) InsertUserPointHistory(ctx context.Context, tx shared.Tx, userID int64, points int64, reason string) error {
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("InsertUserPointHistory: %w", err)
	}
	if err := q.InsertUserPointHistory(ctx, sqlc.InsertUserPointHistoryParams{
		UserID: userID,
		Points: points,
		Reason: reason,
	}); err != nil {
		return fmt.Errorf("insert user point history: %w", err)
	}
	return nil
}

// ListUsersByIDs は指定 ID のユーザー名を一括取得し、ID をキーにしたマップで返す。
// 存在しない ID は結果に含まれない（呼び出し側が欠落を許容する前提）。
func (r *RankingRepository) ListUsersByIDs(ctx context.Context, tx shared.Tx, userIDs []int64) (map[int64]string, error) {
	q, err := r.querier(tx)
	if err != nil {
		return nil, fmt.Errorf("ListUsersByIDs: %w", err)
	}
	rows, err := q.ListUsersByIDs(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list users by ids: %w", err)
	}
	result := make(map[int64]string, len(rows))
	for _, u := range rows {
		result[u.ID] = u.Name
	}
	return result, nil
}

// ListAllUserPoints は全ユーザーのポイントをポイント降順で返す（バッチ同期用）。
func (r *RankingRepository) ListAllUserPoints(ctx context.Context, tx shared.Tx) ([]rankingdomain.UserPoint, error) {
	q, err := r.querier(tx)
	if err != nil {
		return nil, fmt.Errorf("ListAllUserPoints: %w", err)
	}
	rows, err := q.ListAllUserPoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("list all user points: %w", err)
	}
	result := make([]rankingdomain.UserPoint, 0, len(rows))
	for _, up := range rows {
		result = append(result, rankingdomain.UserPoint{
			UserID:    up.UserID,
			Points:    up.Points,
			UpdatedAt: up.UpdatedAt.Time,
		})
	}
	return result, nil
}
