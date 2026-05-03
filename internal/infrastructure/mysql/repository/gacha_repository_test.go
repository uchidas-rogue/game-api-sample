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
