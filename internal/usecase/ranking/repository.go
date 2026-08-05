// Package ranking はランキング機能のユースケースを提供する。
package ranking

//go:generate mockgen -source=repository.go -destination=mock/mock_repository.go -package=mock_ranking

import (
	"context"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// Repository は MySQL 操作のインターフェース。
type Repository interface {
	// GetGuild はギルド情報を取得する。
	GetGuild(ctx context.Context, tx shared.Tx, guildID int64) (rankingdomain.Guild, error)

	// GetUser はユーザー情報を取得する。
	GetUser(ctx context.Context, tx shared.Tx, userID int64) (userName string, err error)

	// GetGuildScore はギルドの現在スコアを取得する。
	GetGuildScore(ctx context.Context, tx shared.Tx, guildID int64) (rankingdomain.GuildScore, error)

	// IncrementGuildScore はギルドスコアを加算する。
	IncrementGuildScore(ctx context.Context, tx shared.Tx, guildID int64, score int64) error

	// InsertGuildScoreHistory はスコア履歴を記録する。
	InsertGuildScoreHistory(ctx context.Context, tx shared.Tx, guildID int64, userID int64, score int64) error

	// BulkIncrementGuildScores は複数ギルドのスコアを1文の複数行 upsert で一括加算する。
	// outbox-worker がバッチ単位トランザクションで適用するために使う。
	// 実装は deltas を GuildID 昇順にソートしてから発行すること（並行するバッチ tx 間で
	// ロック取得順を揃え、デッドロックを防ぐため）。
	BulkIncrementGuildScores(ctx context.Context, tx shared.Tx, deltas []rankingdomain.GuildScoreDelta) error

	// BulkInsertGuildScoreHistories はスコア履歴を1文の複数行 INSERT で一括記録する。
	// スコア加算はギルド単位に集約できるが、履歴はイベント単位で残す必要がある。
	BulkInsertGuildScoreHistories(ctx context.Context, tx shared.Tx, entries []rankingdomain.GuildScoreHistoryEntry) error

	// IsUserInGuild はユーザーがギルドに所属しているかを確認する。
	IsUserInGuild(ctx context.Context, tx shared.Tx, userID int64, guildID int64) (bool, error)

	// GetUserGuildID はユーザーが所属するギルドIDを取得する。未所属時は ErrUserNotInGuild を返す。
	GetUserGuildID(ctx context.Context, tx shared.Tx, userID int64) (int64, error)

	// ListGuildsByIDs は指定 ID のギルド情報を一括取得する。
	ListGuildsByIDs(ctx context.Context, tx shared.Tx, guildIDs []int64) (map[int64]rankingdomain.Guild, error)

	// ListAllGuildScores は全ギルドのスコアを取得する（バッチ同期用）。
	ListAllGuildScores(ctx context.Context, tx shared.Tx) ([]rankingdomain.GuildScore, error)

	// GetUserPoints はユーザーのポイントを取得する。
	GetUserPoints(ctx context.Context, tx shared.Tx, userID int64) (rankingdomain.UserPoint, error)

	// IncrementUserPoints はユーザーポイントを加算する。
	IncrementUserPoints(ctx context.Context, tx shared.Tx, userID int64, points int64) error

	// InsertUserPointHistory はポイント履歴を記録する。
	InsertUserPointHistory(ctx context.Context, tx shared.Tx, userID int64, points int64, reason string) error

	// ListUsersByIDs は指定 ID のユーザー情報を一括取得する。
	ListUsersByIDs(ctx context.Context, tx shared.Tx, userIDs []int64) (map[int64]string, error)

	// ListAllUserPoints は全ユーザーのポイントを取得する（バッチ同期用）。
	ListAllUserPoints(ctx context.Context, tx shared.Tx) ([]rankingdomain.UserPoint, error)
}

// RankingStore は Redis 操作のインターフェース。
type RankingStore interface {
	// IncrementGuildScore はギルドのスコアを加算する。
	IncrementGuildScore(ctx context.Context, guildID int64, score int64) error

	// SetGuildScore はギルドのスコアを上書きする（バッチ同期用）。
	SetGuildScore(ctx context.Context, guildID int64, score int64) error

	// GetGuildRank はギルドの順位を取得する（1-indexed）。
	GetGuildRank(ctx context.Context, guildID int64) (int64, error)

	// GetGuildScore はギルドのスコアを取得する。
	GetGuildScore(ctx context.Context, guildID int64) (int64, bool, error)

	// GetGuildRankings は上位ギルドランキングを取得する。
	GetGuildRankings(ctx context.Context, offset, limit int) ([]rankingdomain.RankEntry, error)

	// GetGuildTotalCount はランキング登録済みのギルド総数を取得する。
	GetGuildTotalCount(ctx context.Context) (int64, error)

	// IncrementUserPoints はユーザーのポイントを加算する。
	IncrementUserPoints(ctx context.Context, userID int64, points int64) error

	// ApplyScoreDeltas はユーザー・ギルド双方の加算をまとめて反映する。
	// 実装はパイプラインで1往復に集約すること（イベント単位に往復すると
	// トランザクションのロック保持時間が加算件数に比例して伸びるため）。
	ApplyScoreDeltas(ctx context.Context, users []rankingdomain.UserPointDelta, guilds []rankingdomain.GuildScoreDelta) error

	// SetUserPoints はユーザーのポイントを上書きする（バッチ同期用）。
	SetUserPoints(ctx context.Context, userID int64, points int64) error

	// GetUserRank はユーザーの順位を取得する（1-indexed）。
	GetUserRank(ctx context.Context, userID int64) (int64, error)

	// GetUserPoints はユーザーのポイントを取得する。
	GetUserPoints(ctx context.Context, userID int64) (int64, bool, error)

	// GetUserRankings は上位ユーザーランキングを取得する。
	GetUserRankings(ctx context.Context, offset, limit int) ([]rankingdomain.RankEntry, error)

	// GetUserTotalCount はランキング登録済みのユーザー総数を取得する。
	GetUserTotalCount(ctx context.Context) (int64, error)
}
