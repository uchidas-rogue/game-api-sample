// Package ranking はランキング機能のドメインモデルを提供する。
package ranking

import "time"

// Guild はGvGに参加するギルド。
type Guild struct {
	ID        int64
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// GuildScore はギルドのスコア情報。
type GuildScore struct {
	GuildID   int64
	Score     int64
	UpdatedAt time.Time
}

// UserPoint はユーザーのポイント情報。
type UserPoint struct {
	UserID    int64
	Points    int64
	UpdatedAt time.Time
}

// RankEntry はランキング1件分の情報。
type RankEntry struct {
	Rank  int64
	ID    int64
	Name  string
	Score int64
}

// UserPointAddResult は個人ポイント加算の結果。
// MySQL 側の累計更新の結果のみ返す。順位（Rank/GuildRank）は worker による
// Redis 反映を待つ必要があるため本結果には含めず、別 API で取得する。
// ギルドスコアの加算は outbox-worker に非同期化したため、
// API 同期レスポンスでは正確な値を返せない。GuildID のみ残し、
// ギルドの previous/new total は返さない（ランキングは Redis 経由で別途参照）。
type UserPointAddResult struct {
	UserID        int64
	Points        int64
	PreviousTotal int64
	NewTotal      int64

	GuildID int64
}

// GuildRankResult はギルド順位取得の結果。
type GuildRankResult struct {
	GuildID     int64
	GuildName   string
	Score       int64
	Rank        int64
	TotalGuilds int64
}

// UserRankResult はユーザー順位取得の結果。
type UserRankResult struct {
	UserID     int64
	UserName   string
	Points     int64
	Rank       int64
	TotalUsers int64
}
