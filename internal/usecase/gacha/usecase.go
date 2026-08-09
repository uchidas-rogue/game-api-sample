// Package gacha はガチャ機能のユースケースを提供する。
// CLAUDE.md の方針に従い、本層がトランザクション境界を制御する。
package gacha

//go:generate mockgen -source=usecase.go -destination=mock/mock_usecase.go -package=mock_gacha

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// Result はマルチガチャの結果。
type Result struct {
	UserID        int64
	DrawnItems    []gachadomain.Item
	RemainingGems int
}

// Randomizer は抽選用乱数の抽象。テストで決定論的に振る舞わせるために注入可能とする。
type Randomizer interface {
	IntN(n int) int
}

// Usecase はマルチガチャのユースケースインターフェース。
type Usecase interface {
	Multi(ctx context.Context, userID int64, pullCount int) (Result, error)
}

// usecase が Usecase を満たすことをコンパイル時に検証する。
var _ Usecase = (*usecase)(nil)

// usecase は Usecase の具象実装。
type usecase struct {
	tx     shared.Transactor
	repo   Repository
	rand   Randomizer
	logger *slog.Logger
}

// NewUsecase は Usecase を生成する。rand に nil を渡した場合は既定実装を使う。logger は必須。
func NewUsecase(tx shared.Transactor, repo Repository, rnd Randomizer, logger *slog.Logger) Usecase {
	if rnd == nil {
		rnd = defaultRandomizer{}
	}
	return &usecase{tx: tx, repo: repo, rand: rnd, logger: logger}
}

// Multi はマルチガチャ（PullCount回分の抽選）を実行する。
//
// トランザクション設計（負荷試験向けの注意点）:
//   - アイテムマスタ（items）の取得はトランザクションの外で行う。更新しないためロック順序の
//     対象外であり、行ロックを保持したままマスタ参照の往復をはさまないようにする。
//   - 単一トランザクション内で users を FOR UPDATE → user_items を upsert → gacha_histories を INSERT。
//   - 行ロック取得順序は常に「users → user_items（item_id 昇順）」に統一する。
//     複数ユーザーが同時に同じアイテム集合を引いてもデッドロックを最小化する目的。
//   - 抽選結果が同一 item_id に複数回当選した場合は集約してから upsert することで
//     行ロックの取得回数とラウンドトリップを削減する（負荷試験を意識した最適化）。
func (u *usecase) Multi(ctx context.Context, userID int64, pullCount int) (Result, error) {
	if !gachadomain.IsValidPullCount(pullCount) {
		return Result{}, fmt.Errorf("%w: %d (allowed: %d-%d)", gachadomain.ErrInvalidPullCount, pullCount, gachadomain.MinPullCount, gachadomain.MaxPullCount)
	}

	// アイテムマスタはトランザクションの外で読む（tx=nil）。更新しないためロック順序の
	// 対象外であり、tx 内に置くと users の行ロックを保持したままマスタ参照の往復が
	// 1回はさまる。外に出すことで、マスタ不備（下の2分岐）はトランザクションを張らず
	// 行ロックも取らずに弾ける。
	items, err := u.repo.ListItems(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	if len(items) == 0 {
		return Result{}, gachadomain.ErrNoItemsAvailable
	}

	gemCost := gachadomain.GemCostFor(pullCount)
	var result Result
	err = u.tx.DoInTx(ctx, func(tx shared.Tx) error {
		user, err := u.repo.GetUserForUpdate(ctx, tx, userID)
		if err != nil {
			return err
		}
		if !user.HasEnoughGemsFor(pullCount) {
			return fmt.Errorf("user %d: %w (have=%d, need=%d)", userID, gachadomain.ErrInsufficientGems, user.GemNum, gemCost)
		}

		drawn, err := u.draw(items, pullCount)
		if err != nil {
			return err
		}

		newGems := user.GemNum - gemCost
		if err := u.repo.UpdateUserGems(ctx, tx, userID, newGems); err != nil {
			return err
		}

		// 当選結果を item_id でグルーピングし、ID 昇順の1文で一括 upsert する。
		// 昇順にすることで行ロックの取得順序を揃え、逆順アクセスによるデッドロックを避ける。
		// 単行ループから複数行 INSERT へ集約し、トランザクション内の DB 往復とロック保持時間を削減する。
		counts := aggregateByID(drawn)
		sortedIDs := sortedKeys(counts)
		userItems := make([]UserItemCount, 0, len(sortedIDs))
		for _, itemID := range sortedIDs {
			userItems = append(userItems, UserItemCount{ItemID: itemID, Num: counts[itemID]})
		}
		if err := u.repo.UpsertUserItems(ctx, tx, userID, userItems); err != nil {
			return err
		}

		// ガチャ履歴を抽選順に1文で一括追加（書き込み負荷検証用）。
		historyItemIDs := make([]int64, len(drawn))
		for i, it := range drawn {
			historyItemIDs[i] = it.ID
		}
		if err := u.repo.InsertGachaHistories(ctx, tx, userID, historyItemIDs); err != nil {
			return err
		}

		result = Result{
			UserID:        userID,
			DrawnItems:    drawn,
			RemainingGems: newGems,
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// draw は重み付きランダムで count 個のアイテムを抽選する。
func (u *usecase) draw(items []gachadomain.Item, count int) ([]gachadomain.Item, error) {
	totalWeight := 0
	for _, it := range items {
		if it.Weight > 0 {
			totalWeight += it.Weight
		}
	}
	if totalWeight <= 0 {
		return nil, gachadomain.ErrInvalidItemWeights
	}

	results := make([]gachadomain.Item, 0, count)
	for range count {
		r := u.rand.IntN(totalWeight)
		acc := 0
		for _, it := range items {
			if it.Weight <= 0 {
				continue
			}
			acc += it.Weight
			if r < acc {
				results = append(results, it)
				break
			}
		}
	}
	return results, nil
}

// aggregateByID は当選アイテム配列を item_id → 個数 に集約する。
func aggregateByID(items []gachadomain.Item) map[int64]int {
	m := make(map[int64]int, len(items))
	for _, it := range items {
		m[it.ID] += gachadomain.GrantQuantity
	}
	return m
}

// sortedKeys は map の int64 キーを昇順で返す。
// 行ロック取得順序を一定に保つことでデッドロックを回避する目的。
func sortedKeys(m map[int64]int) []int64 {
	return slices.Sorted(maps.Keys(m))
}

// defaultRandomizer は math/rand/v2 を使う既定実装。
var _ Randomizer = defaultRandomizer{}

type defaultRandomizer struct{}

// IntN は半開区間 [0, n) の整数を返す。
func (defaultRandomizer) IntN(n int) int { return rand.IntN(n) }
