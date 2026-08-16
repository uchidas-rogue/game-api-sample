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

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/driver/batch"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
	mockshared "github.com/uchidas-rogue/game-api-sample/internal/usecase/shared/mock"
)

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

// expectDoInTxRunsFn は DoInTx が fn を実際に実行する設定にする。
// トランザクション境界そのものの契約は docs/testing/transaction-boundary.md が
// Transactor 自身のテストで担保しているため、ここでは境界をモックしてよい。
func expectDoInTxRunsFn(tx *mockshared.MockTransactor) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error, _ ...shared.TxOption) error {
			return fn(nil)
		})
}

// syncStep は SyncAll のフロー上で失敗させる地点。
type syncStep int

const (
	syncStepNone syncStep = iota
	syncStepBeginTx
	syncStepListGuildScores
	syncStepListUserPoints
	// syncStepRedisSet は COMMIT 後の Redis 反映が一部失敗する経路。
	// ループは継続する（残り全件を反映する）が、最後にエラーを返す。
	syncStepRedisSet
	// syncStepMarkInitialized は全件反映後のセンチネルキー設定が失敗する経路。
	syncStepMarkInitialized
)

// syncCase は SyncAll のテストケース1件。データのみを持つ。
type syncCase struct {
	name string

	guildScores []rankingdomain.GuildScore
	userPoints  []rankingdomain.UserPoint

	failAt syncStep

	// wantErrIs は期待するエラーの原因。nil なら正常終了を期待する。
	wantErrIs error
}

func TestRankingSyncer_SyncAll(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection refused")
	errRedis := errors.New("redis error")

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
			name:      "DoInTx 自体が失敗: 内部処理も Redis SET も呼ばれない",
			failAt:    syncStepBeginTx,
			wantErrIs: errDB,
		},
		{
			// #2 A→B→C→E2
			name:      "ListAllGuildScores が失敗: Redis SET が呼ばれない",
			failAt:    syncStepListGuildScores,
			wantErrIs: errDB,
		},
		{
			// #3 …→C→D→E3
			name:      "ListAllUserPoints が失敗: Redis SET が呼ばれない",
			failAt:    syncStepListUserPoints,
			wantErrIs: errDB,
		},
		{
			// #4 …→D→E→F→H→I→E5
			name:        "MarkInitialized が失敗: SET は全件済みでもエラーを返す",
			guildScores: guildScores,
			userPoints:  userPoints,
			failAt:      syncStepMarkInitialized,
			wantErrIs:   errRedis,
		},
		{
			// #5 …→D→E→F→H→I→Z（対象0件）
			name:        "正常系: 空テーブルでも完了する",
			guildScores: []rankingdomain.GuildScore{},
			userPoints:  []rankingdomain.UserPoint{},
		},
		{
			// #6 …→E→F→H→I→Z
			name:        "正常系: スナップショットが Redis に反映される",
			guildScores: guildScores,
			userPoints:  userPoints,
		},
		{
			// #7 …→F→G→F→H→E4（Redis SET の一部が失敗）
			// 部分復旧のままセンチネルキーを立てると、読み取りが欠けたランキングを
			// 正常なものとして返し始める。MarkInitialized を EXPECT に登録しないことで
			// 「呼ばれないこと」を gomock に検証させている。
			name:        "Redis SET の一部が失敗: 残り全件を反映したうえでエラーを返す",
			guildScores: guildScores,
			userPoints:  userPoints,
			failAt:      syncStepRedisSet,
			wantErrIs:   errRedis,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			d := newSyncerDeps(ctrl)
			expectSyncCalls(d, tt, errDB, errRedis)

			err := d.build(t).SyncAll(context.Background())

			if tt.wantErrIs != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErrIs, "原因のエラーを errors.Is で辿れること")
				return
			}
			require.NoError(t, err)
		})
	}
}

// expectSyncCalls は tc の「どこで失敗するか」に応じて、そこまでの呼び出しだけを期待に登録する。
// 期待していない呼び出しが起きれば gomock が失敗させるため、
// 「Redis SET が呼ばれないこと」の検証はここに含まれている。
func expectSyncCalls(d syncerDeps, tc syncCase, errDB, errRedis error) {
	// B: トランザクション境界
	if tc.failAt == syncStepBeginTx {
		d.tx.EXPECT().DoInTx(gomock.Any(), gomock.Any()).Return(errDB)
		return
	}
	expectDoInTxRunsFn(d.tx)

	// C: ギルドスコア一覧
	if tc.failAt == syncStepListGuildScores {
		d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).Return(nil, errDB)
		return
	}
	d.repo.EXPECT().ListAllGuildScores(gomock.Any(), gomock.Any()).Return(tc.guildScores, nil)

	// D: ユーザーポイント一覧
	if tc.failAt == syncStepListUserPoints {
		d.repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).Return(nil, errDB)
		return
	}
	d.repo.EXPECT().ListAllUserPoints(gomock.Any(), gomock.Any()).Return(tc.userPoints, nil)

	// E / F: COMMIT 後の Redis 反映。
	// 個別 SET が失敗してもループは継続するため、1件目を失敗させても全件が呼ばれる。
	// 「打ち切らない」ことの検証は、全件ぶんの EXPECT が消化されるかで担保している。
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

	// H / I: 全件成功したときだけセンチネルキーを立てる。
	if tc.failAt == syncStepRedisSet {
		return
	}
	if tc.failAt == syncStepMarkInitialized {
		d.store.EXPECT().MarkInitialized(gomock.Any()).Return(errRedis)
		return
	}
	d.store.EXPECT().MarkInitialized(gomock.Any()).Return(nil)
}
