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

// GuildScoreSubmitResult はギルドスコア送信の結果。
type GuildScoreSubmitResult struct {
	GuildID       int64
	Score         int64
	IsHighScore   bool
	PreviousScore int64
	Rank          int64
}

// UserPointAddResult は個人ポイント加算の結果。
type UserPointAddResult struct {
	UserID        int64
	Points        int64
	PreviousTotal int64
	NewTotal      int64
	Rank          int64
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
