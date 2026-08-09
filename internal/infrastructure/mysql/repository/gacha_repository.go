// Package repository は infrastructure 層の Repository 実装群を提供する。
// sqlc 生成コードに依存し、usecase 層が要求するインターフェースを満たすように
// sqlc モデル ⇄ domain エンティティの変換を行う。
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	gachausecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// GachaRepository が gachausecase.Repository を満たすことをコンパイル時に検証する。
var _ gachausecase.Repository = (*GachaRepository)(nil)

// GachaRepository は gachausecase.Repository の sqlc/MySQL 実装。
type GachaRepository struct {
	querier querierFactory
	execer  execerFactory
}

// NewGachaRepository は GachaRepository を生成する。
// db は通常 *sql.DB を渡す。実際のクエリは tx に応じて WithTx で切り替える。
func NewGachaRepository(db sqlc.DBTX) *GachaRepository {
	return &GachaRepository{
		querier: newQuerierFactory(db),
		execer:  newExecerFactory(db),
	}
}

// GetUserForUpdate はユーザー行を排他ロックで取得する。
// tx は必須。nil の場合は FOR UPDATE がトランザクション外になるためエラーを返す。
func (r *GachaRepository) GetUserForUpdate(ctx context.Context, tx shared.Tx, userID int64) (gachadomain.User, error) {
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
func (r *GachaRepository) UpdateUserGems(ctx context.Context, tx shared.Tx, userID int64, newGems int) error {
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
func (r *GachaRepository) ListItems(ctx context.Context, tx shared.Tx) ([]gachadomain.Item, error) {
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

// 複数行 INSERT の1行あたりのプレースホルダ数。args のキャパシティ計算に使う。
const (
	// userItemColumns は user_items の (user_id, item_id, num)。
	userItemColumns = 3
	// gachaHistoryColumns は gacha_histories の (user_id, item_id)。
	gachaHistoryColumns = 2
)

// UpsertUserItems はユーザー所持アイテムを複数行 INSERT で一括追加（既存は加算）する。
// sqlc は可変行数の複数行 INSERT を生成できないため、execer で生 SQL を発行する。
// items は呼び出し側で item_id 昇順に並べてある前提（行ロック取得順を揃えデッドロックを回避）。
func (r *GachaRepository) UpsertUserItems(ctx context.Context, tx shared.Tx, userID int64, items []gachausecase.UserItemCount) error {
	if len(items) == 0 {
		return nil
	}
	ex, err := r.execer(tx)
	if err != nil {
		return fmt.Errorf("UpsertUserItems: %w", err)
	}
	valuesClause := strings.TrimSuffix(strings.Repeat("(?, ?, ?),", len(items)), ",")
	query := "INSERT INTO user_items (user_id, item_id, num) VALUES " + valuesClause +
		" ON DUPLICATE KEY UPDATE num = num + VALUES(num)"
	args := make([]any, 0, len(items)*userItemColumns)
	for _, it := range items {
		args = append(args, userID, it.ItemID, int32(it.Num))
	}
	if _, err := ex.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("failed to bulk upsert user items (user=%d, count=%d): %w", userID, len(items), err)
	}
	return nil
}

// InsertGachaHistories はガチャ履歴を複数行 INSERT で一括追加する。
func (r *GachaRepository) InsertGachaHistories(ctx context.Context, tx shared.Tx, userID int64, itemIDs []int64) error {
	if len(itemIDs) == 0 {
		return nil
	}
	ex, err := r.execer(tx)
	if err != nil {
		return fmt.Errorf("InsertGachaHistories: %w", err)
	}
	valuesClause := strings.TrimSuffix(strings.Repeat("(?, ?),", len(itemIDs)), ",")
	query := "INSERT INTO gacha_histories (user_id, item_id) VALUES " + valuesClause
	args := make([]any, 0, len(itemIDs)*gachaHistoryColumns)
	for _, itemID := range itemIDs {
		args = append(args, userID, itemID)
	}
	if _, err := ex.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("failed to bulk insert gacha histories (user=%d, count=%d): %w", userID, len(itemIDs), err)
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
