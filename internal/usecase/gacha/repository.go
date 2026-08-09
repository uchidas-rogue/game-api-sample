// Package gacha のうち、リポジトリインターフェースを定義する。
// CLAUDE.md §2 の依存関係逆転に従い、usecase 層が必要とする抽象は usecase 層で定義する。
// 実体は infrastructure 層が実装する。
package gacha

//go:generate mockgen -source=repository.go -destination=mock/mock_repository.go -package=mock_gacha

import (
	"context"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// Repository はガチャ機能のデータアクセスインターフェース。
// 各メソッドは shared.Tx を引数で受け取ることで、usecase 層がトランザクション境界を
// 制御する設計を実現する。tx は Transactor.DoInshared.Tx から渡される値をそのまま透過する。
type Repository interface {
	// GetUserForUpdate はユーザー行を排他ロック付きで取得する。
	// 必ずトランザクション内で呼び出すこと。
	GetUserForUpdate(ctx context.Context, tx shared.Tx, userID int64) (gachadomain.User, error)

	// UpdateUserGems は石残高を更新する。
	UpdateUserGems(ctx context.Context, tx shared.Tx, userID int64, newGems int) error

	// ListItems はアイテムマスタ全件を返す。
	// マスタ参照のためトランザクション必須ではないが、シグネチャを統一するため tx を受ける。
	ListItems(ctx context.Context, tx shared.Tx) ([]gachadomain.Item, error)

	// UpsertUserItems はユーザー所持アイテムを一括追加（同一item_idは加算）する。
	// items は item_id 昇順で渡すこと。同一トランザクション内で行ロックを昇順取得させ、
	// 逆順アクセスによるデッドロックを避けるための前提（呼び出し側が保証する）。
	UpsertUserItems(ctx context.Context, tx shared.Tx, userID int64, items []UserItemCount) error

	// InsertGachaHistories はガチャ履歴を一括追加する（抽選順）。
	InsertGachaHistories(ctx context.Context, tx shared.Tx, userID int64, itemIDs []int64) error
}

// UserItemCount は所持アイテムの加算単位（bulk upsert 用）。
type UserItemCount struct {
	ItemID int64
	Num    int
}
