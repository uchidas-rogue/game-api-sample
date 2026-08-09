// Package repository_test は RankingRepository の外部テスト。
package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/repository"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	mocksqlc "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc/mock"
)

func TestRankingRepository_GetGuild(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection refused")
	now := time.Now()

	tests := []struct {
		name      string
		stubGuild sqlc.Guild
		stubErr   error
		wantErr   error
	}{
		{
			name: "正常系: ギルド取得成功",
			stubGuild: sqlc.Guild{
				ID:        1,
				Name:      "テストギルド",
				CreatedAt: sql.NullTime{Time: now, Valid: true},
				UpdatedAt: sql.NullTime{Time: now, Valid: true},
			},
		},
		{
			name:    "異常系: sql.ErrNoRows は ErrGuildNotFound に変換される",
			stubErr: sql.ErrNoRows,
			wantErr: rankingdomain.ErrGuildNotFound,
		},
		{
			name:    "異常系: その他の DB エラーは原エラーをラップして返す",
			stubErr: errDB,
			wantErr: errDB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetGuild(gomock.Any(), int64(1)).Return(tt.stubGuild, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.GetGuild(context.Background(), dummyTx{}, 1)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, rankingdomain.Guild{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.stubGuild.ID, got.ID)
			assert.Equal(t, tt.stubGuild.Name, got.Name)
		})
	}
}

func TestRankingRepository_GetUser(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name     string
		stubUser sqlc.User
		stubErr  error
		wantErr  error
		wantName string
	}{
		{
			name:     "正常系: ユーザー名取得成功",
			stubUser: sqlc.User{ID: 1, Name: "プレイヤー1"},
			wantName: "プレイヤー1",
		},
		{
			name:    "異常系: sql.ErrNoRows は ErrUserNotFound に変換される",
			stubErr: sql.ErrNoRows,
			wantErr: rankingdomain.ErrUserNotFound,
		},
		{
			name:    "異常系: その他の DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: errDB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetUser(gomock.Any(), int64(1)).Return(tt.stubUser, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.GetUser(context.Background(), dummyTx{}, 1)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, got)
		})
	}
}

func TestRankingRepository_GetGuildScore(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	now := time.Now()

	tests := []struct {
		name      string
		stubScore sqlc.GuildScore
		stubErr   error
		wantErr   error
	}{
		{
			name: "正常系: ギルドスコア取得成功",
			stubScore: sqlc.GuildScore{
				GuildID:   1,
				Score:     1000,
				UpdatedAt: sql.NullTime{Time: now, Valid: true},
			},
		},
		{
			name:    "異常系: sql.ErrNoRows は ErrScoreNotFound に変換される",
			stubErr: sql.ErrNoRows,
			wantErr: rankingdomain.ErrScoreNotFound,
		},
		{
			name:    "異常系: その他の DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: errDB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetGuildScore(gomock.Any(), int64(1)).Return(tt.stubScore, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.GetGuildScore(context.Background(), dummyTx{}, 1)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, rankingdomain.GuildScore{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.stubScore.GuildID, got.GuildID)
			assert.Equal(t, tt.stubScore.Score, got.Score)
		})
	}
}

func TestRankingRepository_IncrementGuildScore(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{name: "正常系: スコア加算成功"},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().IncrementGuildScore(gomock.Any(), sqlc.IncrementGuildScoreParams{
				GuildID: int64(1),
				Score:   int64(100),
			}).Return(tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			err := repo.IncrementGuildScore(context.Background(), dummyTx{}, 1, 100)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRankingRepository_InsertGuildScoreHistory(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{name: "正常系: 履歴挿入成功"},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().InsertGuildScoreHistory(gomock.Any(), sqlc.InsertGuildScoreHistoryParams{
				GuildID: int64(1),
				UserID:  int64(2),
				Score:   int64(100),
			}).Return(tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			err := repo.InsertGuildScoreHistory(context.Background(), dummyTx{}, 1, 2, 100)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRankingRepository_IsUserInGuild(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name       string
		stubResult bool
		stubErr    error
		wantResult bool
		wantErr    bool
	}{
		{name: "正常系: メンバーである", stubResult: true, wantResult: true},
		{name: "正常系: メンバーでない", stubResult: false, wantResult: false},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().IsUserInGuild(gomock.Any(), sqlc.IsUserInGuildParams{
				GuildID: int64(1),
				UserID:  int64(2),
			}).Return(tt.stubResult, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.IsUserInGuild(context.Background(), dummyTx{}, 2, 1)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantResult, got)
			}
		})
	}
}

func TestRankingRepository_GetUserGuildID(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name        string
		stubGuildID int64
		stubErr     error
		wantErr     error
	}{
		{name: "正常系: ギルドID取得成功", stubGuildID: 5},
		{name: "異常系: sql.ErrNoRows は ErrUserNotInGuild に変換される", stubErr: sql.ErrNoRows, wantErr: rankingdomain.ErrUserNotInGuild},
		{name: "異常系: その他の DB エラーはラップして返す", stubErr: errDB, wantErr: errDB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetUserGuildID(gomock.Any(), int64(1)).Return(tt.stubGuildID, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.GetUserGuildID(context.Background(), dummyTx{}, 1)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.stubGuildID, got)
		})
	}
}

func TestRankingRepository_ListGuildsByIDs(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	now := time.Now()

	tests := []struct {
		name       string
		guildIDs   []int64
		stubGuilds []sqlc.Guild
		stubErr    error
		wantCount  int
		wantErr    bool
	}{
		{
			name:     "正常系: 複数ギルド取得",
			guildIDs: []int64{1, 2},
			stubGuilds: []sqlc.Guild{
				{ID: 1, Name: "ギルドA", CreatedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
				{ID: 2, Name: "ギルドB", CreatedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
			},
			wantCount: 2,
		},
		{
			name:       "正常系: 空リスト",
			guildIDs:   []int64{},
			stubGuilds: []sqlc.Guild{},
			wantCount:  0,
		},
		{
			name:     "異常系: DB エラーはラップして返す",
			guildIDs: []int64{1},
			stubErr:  errDB,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().ListGuildsByIDs(gomock.Any(), tt.guildIDs).Return(tt.stubGuilds, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.ListGuildsByIDs(context.Background(), dummyTx{}, tt.guildIDs)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestRankingRepository_ListAllGuildScores(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	now := time.Now()

	tests := []struct {
		name       string
		stubScores []sqlc.GuildScore
		stubErr    error
		wantCount  int
		wantErr    bool
	}{
		{
			name: "正常系: 複数スコア取得",
			stubScores: []sqlc.GuildScore{
				{GuildID: 1, Score: 100, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
				{GuildID: 2, Score: 200, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
			},
			wantCount: 2,
		},
		{
			name:       "正常系: 空リスト",
			stubScores: []sqlc.GuildScore{},
			wantCount:  0,
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
			mockQ.EXPECT().ListAllGuildScores(gomock.Any()).Return(tt.stubScores, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.ListAllGuildScores(context.Background(), dummyTx{})

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestRankingRepository_GetUserPoints(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	now := time.Now()

	tests := []struct {
		name      string
		stubPoint sqlc.UserPoint
		stubErr   error
		wantErr   error
	}{
		{
			name: "正常系: ユーザーポイント取得成功",
			stubPoint: sqlc.UserPoint{
				UserID:    1,
				Points:    500,
				UpdatedAt: sql.NullTime{Time: now, Valid: true},
			},
		},
		{
			name:    "異常系: sql.ErrNoRows は ErrPointsNotFound に変換される",
			stubErr: sql.ErrNoRows,
			wantErr: rankingdomain.ErrPointsNotFound,
		},
		{
			name:    "異常系: その他の DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: errDB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetUserPoints(gomock.Any(), int64(1)).Return(tt.stubPoint, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.GetUserPoints(context.Background(), dummyTx{}, 1)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, rankingdomain.UserPoint{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.stubPoint.UserID, got.UserID)
			assert.Equal(t, tt.stubPoint.Points, got.Points)
		})
	}
}

func TestRankingRepository_IncrementUserPoints(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{name: "正常系: ポイント加算成功"},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().IncrementUserPoints(gomock.Any(), sqlc.IncrementUserPointsParams{
				UserID: int64(1),
				Points: int64(200),
			}).Return(tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			err := repo.IncrementUserPoints(context.Background(), dummyTx{}, 1, 200)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRankingRepository_InsertUserPointHistory(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{name: "正常系: 履歴挿入成功"},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().InsertUserPointHistory(gomock.Any(), sqlc.InsertUserPointHistoryParams{
				UserID: int64(1),
				Points: int64(200),
				Reason: "battle",
			}).Return(tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			err := repo.InsertUserPointHistory(context.Background(), dummyTx{}, 1, 200, "battle")

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRankingRepository_ListUsersByIDs(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name      string
		userIDs   []int64
		stubUsers []sqlc.User
		stubErr   error
		wantCount int
		wantErr   bool
	}{
		{
			name:      "正常系: 複数ユーザー取得",
			userIDs:   []int64{1, 2},
			stubUsers: []sqlc.User{{ID: 1, Name: "ユーザーA"}, {ID: 2, Name: "ユーザーB"}},
			wantCount: 2,
		},
		{
			name:      "正常系: 空リスト",
			userIDs:   []int64{},
			stubUsers: []sqlc.User{},
			wantCount: 0,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			userIDs: []int64{1},
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().ListUsersByIDs(gomock.Any(), tt.userIDs).Return(tt.stubUsers, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.ListUsersByIDs(context.Background(), dummyTx{}, tt.userIDs)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
			for _, u := range tt.stubUsers {
				assert.Equal(t, u.Name, got[u.ID])
			}
		})
	}
}

func TestRankingRepository_ListAllUserPoints(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	now := time.Now()

	tests := []struct {
		name       string
		stubPoints []sqlc.UserPoint
		stubErr    error
		wantCount  int
		wantErr    bool
	}{
		{
			name: "正常系: 複数ポイント取得",
			stubPoints: []sqlc.UserPoint{
				{UserID: 1, Points: 100, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
				{UserID: 2, Points: 200, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
			},
			wantCount: 2,
		},
		{
			name:       "正常系: 空リスト",
			stubPoints: []sqlc.UserPoint{},
			wantCount:  0,
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
			mockQ.EXPECT().ListAllUserPoints(gomock.Any()).Return(tt.stubPoints, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.ListAllUserPoints(context.Background(), dummyTx{})

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
		})
	}
}
