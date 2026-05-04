// Package batch_test はバッチ処理の外部テストパッケージ。
package batch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/uchidas-rogue/game-api-sample/internal/batch"
	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
)

func TestRankingSyncer_SyncAll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(ctrl *gomock.Controller) *batch.RankingSyncer
		wantErr bool
	}{
		{
			name: "正常系: ギルドとユーザー両方の同期が成功する",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.GuildScore{
						{GuildID: 1, Score: 9000},
					}, nil)
				store.EXPECT().SetGuildScore(gomock.Any(), int64(1), int64(9000)).Return(nil)

				repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.UserPoint{
						{UserID: 10, Points: 5000},
					}, nil)
				store.EXPECT().SetUserPoints(gomock.Any(), int64(10), int64(5000)).Return(nil)

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
		{
			name: "異常系: SyncGuildRankings がエラーの場合は SyncAll がエラーを返す",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				errDB := errors.New("db error")
				repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return(nil, errDB)

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name: "異常系: SyncUserRankings がエラーの場合は SyncAll がエラーを返す",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.GuildScore{}, nil)
				repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
					Return(nil, errors.New("db error"))

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			syncer := tt.setup(ctrl)

			err := syncer.SyncAll(context.Background())

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRankingSyncer_SyncGuildRankings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(ctrl *gomock.Controller) *batch.RankingSyncer
		wantErr bool
		check   func(t *testing.T)
	}{
		{
			name: "正常系: 複数ギルドを同期する",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.GuildScore{
						{GuildID: 1, Score: 9000},
						{GuildID: 2, Score: 7000},
						{GuildID: 3, Score: 5000},
					}, nil)
				store.EXPECT().SetGuildScore(gomock.Any(), int64(1), int64(9000)).Return(nil)
				store.EXPECT().SetGuildScore(gomock.Any(), int64(2), int64(7000)).Return(nil)
				store.EXPECT().SetGuildScore(gomock.Any(), int64(3), int64(5000)).Return(nil)

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
		{
			name: "正常系: スコアが空の場合は SetGuildScore を呼ばない",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.GuildScore{}, nil)

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
		{
			name: "異常系: ListAllGuildScores がエラーの場合はエラーを返す",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				errDB := errors.New("db error")
				repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return(nil, errDB)

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name: "エッジケース: SetGuildScore が一部失敗しても処理を継続してエラーを返さない（ログのみ）",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.GuildScore{
						{GuildID: 1, Score: 9000},
						{GuildID: 2, Score: 7000},
					}, nil)
				// ギルド1は失敗
				store.EXPECT().SetGuildScore(gomock.Any(), int64(1), int64(9000)).
					Return(errors.New("redis error"))
				// ギルド2は成功（処理が継続されることを確認）
				store.EXPECT().SetGuildScore(gomock.Any(), int64(2), int64(7000)).
					Return(nil)

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			// SetGuildScore の失敗はログに記録されるが、SyncGuildRankings 自体はエラーを返さない
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			syncer := tt.setup(ctrl)

			err := syncer.SyncGuildRankings(context.Background())

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRankingSyncer_SyncUserRankings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(ctrl *gomock.Controller) *batch.RankingSyncer
		wantErr bool
	}{
		{
			name: "正常系: 複数ユーザーを同期する",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.UserPoint{
						{UserID: 10, Points: 8000},
						{UserID: 20, Points: 6000},
					}, nil)
				store.EXPECT().SetUserPoints(gomock.Any(), int64(10), int64(8000)).Return(nil)
				store.EXPECT().SetUserPoints(gomock.Any(), int64(20), int64(6000)).Return(nil)

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
		{
			name: "正常系: ポイントが空の場合は SetUserPoints を呼ばない",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.UserPoint{}, nil)

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
		{
			name: "異常系: ListAllUserPoints がエラーの場合はエラーを返す",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
					Return(nil, errors.New("db error"))

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name: "エッジケース: SetUserPoints が一部失敗しても処理を継続してエラーを返さない（ログのみ）",
			setup: func(ctrl *gomock.Controller) *batch.RankingSyncer {
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)

				repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.UserPoint{
						{UserID: 10, Points: 8000},
						{UserID: 20, Points: 6000},
					}, nil)
				store.EXPECT().SetUserPoints(gomock.Any(), int64(10), int64(8000)).
					Return(errors.New("redis error"))
				// ユーザー20は成功（処理が継続されることを確認）
				store.EXPECT().SetUserPoints(gomock.Any(), int64(20), int64(6000)).
					Return(nil)

				return batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			syncer := tt.setup(ctrl)

			err := syncer.SyncUserRankings(context.Background())

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestRankingSyncer_SyncAll_エラーメッセージのラップ は SyncAll が返すエラーに適切なコンテキストが付与されるか検証する。
func TestRankingSyncer_SyncAll_エラーメッセージのラップ(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	repo := mockranking.NewMockRepository(ctrl)
	store := mockranking.NewMockRankingStore(ctrl)

	errDB := errors.New("connection refused")
	repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).Return(nil, errDB)

	syncer := batch.NewRankingSyncer(repo, store, slogtest.NewLogger(t, nil))
	err := syncer.SyncAll(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, errDB)
}
