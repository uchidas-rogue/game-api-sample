// Package gacha はガチャ機能のドメイン層を提供する。
// ここでは特定のフレームワーク・DBに依存しない純粋なビジネスエンティティと
// 業務上の不変・定数のみを定義する。
package gacha

import "time"

// ガチャ仕様に関する固定値（マジックナンバー禁止対応）。
const (
	// MinPullCount は1リクエストで指定可能な最小抽選回数。
	MinPullCount = 1
	// MaxPullCount は1リクエストで指定可能な最大抽選回数。
	MaxPullCount = 10
	// GemCostPerPull は1回の単発抽選に必要な石の数。
	GemCostPerPull = 100
	// GrantQuantity は当選時に1度の抽選で付与される個数。
	GrantQuantity = 1
)

// IsValidPullCount は pullCount が許容範囲 [MinPullCount, MaxPullCount] に収まるかを返す。
func IsValidPullCount(pullCount int) bool {
	return pullCount >= MinPullCount && pullCount <= MaxPullCount
}

// GemCostFor は pullCount 回分のマルチガチャに必要な石の総量を返す。
func GemCostFor(pullCount int) int {
	return GemCostPerPull * pullCount
}

// レアリティ定数（items.rarity 列に対応）。
const (
	RarityN   = 1
	RarityR   = 2
	RaritySR  = 3
	RaritySSR = 4
)

// User はガチャに参加するユーザー。
type User struct {
	ID        int64
	Name      string
	GemNum    int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// HasEnoughGemsFor は pullCount 回分のマルチガチャに必要な石を保有しているかを返す。
func (u User) HasEnoughGemsFor(pullCount int) bool {
	return u.GemNum >= GemCostFor(pullCount)
}

// Item はガチャから排出されるアイテムマスタ。
type Item struct {
	ID        int64
	Name      string
	Rarity    int
	Weight    int
	CreatedAt time.Time
}

// UserItem はユーザーが所持するアイテムの数量。
type UserItem struct {
	UserID    int64
	ItemID    int64
	Num       int
	CreatedAt time.Time
	UpdatedAt time.Time
}
