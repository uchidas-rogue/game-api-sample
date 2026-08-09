// Package gacha_test は gacha ユースケースの外部テストパッケージ。
// 公開 API のみを対象とし、モックを用いてデータベース接続をバイパスする。
//
// テスト設計（フロー図・テスト仕様表）は docs/testing/gacha.md にある。
// 分岐を追加・変更したら、まず図と表を更新してからここを直す。
package gacha_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
	mockuc "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
	mockshared "github.com/uchidas-rogue/game-api-sample/internal/usecase/shared/mock"
)

const testUserID = int64(42)

// newTestItems はテスト用のアイテムリストを返す。
// 合計 weight = 100（item1:60, item2:40）で、IntN(100) の戻り値によって
// 当選アイテムを決定論的に制御できる。
//
// パッケージスコープの変数ではなく関数にしているのは、t.Parallel() 配下の
// サブテスト間で同一スライスを共有しないため（AGENTS.md §3 の pkg-global 書換禁止）。
func newTestItems() []gachadomain.Item {
	return []gachadomain.Item{
		{ID: 1, Name: "ノーマルソード", Rarity: gachadomain.RarityN, Weight: 60},
		{ID: 2, Name: "レアソード", Rarity: gachadomain.RarityR, Weight: 40},
	}
}

// newItemsWithZeroWeight は先頭に Weight 0 のアイテムを混ぜたリストを返す。
// draw 内の「Weight ≤ 0 はスキップ」分岐（docs/testing/gacha.md の D4→D5）を通すためのもの。
// 合計 weight は newTestItems と同じ 100 に保つ。
func newItemsWithZeroWeight() []gachadomain.Item {
	return []gachadomain.Item{
		{ID: 3, Name: "抽選対象外アイテム", Rarity: gachadomain.RarityN, Weight: 0},
		{ID: 1, Name: "ノーマルソード", Rarity: gachadomain.RarityN, Weight: 60},
		{ID: 2, Name: "レアソード", Rarity: gachadomain.RarityR, Weight: 40},
	}
}

// step は Multi のフロー上で失敗させる地点。
// テーブルが「どこまで進んでどこで失敗するか」をデータとして表現するために使う。
type step int

const (
	stepNone step = iota // repo / tx はすべて成功する
	stepBeginTx
	stepGetUser
	stepListItems
	stepUpdateGems
	stepUpsertItem
	stepInsertHistory
)

// multiCase は Multi のテストケース1件。**データのみ**を持ち、手続きは持たない。
type multiCase struct {
	name string

	// ---- 入力 ----
	pullCount int
	// userGems は 0 のとき「十分な石」を自動で割り当てる。
	// 石の過不足の境界値は domain の HasEnoughGemsFor が担保するため、
	// ここでは「足りる / 足りない」の分岐だけを区別する。
	userGems int
	// items は nil のとき newTestItems() を使う。
	items []gachadomain.Item
	// randValue は Randomizer が毎回返す固定値。
	randValue int
	// wantDrawnIndex は items のうち毎回当選するアイテムの添字。
	// 抽選アルゴリズムをテスト側で再実装しないよう、期待値をデータとして持つ。
	wantDrawnIndex int

	// ---- どこで失敗させるか ----
	failAt  step
	failErr error

	// ---- 期待結果 ----
	wantErrIs error
}

func TestUsecase_Multi(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	// infrastructure 層が sql.ErrNoRows を変換して返すケースを模す。
	errUserNotFound := gachadomain.ErrUserNotFound

	// docs/testing/gacha.md の「1-3. テスト仕様表」と 1 対 1 で対応する。
	// 並び順は図のパスが短い順（エラー・早期リターンが先、正常系フルフローが後）。
	tests := []multiCase{
		{
			// #1 A→B→E1
			name:      "pullCount が下限未満（0）: DoInTx に入らない",
			pullCount: gachadomain.MinPullCount - 1,
			wantErrIs: gachadomain.ErrInvalidPullCount,
		},
		{
			// #2 A→B→E1
			name:      "pullCount が上限超過: DoInTx に入らない",
			pullCount: gachadomain.MaxPullCount + 1,
			wantErrIs: gachadomain.ErrInvalidPullCount,
		},
		{
			// #3 A→B→C→D→E2（fn が実行されない）
			name:      "DoInTx 自体がエラー: fn が実行されず repo が一切呼ばれない",
			pullCount: gachadomain.MaxPullCount,
			failAt:    stepBeginTx,
			failErr:   errDB,
			wantErrIs: errDB,
		},
		{
			// #4 A→B→C→D→F→E3
			// 「一般エラー」と「ErrUserNotFound」は同一パスのため 1 ケースに統合し、
			// より情報量の多い domain sentinel の透過を検証する。
			name:      "GetUserForUpdate がエラー: repo のエラーを変換せず返す",
			pullCount: gachadomain.MaxPullCount,
			failAt:    stepGetUser,
			failErr:   errUserNotFound,
			wantErrIs: errUserNotFound,
		},
		{
			// #5 …→F→G→E4
			name:      "石が不足: ListItems 以降が呼ばれない",
			pullCount: gachadomain.MaxPullCount,
			userGems:  1,
			wantErrIs: gachadomain.ErrInsufficientGems,
		},
		{
			// #6 …→G→H→E5
			name:      "ListItems がエラー",
			pullCount: gachadomain.MaxPullCount,
			failAt:    stepListItems,
			failErr:   errDB,
			wantErrIs: errDB,
		},
		{
			// #7 …→H→I→E6
			name:      "items が空",
			pullCount: gachadomain.MaxPullCount,
			items:     []gachadomain.Item{},
			wantErrIs: gachadomain.ErrNoItemsAvailable,
		},
		{
			// #8 …→I→J→E7（draw の D1→D2→DE）
			name:      "全アイテムの Weight が 0",
			pullCount: gachadomain.MaxPullCount,
			items: []gachadomain.Item{
				{ID: 10, Name: "無効アイテム1", Rarity: gachadomain.RarityN, Weight: 0},
				{ID: 11, Name: "無効アイテム2", Rarity: gachadomain.RarityR, Weight: 0},
			},
			wantErrIs: gachadomain.ErrInvalidItemWeights,
		},
		{
			// #9 …→J→K→E8
			name:      "UpdateUserGems がエラー: Upsert 以降が呼ばれない",
			pullCount: gachadomain.MaxPullCount,
			failAt:    stepUpdateGems,
			failErr:   errDB,
			wantErrIs: errDB,
		},
		{
			// #10 …→K→L→E9
			name:      "UpsertUserItem がエラー: InsertGachaHistory が呼ばれない",
			pullCount: gachadomain.MaxPullCount,
			failAt:    stepUpsertItem,
			failErr:   errDB,
			wantErrIs: errDB,
		},
		{
			// #11 …→L→M→E10
			name:      "InsertGachaHistory がエラー",
			pullCount: gachadomain.MaxPullCount,
			failAt:    stepInsertHistory,
			failErr:   errDB,
			wantErrIs: errDB,
		},
		{
			// #12 …→M→N→Z（draw は D4→D6→D7 で即当選）
			name:           "正常系: 1件目のアイテムが当選（IntN=0）",
			pullCount:      gachadomain.MaxPullCount,
			randValue:      0,
			wantDrawnIndex: 0,
		},
		{
			// #13 …→M→N→Z（draw の D6 が No→Yes）
			// IntN=60 は items[0].Weight と同値。r < acc が偽になり2件目へ進むことを狙う。
			name:           "正常系: 2件目のアイテムが当選（IntN=1件目の重みと同値）",
			pullCount:      gachadomain.MaxPullCount,
			randValue:      60,
			wantDrawnIndex: 1,
		},
		{
			// #14 …→M→N→Z（draw の D4→D5 を通過）
			name:           "正常系: Weight 0 のアイテムが混在しても抽選対象から除外される",
			pullCount:      gachadomain.MaxPullCount,
			items:          newItemsWithZeroWeight(),
			randValue:      0,
			wantDrawnIndex: 1, // 先頭の Weight 0 はスキップされ、次の item が当選する
		},
		{
			// #12 と同じパス。pullCount の下限でも成立することを確認する。
			// 図のパスは同一だが、pullCount によって upsert の個数と履歴の件数が変わるため残す。
			name:           "正常系: pullCount が下限（1回）",
			pullCount:      gachadomain.MinPullCount,
			randValue:      0,
			wantDrawnIndex: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runMultiCase(t, tt)
		})
	}
}

// runMultiCase はテーブルの1行を実行する唯一の手続き。
// 対象の生成・モックの期待組み立て・呼び出し・アサーションはすべてここにある。
func runMultiCase(t *testing.T, tc multiCase) {
	t.Helper()

	ctrl := gomock.NewController(t)
	repo := mockuc.NewMockRepository(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)
	rnd := mockuc.NewMockRandomizer(ctrl)
	rnd.EXPECT().IntN(gomock.Any()).Return(tc.randValue).AnyTimes()

	items := tc.items
	if items == nil {
		items = newTestItems()
	}
	gems := tc.userGems
	if gems == 0 {
		gems = gachadomain.GemCostFor(gachadomain.MaxPullCount) + 500
	}

	expectMultiCalls(tc, repo, tx, items, gems)

	uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
	res, err := uc.Multi(context.Background(), testUserID, tc.pullCount)

	if tc.wantErrIs != nil {
		require.Error(t, err)
		assert.ErrorIs(t, err, tc.wantErrIs)
		assert.Equal(t, gacha.Result{}, res, "エラー時はゼロ値の Result を返す")
		return
	}

	require.NoError(t, err)
	assert.Equal(t, testUserID, res.UserID)
	require.Len(t, res.DrawnItems, tc.pullCount)
	wantItem := items[tc.wantDrawnIndex]
	for i, it := range res.DrawnItems {
		assert.Equal(t, wantItem.ID, it.ID, "%d 回目の当選アイテム", i+1)
	}
	assert.Equal(t, gems-gachadomain.GemCostFor(tc.pullCount), res.RemainingGems)
}

// expectMultiCalls は tc の「どこで失敗するか」に応じて、そこまでの呼び出しだけを期待に登録する。
//
// 期待していない呼び出しが起きれば gomock が失敗させるため、テスト仕様表の
// 「検証すべき呼び出し」列にある「後続が呼ばれないこと」の検証はここに含まれている。
func expectMultiCalls(tc multiCase, repo *mockuc.MockRepository, tx *mockshared.MockTransactor, items []gachadomain.Item, gems int) {
	// B: pullCount が不正なら DoInTx にも入らない
	if !gachadomain.IsValidPullCount(tc.pullCount) {
		return
	}

	// D: トランザクション境界
	if tc.failAt == stepBeginTx {
		tx.EXPECT().DoInTx(gomock.Any(), gomock.Any()).Return(tc.failErr)
		return
	}
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
			return fn(nil)
		})

	// F: GetUserForUpdate
	if tc.failAt == stepGetUser {
		repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), testUserID).Return(gachadomain.User{}, tc.failErr)
		return
	}
	user := gachadomain.User{ID: testUserID, GemNum: gems}
	repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), testUserID).Return(user, nil)

	// G: 石の充足判定（domain の判定結果を受けた分岐）
	if !user.HasEnoughGemsFor(tc.pullCount) {
		return
	}

	// H: ListItems
	if tc.failAt == stepListItems {
		repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(nil, tc.failErr)
		return
	}
	repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(items, nil)

	// I / J: items 空・重み不正はここで打ち切られる
	if len(items) == 0 || totalWeightOf(items) <= 0 {
		return
	}

	// K: UpdateUserGems
	newGems := gems - gachadomain.GemCostFor(tc.pullCount)
	if tc.failAt == stepUpdateGems {
		repo.EXPECT().UpdateUserGems(gomock.Any(), gomock.Any(), testUserID, newGems).Return(tc.failErr)
		return
	}
	repo.EXPECT().UpdateUserGems(gomock.Any(), gomock.Any(), testUserID, newGems).Return(nil)

	// L: 当選アイテムは固定なので、集約後は1種類 × pullCount 個になる
	wantItemID := items[tc.wantDrawnIndex].ID
	if tc.failAt == stepUpsertItem {
		repo.EXPECT().UpsertUserItem(gomock.Any(), gomock.Any(), testUserID, wantItemID, tc.pullCount).Return(tc.failErr)
		return
	}
	repo.EXPECT().UpsertUserItem(gomock.Any(), gomock.Any(), testUserID, wantItemID, tc.pullCount).Return(nil)

	// M: 履歴は抽選回数ぶん
	if tc.failAt == stepInsertHistory {
		// 1回目で失敗し、以降は呼ばれない
		repo.EXPECT().InsertGachaHistory(gomock.Any(), gomock.Any(), testUserID, wantItemID).Return(tc.failErr)
		return
	}
	repo.EXPECT().InsertGachaHistory(gomock.Any(), gomock.Any(), testUserID, wantItemID).Return(nil).Times(tc.pullCount)
}

func totalWeightOf(items []gachadomain.Item) int {
	total := 0
	for _, it := range items {
		if it.Weight > 0 {
			total += it.Weight
		}
	}
	return total
}

// TestUsecase_Multi_ロック取得順序 は、複数種類のアイテムが当選したときに
// UpsertUserItem が item_id の昇順で呼ばれることを検証する。
//
// この順序はデッドロック回避のための不変条件（usecase.go の Multi コメント参照）であり、
// 「抽選順」ではなく「ID 昇順」であることに意味がある。
// 抽選順と ID 順が一致してしまうと検証にならないため、
// 先に当選するアイテムの ID を大きくしてある。
//
// 乱数の戻り値を呼び出しごとに変える必要があり、他のケースと手続きが異なるため
// テーブルを分けている。
func TestUsecase_Multi_ロック取得順序_UpsertはItemID昇順で呼ばれる(t *testing.T) {
	t.Parallel()

	const pullCount = 2
	// ID 2 の重みを 60、ID 1 の重みを 40 にする。
	items := []gachadomain.Item{
		{ID: 2, Name: "先に当たる（ID 大）", Rarity: gachadomain.RarityN, Weight: 60},
		{ID: 1, Name: "後に当たる（ID 小）", Rarity: gachadomain.RarityR, Weight: 40},
	}

	ctrl := gomock.NewController(t)
	repo := mockuc.NewMockRepository(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)
	rnd := mockuc.NewMockRandomizer(ctrl)

	// 1回目は 0 → items[0]（ID 2）、2回目は 60 → items[1]（ID 1）が当選する。
	gomock.InOrder(
		rnd.EXPECT().IntN(gomock.Any()).Return(0),
		rnd.EXPECT().IntN(gomock.Any()).Return(60),
	)

	gems := gachadomain.GemCostFor(pullCount) + 100
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error { return fn(nil) })
	repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), testUserID).
		Return(gachadomain.User{ID: testUserID, GemNum: gems}, nil)
	repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(items, nil)
	repo.EXPECT().UpdateUserGems(gomock.Any(), gomock.Any(), testUserID, gems-gachadomain.GemCostFor(pullCount)).Return(nil)

	// 抽選順は ID 2 → ID 1 だが、upsert は ID 1 → ID 2 の昇順でなければならない。
	gomock.InOrder(
		repo.EXPECT().UpsertUserItem(gomock.Any(), gomock.Any(), testUserID, int64(1), gachadomain.GrantQuantity).Return(nil),
		repo.EXPECT().UpsertUserItem(gomock.Any(), gomock.Any(), testUserID, int64(2), gachadomain.GrantQuantity).Return(nil),
	)

	// 履歴は抽選順（ID 2 → ID 1）で積まれる。
	gomock.InOrder(
		repo.EXPECT().InsertGachaHistory(gomock.Any(), gomock.Any(), testUserID, int64(2)).Return(nil),
		repo.EXPECT().InsertGachaHistory(gomock.Any(), gomock.Any(), testUserID, int64(1)).Return(nil),
	)

	uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
	res, err := uc.Multi(context.Background(), testUserID, pullCount)

	require.NoError(t, err)
	require.Len(t, res.DrawnItems, pullCount)
	assert.Equal(t, int64(2), res.DrawnItems[0].ID, "1回目の当選は ID 2")
	assert.Equal(t, int64(1), res.DrawnItems[1].ID, "2回目の当選は ID 1")
}

// TestNewUsecase_Randomizerがnilなら既定実装を使う は NewUsecase の nil フォールバック分岐を検証する。
// 既定実装は乱数なので当選アイテムを固定できない。
// 「どれかが当選して完走する」ことだけを確認し、どれが当たるかは検証しない。
func TestNewUsecase_Randomizerがnilなら既定実装を使う(t *testing.T) {
	t.Parallel()

	const pullCount = gachadomain.MaxPullCount
	items := newTestItems()
	gems := gachadomain.GemCostFor(pullCount) + 100

	ctrl := gomock.NewController(t)
	repo := mockuc.NewMockRepository(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)

	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error { return fn(nil) })
	repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), testUserID).
		Return(gachadomain.User{ID: testUserID, GemNum: gems}, nil)
	repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(items, nil)
	repo.EXPECT().UpdateUserGems(gomock.Any(), gomock.Any(), testUserID, gems-gachadomain.GemCostFor(pullCount)).Return(nil)
	// 当選内訳は乱数依存なので、呼び出し回数の範囲だけを期待する。
	repo.EXPECT().UpsertUserItem(gomock.Any(), gomock.Any(), testUserID, gomock.Any(), gomock.Any()).
		Return(nil).MinTimes(1).MaxTimes(len(items))
	repo.EXPECT().InsertGachaHistory(gomock.Any(), gomock.Any(), testUserID, gomock.Any()).
		Return(nil).Times(pullCount)

	uc := gacha.NewUsecase(tx, repo, nil, slogtest.NewLogger(t, nil))
	res, err := uc.Multi(context.Background(), testUserID, pullCount)

	require.NoError(t, err)
	assert.Len(t, res.DrawnItems, pullCount)
}
