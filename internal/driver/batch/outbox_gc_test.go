package batch_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/uchidas-rogue/game-api-sample/internal/driver/batch"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockoutbox "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
	mockshared "github.com/uchidas-rogue/game-api-sample/internal/usecase/shared/mock"
)

// gcStep は Run のフロー上で失敗させる地点。
type gcStep int

const (
	gcStepNone gcStep = iota
	gcStepBeginTx
	gcStepDelete
	// gcStepCtxCanceled はチャンクが満杯のまま ctx が死んでいる経路。
	gcStepCtxCanceled
)

// gcCase は Run のテストケース1件。データのみを持つ。
type gcCase struct {
	name string

	// deletedPerChunk は各チャンクの削除件数を順に返す台本。
	// batch.OutboxGCChunkSize と同じ値なら「満杯」= 次のチャンクへ進む。
	deletedPerChunk []int64

	failAt gcStep

	// wantErrIs は期待するエラーの原因。nil なら正常終了を期待する。
	wantErrIs error
	// wantCalls は DeleteProcessedBefore が呼ばれる回数。
	wantCalls int
}

func TestOutboxGC_Run(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection refused")
	full := int64(batch.OutboxGCChunkSize)

	// docs/testing/outbox-gc.md のテスト仕様表と 1 対 1 で対応する。
	// 並び順は図のパスが短い順。
	tests := []gcCase{
		{
			// #1 A→B→E1
			name:      "DoInTx 自体が失敗: 削除が呼ばれない",
			failAt:    gcStepBeginTx,
			wantErrIs: errDB,
			wantCalls: 0,
		},
		{
			// #2 A→B→C→E2
			name:      "削除が失敗: 2 チャンク目が呼ばれない",
			failAt:    gcStepDelete,
			wantErrIs: errDB,
			wantCalls: 1,
		},
		{
			// #3 …→C→D→F→Z
			name:            "対象なし: 削除は1回だけで終わる",
			deletedPerChunk: []int64{0},
			wantCalls:       1,
		},
		{
			// #4 …→F→Z
			name:            "端数で終わる: 削除は1回だけで終わる",
			deletedPerChunk: []int64{full - 1},
			wantCalls:       1,
		},
		{
			// #5 …→F→G→B→…→F→Z
			name:            "チャンク満杯なら次のチャンクへ進み、端数で終わる",
			deletedPerChunk: []int64{full, full - 1},
			wantCalls:       2,
		},
		{
			// #6 …→F→G→E3
			// チャンクが満杯なので通常なら次へ進むが、ctx が死んでいるので進まない。
			name:            "チャンク満杯のまま ctx が死んでいたら次のチャンクへ進まない",
			deletedPerChunk: []int64{full},
			failAt:          gcStepCtxCanceled,
			wantErrIs:       context.Canceled,
			wantCalls:       1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			repo := mockoutbox.NewMockRepository(ctrl)
			tx := mockshared.NewMockTransactor(ctrl)

			calls := expectGCCalls(t, tx, repo, tt, errDB)

			ctx := context.Background()
			if tt.failAt == gcStepCtxCanceled {
				// 実運用では次の BeginTx も失敗するが、ここでは
				// 「無駄なトランザクションを張る前に打ち切る」歯止めの側を通す。
				canceled, cancel := context.WithCancel(context.Background())
				cancel()
				ctx = canceled
			}

			gc := batch.NewOutboxGC(repo, tx, testRetention, slogtest.NewLogger(t, nil))
			err := gc.Run(ctx)

			if tt.wantErrIs != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErrIs, "原因のエラーを errors.Is で辿れること")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCalls, *calls, "DeleteProcessedBefore の呼び出し回数")
		})
	}
}

// testRetention は保持期間。値そのものは SQL 側の解釈で、driver は透過するだけ。
const testRetention = 72 * time.Hour

// expectGCCalls は tc の台本どおりにモックを設定し、削除呼び出し回数のカウンタを返す。
// 期待していない呼び出しが起きれば gomock が失敗させるため、
// 「打ち切られること」の検証はここに含まれている。
func expectGCCalls(
	t *testing.T,
	tx *mockshared.MockTransactor,
	repo *mockoutbox.MockRepository,
	tc gcCase,
	errDB error,
) *int {
	t.Helper()

	calls := 0

	// B: トランザクション境界
	if tc.failAt == gcStepBeginTx {
		tx.EXPECT().
			DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, _ func(shared.Tx) error, opts ...shared.TxOption) error {
				assertReadCommitted(t, opts)
				return errDB
			})
		return &calls
	}
	// チャンクごとに1回ずつ境界に入る。
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error, opts ...shared.TxOption) error {
			assertReadCommitted(t, opts)
			return fn(nil)
		}).
		Times(chunkCount(tc))

	// C: 削除
	if tc.failAt == gcStepDelete {
		repo.EXPECT().
			DeleteProcessedBefore(gomock.Any(), gomock.Any(), testRetention, int32(batch.OutboxGCChunkSize)).
			DoAndReturn(func(context.Context, shared.Tx, time.Duration, int32) (int64, error) {
				calls++
				return 0, errDB
			})
		return &calls
	}

	for _, deleted := range tc.deletedPerChunk {
		repo.EXPECT().
			DeleteProcessedBefore(gomock.Any(), gomock.Any(), testRetention, int32(batch.OutboxGCChunkSize)).
			DoAndReturn(func(context.Context, shared.Tx, time.Duration, int32) (int64, error) {
				calls++
				return deleted, nil
			})
	}
	return &calls
}

// assertReadCommitted は全チャンクの境界が READ COMMITTED で開かれることを検証する。
// 既定の REPEATABLE READ に戻ると idx_outbox_events_pending のギャップロックで
// API 側の outbox INSERT を止めるため、性能上の必須条件（outbox_gc.go の Run 参照）。
func assertReadCommitted(t *testing.T, opts []shared.TxOption) {
	t.Helper()
	assert.Equal(t, shared.IsolationReadCommitted, shared.NewTxOptions(opts...).Isolation,
		"GC の tx は READ COMMITTED で開始すること")
}

// chunkCount は台本から DoInTx が呼ばれる回数を求める。
func chunkCount(tc gcCase) int {
	if tc.failAt == gcStepDelete {
		return 1
	}
	return len(tc.deletedPerChunk)
}
