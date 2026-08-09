// Package gacha_test は gacha ユースケースの外部テストパッケージ。
// 公開 API のみを対象とし、モックを用いてデータベース接続をバイパスする。
package gacha_test

import (
	"context"
	"errors"
	"fmt"
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

// testItems はテスト用のアイテムリスト。
// 合計 weight = 100 (item1:60, item2:40) で、IntN(100) の戻り値によって
// 当選アイテムを決定論的に制御できる。
var testItems = []gachadomain.Item{
	{ID: 1, Name: "ノーマルソード", Rarity: gachadomain.RarityN, Weight: 60},
	{ID: 2, Name: "レアソード", Rarity: gachadomain.RarityR, Weight: 40},
}

// newDoInTxCaller は MockTransactor.DoInTx を実際に fn(nil) を呼び出す DoAndReturn として設定するヘルパー。
func newDoInTxCaller(tx *mockshared.MockTransactor) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
			return fn(nil)
		})
}

// newFixedRandomizer は毎回 fixedVal を返す MockRandomizer を設定するヘルパー。
// IntN は10連ガチャで10回呼ばれるため AnyTimes() で設定する。
func newFixedRandomizer(ctrl *gomock.Controller, fixedVal int) *mockuc.MockRandomizer {
	rnd := mockuc.NewMockRandomizer(ctrl)
	rnd.EXPECT().IntN(gomock.Any()).Return(fixedVal).AnyTimes()
	return rnd
}

func TestUsecase_Multi(t *testing.T) {
	t.Parallel()

	const (
		userID = int64(42)
	)
	enoughGem := gachadomain.GemCostFor(gachadomain.MaxPullCount) + 500 // 十分な石

	errDB := errors.New("db error")

	tests := []struct {
		name        string
		setup       func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context)
		wantErr     bool
		checkErr    func(t *testing.T, err error) // エラー内容の詳細検証（任意）
		checkResult func(t *testing.T, r gacha.Result)
	}{
		{
			name: "正常系: 10件抽選成功・石が消費・upsert と history が正しく呼ばれる",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				// IntN(100)=0 のとき acc=60>0 で item1 が当選
				rnd := newFixedRandomizer(ctrl, 0)

				newDoInTxCaller(tx)

				user := gachadomain.User{ID: userID, GemNum: enoughGem}
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(user, nil)
				repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(testItems, nil)
				// 石消費後の残高
				newGems := enoughGem - gachadomain.GemCostFor(gachadomain.MaxPullCount)
				repo.EXPECT().UpdateUserGems(gomock.Any(), gomock.Any(), userID, newGems).Return(nil)
				// 全 MaxPullCount 回 item1 当選 → upsert は item1 のみ
				repo.EXPECT().UpsertUserItem(gomock.Any(), gomock.Any(), userID, testItems[0].ID, gachadomain.MaxPullCount).Return(nil)
				// history は MaxPullCount 件
				repo.EXPECT().InsertGachaHistory(gomock.Any(), gomock.Any(), userID, testItems[0].ID).Return(nil).Times(gachadomain.MaxPullCount)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: false,
			checkResult: func(t *testing.T, r gacha.Result) {
				t.Helper()
				assert.Equal(t, userID, r.UserID)
				assert.Len(t, r.DrawnItems, gachadomain.MaxPullCount)
				assert.Equal(t, enoughGem-gachadomain.GemCostFor(gachadomain.MaxPullCount), r.RemainingGems)
				// 全アイテムが item1 であることを確認
				for _, it := range r.DrawnItems {
					assert.Equal(t, testItems[0].ID, it.ID)
				}
			},
		},
		{
			name: fmt.Sprintf("正常系: item2 が当選するケース（IntN=%d）", testItems[0].Weight),
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				// IntN=item1.Weight のとき acc=item1.Weight はまだ item1 の範囲外、
				// acc=item1.Weight+item2.Weight で item2 が当選
				rnd := newFixedRandomizer(ctrl, testItems[0].Weight)

				newDoInTxCaller(tx)

				user := gachadomain.User{ID: userID, GemNum: enoughGem}
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(user, nil)
				repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(testItems, nil)
				newGems := enoughGem - gachadomain.GemCostFor(gachadomain.MaxPullCount)
				repo.EXPECT().UpdateUserGems(gomock.Any(), gomock.Any(), userID, newGems).Return(nil)
				// 全 MaxPullCount 回 item2 当選
				repo.EXPECT().UpsertUserItem(gomock.Any(), gomock.Any(), userID, testItems[1].ID, gachadomain.MaxPullCount).Return(nil)
				repo.EXPECT().InsertGachaHistory(gomock.Any(), gomock.Any(), userID, testItems[1].ID).Return(nil).Times(gachadomain.MaxPullCount)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: false,
			checkResult: func(t *testing.T, r gacha.Result) {
				t.Helper()
				assert.Len(t, r.DrawnItems, gachadomain.MaxPullCount)
				for _, it := range r.DrawnItems {
					assert.Equal(t, testItems[1].ID, it.ID)
				}
			},
		},
		{
			name: "異常系: GetUserForUpdate がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := mockuc.NewMockRandomizer(ctrl)

				newDoInTxCaller(tx)
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(gachadomain.User{}, errDB)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errDB)
			},
		},
		{
			name: "異常系: ユーザー不在（ErrUserNotFound）— 後続処理は呼ばれない",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := mockuc.NewMockRandomizer(ctrl)

				newDoInTxCaller(tx)
				// infrastructure 層が sql.ErrNoRows を ErrUserNotFound にラップして返すケースを模す
				wrapped := fmt.Errorf("get user for update (id=%d): %w", userID, gachadomain.ErrUserNotFound)
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(gachadomain.User{}, wrapped)
				// ListItems 以降の呼び出しが発生しないことは MockRepository が未設定 EXPECT で検証する

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, gachadomain.ErrUserNotFound)
			},
		},
		{
			name: "異常系: 石不足（ErrInsufficientGems）",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := mockuc.NewMockRandomizer(ctrl)

				newDoInTxCaller(tx)
				// 石数が必要量より1少ない
				user := gachadomain.User{ID: userID, GemNum: gachadomain.GemCostFor(gachadomain.MaxPullCount) - 1}
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(user, nil)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, gachadomain.ErrInsufficientGems)
			},
		},
		{
			name: "異常系: ListItems がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := mockuc.NewMockRandomizer(ctrl)

				newDoInTxCaller(tx)
				user := gachadomain.User{ID: userID, GemNum: enoughGem}
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(user, nil)
				repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(nil, errDB)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errDB)
			},
		},
		{
			name: "異常系: items が空（ErrNoItemsAvailable）",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := mockuc.NewMockRandomizer(ctrl)

				newDoInTxCaller(tx)
				user := gachadomain.User{ID: userID, GemNum: enoughGem}
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(user, nil)
				repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return([]gachadomain.Item{}, nil)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, gachadomain.ErrNoItemsAvailable)
			},
		},
		{
			name: "異常系: 全アイテムの Weight が 0（ErrInvalidItemWeights）",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := mockuc.NewMockRandomizer(ctrl)

				newDoInTxCaller(tx)
				user := gachadomain.User{ID: userID, GemNum: enoughGem}
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(user, nil)
				// weight が全て 0 のアイテム
				zeroWeightItems := []gachadomain.Item{
					{ID: 10, Name: "無効アイテム", Rarity: gachadomain.RarityN, Weight: 0},
					{ID: 11, Name: "無効アイテム2", Rarity: gachadomain.RarityR, Weight: 0},
				}
				repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(zeroWeightItems, nil)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, gachadomain.ErrInvalidItemWeights)
			},
		},
		{
			name: "異常系: UpdateUserGems がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := newFixedRandomizer(ctrl, 0)

				newDoInTxCaller(tx)
				user := gachadomain.User{ID: userID, GemNum: enoughGem}
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(user, nil)
				repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(testItems, nil)
				newGems := enoughGem - gachadomain.GemCostFor(gachadomain.MaxPullCount)
				repo.EXPECT().UpdateUserGems(gomock.Any(), gomock.Any(), userID, newGems).Return(errDB)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errDB)
			},
		},
		{
			name: "異常系: UpsertUserItem がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := newFixedRandomizer(ctrl, 0)

				newDoInTxCaller(tx)
				user := gachadomain.User{ID: userID, GemNum: enoughGem}
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(user, nil)
				repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(testItems, nil)
				newGems := enoughGem - gachadomain.GemCostFor(gachadomain.MaxPullCount)
				repo.EXPECT().UpdateUserGems(gomock.Any(), gomock.Any(), userID, newGems).Return(nil)
				repo.EXPECT().UpsertUserItem(gomock.Any(), gomock.Any(), userID, testItems[0].ID, gachadomain.MaxPullCount).Return(errDB)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errDB)
			},
		},
		{
			name: "異常系: InsertGachaHistory がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := newFixedRandomizer(ctrl, 0)

				newDoInTxCaller(tx)
				user := gachadomain.User{ID: userID, GemNum: enoughGem}
				repo.EXPECT().GetUserForUpdate(gomock.Any(), gomock.Any(), userID).Return(user, nil)
				repo.EXPECT().ListItems(gomock.Any(), gomock.Any()).Return(testItems, nil)
				newGems := enoughGem - gachadomain.GemCostFor(gachadomain.MaxPullCount)
				repo.EXPECT().UpdateUserGems(gomock.Any(), gomock.Any(), userID, newGems).Return(nil)
				repo.EXPECT().UpsertUserItem(gomock.Any(), gomock.Any(), userID, testItems[0].ID, gachadomain.MaxPullCount).Return(nil)
				// 最初の1回目でエラー、以降は呼ばれないので MinTimes(1) で受け取る
				repo.EXPECT().InsertGachaHistory(gomock.Any(), gomock.Any(), userID, testItems[0].ID).Return(errDB)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errDB)
			},
		},
		{
			name: "異常系: Transactor.DoInTx 自体がエラー（fn が呼ばれない）",
			setup: func(t *testing.T, ctrl *gomock.Controller) (gacha.Usecase, context.Context) {
				repo := mockuc.NewMockRepository(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				rnd := mockuc.NewMockRandomizer(ctrl)

				// fn を呼ばずにエラーを返す（begin 失敗などを想定）
				tx.EXPECT().DoInTx(gomock.Any(), gomock.Any()).Return(errDB)

				uc := gacha.NewUsecase(tx, repo, rnd, slogtest.NewLogger(t, nil))
				return uc, context.Background()
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, errDB)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc, ctx := tt.setup(t, ctrl)

			result, err := uc.Multi(ctx, userID, gachadomain.MaxPullCount)

			if tt.wantErr {
				require.Error(t, err)
				if tt.checkErr != nil {
					tt.checkErr(t, err)
				}
				// エラー時は空の Result が返ること
				assert.Equal(t, gacha.Result{}, result)
			} else {
				require.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}
