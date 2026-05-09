// Package repository_test は GachaRepository の外部テスト。
// sqlc.Querier をモック注入し、infrastructure 層が DB 固有エラーを domain エラーに
// 変換する責務を網羅的に検証する。
package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/repository"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	mocksqlc "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc/mock"
)

// dummyTx は usecase.Tx を満たすだけのスタブ。
// テスト用の querier ファクトリは tx を参照しないため、中身は空で良い。
type dummyTx struct{}

func (dummyTx) IsTx() {}

func TestGachaRepository_GetUserForUpdate(t *testing.T) {
	t.Parallel()

	const userID = int64(42)
	errOther := errors.New("connection refused")

	tests := []struct {
		name      string
		stubErr   error
		stubUser  sqlc.User
		wantErrIs error // errors.Is で判定すべきエラー（nil なら正常系）
		wantUser  gachadomain.User
	}{
		{
			name:    "正常系: ユーザー取得成功",
			stubErr: nil,
			stubUser: sqlc.User{
				ID:     userID,
				Name:   "tester",
				GemNum: 1000,
			},
			wantUser: gachadomain.User{ID: userID, Name: "tester", GemNum: 1000},
		},
		{
			name:      "異常系: sql.ErrNoRows は ErrUserNotFound にラップされる",
			stubErr:   sql.ErrNoRows,
			wantErrIs: gachadomain.ErrUserNotFound,
		},
		{
			name:      "異常系: その他の DB エラーは原エラーをラップして返す（ErrUserNotFound にはならない）",
			stubErr:   errOther,
			wantErrIs: errOther,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetUserForUpdate(gomock.Any(), userID).Return(tt.stubUser, tt.stubErr)

			repo := repository.NewGachaRepositoryWithQuerier(mockQ)
			got, err := repo.GetUserForUpdate(context.Background(), dummyTx{}, userID)

			if tt.wantErrIs != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErrIs)
				if errors.Is(tt.wantErrIs, gachadomain.ErrUserNotFound) {
					// sql.ErrNoRows は infrastructure 層で吸収され、上位層には透過しない
					assert.NotErrorIs(t, err, sql.ErrNoRows)
				}
				assert.Equal(t, gachadomain.User{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantUser.ID, got.ID)
			assert.Equal(t, tt.wantUser.Name, got.Name)
			assert.Equal(t, tt.wantUser.GemNum, got.GemNum)
		})
	}
}

func TestGachaRepository_GetUserForUpdate_RequiresTx(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	mockQ := mocksqlc.NewMockQuerier(ctrl)
	// tx == nil の段階でエラー return されるため、Querier は呼ばれないことも検証する
	// （未設定 EXPECT が呼ばれたら gomock がテスト失敗させる）

	repo := repository.NewGachaRepositoryWithQuerier(mockQ)
	_, err := repo.GetUserForUpdate(context.Background(), nil, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transaction is required")
}

func TestGachaRepository_UpdateUserGems(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection refused")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{
			name:    "正常系: 更新成功",
			stubErr: nil,
			wantErr: false,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().UpdateUserGems(gomock.Any(), sqlc.UpdateUserGemsParams{
				GemNum: int32(500),
				ID:     int64(1),
			}).Return(tt.stubErr)

			repo := repository.NewGachaRepositoryWithQuerier(mockQ)
			err := repo.UpdateUserGems(context.Background(), dummyTx{}, 1, 500)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGachaRepository_ListItems(t *testing.T) {
	t.Parallel()

	errDB := errors.New("query failed")

	tests := []struct {
		name      string
		stubRows  []sqlc.Item
		stubErr   error
		wantItems int
		wantErr   bool
	}{
		{
			name:      "正常系: 複数件取得",
			stubRows:  []sqlc.Item{{ID: 1, Name: "剣", Rarity: 2, Weight: 50}, {ID: 2, Name: "盾", Rarity: 3, Weight: 30}},
			wantItems: 2,
		},
		{
			name:      "正常系: 空の結果",
			stubRows:  []sqlc.Item{},
			wantItems: 0,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().ListItems(gomock.Any()).Return(tt.stubRows, tt.stubErr)

			repo := repository.NewGachaRepositoryWithQuerier(mockQ)
			got, err := repo.ListItems(context.Background(), dummyTx{})

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantItems)
			for i, item := range got {
				assert.Equal(t, tt.stubRows[i].ID, item.ID)
				assert.Equal(t, tt.stubRows[i].Name, item.Name)
				assert.Equal(t, int(tt.stubRows[i].Rarity), item.Rarity)
				assert.Equal(t, int(tt.stubRows[i].Weight), item.Weight)
			}
		})
	}
}

func TestGachaRepository_UpsertUserItem(t *testing.T) {
	t.Parallel()

	errDB := errors.New("upsert failed")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{
			name:    "正常系: アップサート成功",
			stubErr: nil,
			wantErr: false,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().UpsertUserItem(gomock.Any(), sqlc.UpsertUserItemParams{
				UserID: int64(10),
				ItemID: int64(20),
				Num:    int32(3),
			}).Return(tt.stubErr)

			repo := repository.NewGachaRepositoryWithQuerier(mockQ)
			err := repo.UpsertUserItem(context.Background(), dummyTx{}, 10, 20, 3)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGachaRepository_InsertGachaHistory(t *testing.T) {
	t.Parallel()

	errDB := errors.New("insert failed")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{
			name:    "正常系: 履歴挿入成功",
			stubErr: nil,
			wantErr: false,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().InsertGachaHistory(gomock.Any(), sqlc.InsertGachaHistoryParams{
				UserID: int64(10),
				ItemID: int64(20),
			}).Return(tt.stubErr)

			repo := repository.NewGachaRepositoryWithQuerier(mockQ)
			err := repo.InsertGachaHistory(context.Background(), dummyTx{}, 10, 20)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
