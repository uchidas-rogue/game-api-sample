// Package repository は infrastructure 層の Repository 実装群を提供する。
// sqlc 生成コードに依存し、usecase 層が要求するインターフェースを満たすように
// sqlc モデル ⇄ domain エンティティの変換を行う。
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	gachausecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
)

// GachaRepository が gachausecase.Repository を満たすことをコンパイル時に検証する。
var _ gachausecase.Repository = (*GachaRepository)(nil)

// querierFactory は usecase.Tx から sqlc.Querier を返すファクトリ。
// 本番では Queries / Queries.WithTx を返し、テストではモック Querier を返すために差し替え可能とする。
type querierFactory func(tx gachausecase.Tx) (sqlc.Querier, error)

// GachaRepository は gachausecase.Repository の sqlc/MySQL 実装。
type GachaRepository struct {
	querier querierFactory
}

// NewGachaRepository は GachaRepository を生成する。
// db は通常 *sql.DB を渡す。実際のクエリは tx に応じて WithTx で切り替える。
func NewGachaRepository(db sqlc.DBTX) *GachaRepository {
	base := sqlc.New(db)
	return &GachaRepository{
		querier: func(tx gachausecase.Tx) (sqlc.Querier, error) {
			if tx == nil {
				return base, nil
			}
			sqlTx, ok := tx.(*infraMysql.SQLTx)
			if !ok {
				return nil, fmt.Errorf("unexpected tx type: %T", tx)
			}
			return base.WithTx(sqlTx.Raw()), nil
		},
	}
}

// GetUserForUpdate はユーザー行を排他ロックで取得する。
// tx は必須。nil の場合は FOR UPDATE がトランザクション外になるためエラーを返す。
func (r *GachaRepository) GetUserForUpdate(ctx context.Context, tx gachausecase.Tx, userID int64) (gachadomain.User, error) {
	if tx == nil {
		return gachadomain.User{}, fmt.Errorf("GetUserForUpdate: transaction is required")
	}
	q, err := r.querier(tx)
	if err != nil {
		return gachadomain.User{}, fmt.Errorf("GetUserForUpdate: %w", err)
	}
	u, err := q.GetUserForUpdate(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return gachadomain.User{}, fmt.Errorf("get user for update (id=%d): %w", userID, gachadomain.ErrUserNotFound)
		}
		return gachadomain.User{}, fmt.Errorf("failed to get user for update (id=%d): %w", userID, err)
	}
	return toDomainUser(u), nil
}

// UpdateUserGems は石残高を更新する。
func (r *GachaRepository) UpdateUserGems(ctx context.Context, tx gachausecase.Tx, userID int64, newGems int) error {
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("UpdateUserGems: %w", err)
	}
	if err := q.UpdateUserGems(ctx, sqlc.UpdateUserGemsParams{
		GemNum: int32(newGems),
		ID:     userID,
	}); err != nil {
		return fmt.Errorf("failed to update user gems (id=%d): %w", userID, err)
	}
	return nil
}

// ListItems はアイテムマスタを全件取得する。
func (r *GachaRepository) ListItems(ctx context.Context, tx gachausecase.Tx) ([]gachadomain.Item, error) {
	q, err := r.querier(tx)
	if err != nil {
		return nil, fmt.Errorf("ListItems: %w", err)
	}
	rows, err := q.ListItems(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list items: %w", err)
	}
	items := make([]gachadomain.Item, 0, len(rows))
	for _, row := range rows {
		items = append(items, toDomainItem(row))
	}
	return items, nil
}

// UpsertUserItem はユーザー所持アイテムを追加（既存は加算）する。
func (r *GachaRepository) UpsertUserItem(ctx context.Context, tx gachausecase.Tx, userID int64, itemID int64, num int) error {
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("UpsertUserItem: %w", err)
	}
	if err := q.UpsertUserItem(ctx, sqlc.UpsertUserItemParams{
		UserID: userID,
		ItemID: itemID,
		Num:    int32(num),
	}); err != nil {
		return fmt.Errorf("failed to upsert user item (user=%d, item=%d): %w", userID, itemID, err)
	}
	return nil
}

// InsertGachaHistory はガチャ履歴を1件追加する。
func (r *GachaRepository) InsertGachaHistory(ctx context.Context, tx gachausecase.Tx, userID int64, itemID int64) error {
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("InsertGachaHistory: %w", err)
	}
	if err := q.InsertGachaHistory(ctx, sqlc.InsertGachaHistoryParams{
		UserID: userID,
		ItemID: itemID,
	}); err != nil {
		return fmt.Errorf("failed to insert gacha history (user=%d, item=%d): %w", userID, itemID, err)
	}
	return nil
}

// toDomainUser は sqlc.User を domain.User に変換する。
func toDomainUser(u sqlc.User) gachadomain.User {
	return gachadomain.User{
		ID:        u.ID,
		Name:      u.Name,
		GemNum:    int(u.GemNum),
		CreatedAt: u.CreatedAt.Time,
		UpdatedAt: u.UpdatedAt.Time,
	}
}

// toDomainItem は sqlc.Item を domain.Item に変換する。
func toDomainItem(i sqlc.Item) gachadomain.Item {
	return gachadomain.Item{
		ID:        i.ID,
		Name:      i.Name,
		Rarity:    int(i.Rarity),
		Weight:    int(i.Weight),
		CreatedAt: i.CreatedAt.Time,
	}
}
