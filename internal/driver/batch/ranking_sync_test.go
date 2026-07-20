// Package batch_test はバッチ処理の外部テストパッケージ。
package batch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/uchidas-rogue/game-api-sample/internal/driver/batch"
	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
	mockshared "github.com/uchidas-rogue/game-api-sample/internal/usecase/shared/mock"
)

// invokeDoInTx は MockTransactor.DoInTx を fn(nil) で実際に実行するヘルパー。
func invokeDoInTx(tx *mockshared.MockTransactor) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
			return fn(nil)
		})
}

type syncerDeps struct {
	repo  *mockranking.MockRepository
	store *mockranking.MockRankingStore
	tx    *mockshared.MockTransactor
}

func newSyncerDeps(ctrl *gomock.Controller) syncerDeps {
	return syncerDeps{
		repo:  mockranking.NewMockRepository(ctrl),
		store: mockranking.NewMockRankingStore(ctrl),
		tx:    mockshared.NewMockTransactor(ctrl),
	}
}

func (d syncerDeps) build(t *testing.T) *batch.RankingSyncer {
	t.Helper()
	return batch.NewRankingSyncer(d.repo, d.store, d.tx, slogtest.NewLogger(t, nil))
}

func TestRankingSyncer_SyncAll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(t *testing.T, ctrl *gomock.Controller) *batch.RankingSyncer
		wantErr bool
	}{
		{
			name: "正常系: ListAll* で取得したスナップショットが Redis に反映される",
			setup: func(t *testing.T, ctrl *gomock.Controller) *batch.RankingSyncer {
				d := newSyncerDeps(ctrl)
				invokeDoInTx(d.tx)

				gomock.InOrder(
					d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
						Return([]rankingdomain.GuildScore{{GuildID: 1, Score: 9000}}, nil),
					d.repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
						Return([]rankingdomain.UserPoint{{UserID: 10, Points: 5000}}, nil),
				)
				d.store.EXPECT().SetGuildScore(gomock.Any(), int64(1), int64(9000)).Return(nil)
				d.store.EXPECT().SetUserPoints(gomock.Any(), int64(10), int64(5000)).Return(nil)
				return d.build(t)
			},
			wantErr: false,
		},
		{
			name: "正常系: 空テーブルでも正常に完了する",
			setup: func(t *testing.T, ctrl *gomock.Controller) *batch.RankingSyncer {
				d := newSyncerDeps(ctrl)
				invokeDoInTx(d.tx)

				d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.GuildScore{}, nil)
				d.repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.UserPoint{}, nil)
				return d.build(t)
			},
			wantErr: false,
		},
		{
			name: "異常系: ListAllGuildScores 失敗時はエラーを返す",
			setup: func(t *testing.T, ctrl *gomock.Controller) *batch.RankingSyncer {
				d := newSyncerDeps(ctrl)
				invokeDoInTx(d.tx)

				d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return(nil, errors.New("db error"))
				// ListAllUserPoints / Redis SET は呼ばれないことを strict マッチで担保
				return d.build(t)
			},
			wantErr: true,
		},
		{
			name: "異常系: ListAllUserPoints 失敗時はエラーを返す",
			setup: func(t *testing.T, ctrl *gomock.Controller) *batch.RankingSyncer {
				d := newSyncerDeps(ctrl)
				invokeDoInTx(d.tx)

				gomock.InOrder(
					d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
						Return([]rankingdomain.GuildScore{}, nil),
					d.repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
						Return(nil, errors.New("db error")),
				)
				return d.build(t)
			},
			wantErr: true,
		},
		{
			name: "異常系: DoInTx 自体が失敗（BEGIN 失敗）時は内部処理が呼ばれずエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) *batch.RankingSyncer {
				d := newSyncerDeps(ctrl)
				d.tx.EXPECT().
					DoInTx(gomock.Any(), gomock.Any()).
					Return(errors.New("begin failed"))
				// repo / store の EXPECT は仕込まない（呼ばれないことを strict マッチで担保）
				return d.build(t)
			},
			wantErr: true,
		},
		{
			name: "エッジケース: SetGuildScore / SetUserPoints の一部失敗はログのみで処理を継続しエラーを返さない",
			setup: func(t *testing.T, ctrl *gomock.Controller) *batch.RankingSyncer {
				d := newSyncerDeps(ctrl)
				invokeDoInTx(d.tx)

				d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.GuildScore{
						{GuildID: 1, Score: 100},
						{GuildID: 2, Score: 200},
					}, nil)
				d.repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).
					Return([]rankingdomain.UserPoint{
						{UserID: 10, Points: 500},
						{UserID: 20, Points: 600},
					}, nil)

				d.store.EXPECT().SetGuildScore(gomock.Any(), int64(1), int64(100)).
					Return(errors.New("redis error"))
				d.store.EXPECT().SetGuildScore(gomock.Any(), int64(2), int64(200)).Return(nil)
				d.store.EXPECT().SetUserPoints(gomock.Any(), int64(10), int64(500)).
					Return(errors.New("redis error"))
				d.store.EXPECT().SetUserPoints(gomock.Any(), int64(20), int64(600)).Return(nil)
				return d.build(t)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			syncer := tt.setup(t, ctrl)

			err := syncer.SyncAll(context.Background())

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestRankingSyncer_SyncAll_エラーラップ は SyncAll が返すエラーが errors.Is で原因を辿れるか検証する。
func TestRankingSyncer_SyncAll_エラーラップ(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newSyncerDeps(ctrl)
	invokeDoInTx(d.tx)

	errDB := errors.New("connection refused")
	d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).Return(nil, errDB)

	syncer := d.build(t)
	err := syncer.SyncAll(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, errDB)
}
