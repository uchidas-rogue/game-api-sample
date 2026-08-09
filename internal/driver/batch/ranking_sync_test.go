// Package batch_test はバッチ処理の外部テストパッケージ。
//
// テスト設計（フロー図・テスト仕様表）は docs/testing/ranking-sync-batch.md にある。
// 分岐を追加・変更したら、まず図と表を更新してからここを直す。
package batch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/driver/batch"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockoutbox "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox/mock"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
	mockshared "github.com/uchidas-rogue/game-api-sample/internal/usecase/shared/mock"
)

type syncerDeps struct {
	repo       *mockranking.MockRepository
	outboxRepo *mockoutbox.MockRepository
	store      *mockranking.MockRankingStore
	tx         *mockshared.MockTransactor
}

func newSyncerDeps(ctrl *gomock.Controller) syncerDeps {
	return syncerDeps{
		repo:       mockranking.NewMockRepository(ctrl),
		outboxRepo: mockoutbox.NewMockRepository(ctrl),
		store:      mockranking.NewMockRankingStore(ctrl),
		tx:         mockshared.NewMockTransactor(ctrl),
	}
}

func (d syncerDeps) build(t *testing.T) *batch.RankingSyncer {
	t.Helper()
	return batch.NewRankingSyncer(d.repo, d.outboxRepo, d.store, d.tx, slogtest.NewLogger(t, nil))
}

// expectDoInTxRunsFn は DoInTx が fn を実際に実行する設定にする。
// トランザクション境界そのものの契約は docs/testing/transaction-boundary.md が
// Transactor 自身のテストで担保しているため、ここでは境界をモックしてよい。
func expectDoInTxRunsFn(tx *mockshared.MockTransactor) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
			return fn(nil)
		})
}

// syncStep は SyncAll のフロー上で失敗させる地点。
type syncStep int

const (
	syncStepNone syncStep = iota
	syncStepBeginTx
	syncStepGetMaxID
	syncStepListGuildScores
	syncStepListUserPoints
	syncStepMarkProcessed
	// syncStepRedisSet は COMMIT 後の Redis 反映が一部失敗する経路。
	// ログのみで処理を継続し、エラーは返さない。
	syncStepRedisSet
)

// syncCase は SyncAll のテストケース1件。データのみを持つ。
type syncCase struct {
	name string

	maxID       uint64
	guildScores []rankingdomain.GuildScore
	userPoints  []rankingdomain.UserPoint
	markedRows  int64

	failAt syncStep

	wantErr bool
}

func TestRankingSyncer_SyncAll(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection refused")

	guildScores := []rankingdomain.GuildScore{
		{GuildID: 1, Score: 100},
		{GuildID: 2, Score: 200},
	}
	userPoints := []rankingdomain.UserPoint{
		{UserID: 10, Points: 500},
		{UserID: 20, Points: 600},
	}

	// docs/testing/ranking-sync-batch.md のテスト仕様表と 1 対 1 で対応する。
	// 並び順は図のパスが短い順。
	tests := []syncCase{
		{
			// #1 A→B→E1
			name:    "DoInTx 自体が失敗: 内部処理も Redis SET も呼ばれない",
			failAt:  syncStepBeginTx,
			wantErr: true,
		},
		{
			// #2 A→B→C→E2
			name:    "GetMaxID が失敗: Redis SET が呼ばれない",
			failAt:  syncStepGetMaxID,
			wantErr: true,
		},
		{
			// #3 …→C→D→E3
			name:    "ListAllGuildScores が失敗",
			failAt:  syncStepListGuildScores,
			wantErr: true,
		},
		{
			// #4 …→D→E→E4
			name:    "ListAllUserPoints が失敗",
			failAt:  syncStepListUserPoints,
			wantErr: true,
		},
		{
			// #5 …→E→F→E5
			name:    "MarkProcessedUpTo が失敗: Redis SET が呼ばれない",
			failAt:  syncStepMarkProcessed,
			wantErr: true,
		},
		{
			// #6 …→F→G→Z（対象0件）
			name:        "正常系: 空テーブル（maxID=0）でも完了する",
			maxID:       0,
			guildScores: []rankingdomain.GuildScore{},
			userPoints:  []rankingdomain.UserPoint{},
			markedRows:  0,
		},
		{
			// #7 …→G→H→Z
			name:        "正常系: スナップショットが Redis に反映される",
			maxID:       42,
			guildScores: guildScores,
			userPoints:  userPoints,
			markedRows:  5,
		},
		{
			// #8 …→H→I→H→Z（Redis SET の一部が失敗）
			name:        "Redis SET の一部失敗はログのみで処理を継続しエラーを返さない",
			maxID:       42,
			guildScores: guildScores,
			userPoints:  userPoints,
			markedRows:  5,
			failAt:      syncStepRedisSet,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			d := newSyncerDeps(ctrl)
			expectSyncCalls(d, tt, errDB)

			err := d.build(t).SyncAll(context.Background())

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB, "原因のエラーを errors.Is で辿れること")
				return
			}
			require.NoError(t, err)
		})
	}
}

// expectSyncCalls は tc の「どこで失敗するか」に応じて、そこまでの呼び出しだけを期待に登録する。
// 期待していない呼び出しが起きれば gomock が失敗させるため、
// 「Redis SET が呼ばれないこと」の検証はここに含まれている。
func expectSyncCalls(d syncerDeps, tc syncCase, errDB error) {
	// B: トランザクション境界
	if tc.failAt == syncStepBeginTx {
		d.tx.EXPECT().DoInTx(gomock.Any(), gomock.Any()).Return(errDB)
		return
	}
	expectDoInTxRunsFn(d.tx)

	// C: スナップショット境界となる outbox の最大 ID
	if tc.failAt == syncStepGetMaxID {
		d.outboxRepo.EXPECT().GetMaxID(gomock.Any(), gomock.Any()).Return(uint64(0), errDB)
		return
	}
	d.outboxRepo.EXPECT().GetMaxID(gomock.Any(), gomock.Any()).Return(tc.maxID, nil)

	// D: ギルドスコア一覧
	if tc.failAt == syncStepListGuildScores {
		d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).Return(nil, errDB)
		return
	}
	d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).Return(tc.guildScores, nil)

	// E: ユーザーポイント一覧
	if tc.failAt == syncStepListUserPoints {
		d.repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).Return(nil, errDB)
		return
	}
	d.repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).Return(tc.userPoints, nil)

	// F: 境界までの pending イベントを processed にする。
	// event_type を明示して ranking 以外のイベントを巻き添えにしないことを検証する。
	if tc.failAt == syncStepMarkProcessed {
		d.outboxRepo.EXPECT().
			MarkProcessedUpTo(gomock.Any(), gomock.Any(), tc.maxID, outboxdomain.EventTypeRankingScoreAdded).
			Return(int64(0), errDB)
		return
	}
	d.outboxRepo.EXPECT().
		MarkProcessedUpTo(gomock.Any(), gomock.Any(), tc.maxID, outboxdomain.EventTypeRankingScoreAdded).
		Return(tc.markedRows, nil)

	// G / H: COMMIT 後の Redis 反映。
	// 個別 SET の失敗はログのみで継続するため、1件目を失敗させても全件が呼ばれる。
	errRedis := errors.New("redis error")
	for i, gs := range tc.guildScores {
		call := d.store.EXPECT().SetGuildScore(gomock.Any(), gs.GuildID, gs.Score)
		if tc.failAt == syncStepRedisSet && i == 0 {
			call.Return(errRedis)
			continue
		}
		call.Return(nil)
	}
	for i, up := range tc.userPoints {
		call := d.store.EXPECT().SetUserPoints(gomock.Any(), up.UserID, up.Points)
		if tc.failAt == syncStepRedisSet && i == 0 {
			call.Return(errRedis)
			continue
		}
		call.Return(nil)
	}
}
