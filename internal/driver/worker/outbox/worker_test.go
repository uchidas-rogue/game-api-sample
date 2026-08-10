package outbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	workeroutbox "github.com/uchidas-rogue/game-api-sample/internal/driver/worker/outbox"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockoutbox "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox/mock"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
	mockshared "github.com/uchidas-rogue/game-api-sample/internal/usecase/shared/mock"
)

// assertReadCommitted は worker が張る tx がすべて READ COMMITTED で開始されることを検証する。
//
// 既定の REPEATABLE READ に戻ると ListPending の SELECT ... FOR UPDATE がギャップロックを取り、
// API 側の InsertOutboxEvent が INSERT_INTENTION 待ちでブロックされる
// （実測で API の p95 が 108ms → 4.6s に悪化。docs/testing/outbox-worker.md §0）。
// SKIP LOCKED はレコードロックを飛ばすだけでギャップロックは回避しないため、
// 分離レベルの明示が唯一の回避手段になる。
//
// DoInTx の可変長オプションを gomock.Any() で受けたまま中身を見ないと、
// worker.go から WithIsolation を削っても全テストが通ってしまう（回帰が素通りする）。
// 同じ不変条件を持つ outbox GC は internal/driver/batch/outbox_gc_test.go で
// 同名のヘルパーが担保している。
//
// worker goroutine から呼ばれるが、assert 系は t.Errorf 経由で並行呼び出し安全。
// 呼び出し元は必ず worker の終了を待ってからテストを抜ける。
func assertReadCommitted(t *testing.T, opts []shared.TxOption) {
	t.Helper()
	assert.Equal(t, shared.IsolationReadCommitted, shared.NewTxOptions(opts...).Isolation,
		"worker の tx は READ COMMITTED で開始すること")
}

// invokeDoInTx は DoInTx 呼び出しごとに fn(nil) を実行するだけのヘルパー。
// Worker のメソッドを直接呼ぶ（Run を経由しない）テストで使う。
func invokeDoInTx(t *testing.T, tx *mockshared.MockTransactor) {
	t.Helper()
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error, opts ...shared.TxOption) error {
			assertReadCommitted(t, opts)
			return fn(nil)
		}).
		AnyTimes()
}

// invokeDoInTxAndSignal は DoInTx 呼び出しごとに fn(nil) を実行し、called チャネルへ通知する
// ヘルパー。runOnce は1ティックで「バッチ適用 tx」「デコード不能イベントの retry 記録 tx」など
// 複数回 DoInTx を呼びうるため、AnyTimes で登録し、呼び出し回数はテスト側で
// called チャネルの受信数を見て検証する。
// wall-clock sleep に頼らず「N 回呼ばれるまで待つ」同期点を作るために使う。
// なお applyRedisAfterCommit は tx の外で走るため、この回数には現れない。
func invokeDoInTxAndSignal(t *testing.T, tx *mockshared.MockTransactor, called chan<- struct{}) {
	t.Helper()
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error, opts ...shared.TxOption) error {
			assertReadCommitted(t, opts)
			err := fn(nil)
			called <- struct{}{}
			return err
		}).
		AnyTimes()
}

// invokeDoInTxAndSignalTimes は呼び出し回数を厳密に固定したいケース向けの signal 付きヘルパー。
// batchSize による打ち切りなど「ちょうど N 回だけ呼ばれる」ことを strict モックで担保したい場合に使う。
func invokeDoInTxAndSignalTimes(t *testing.T, tx *mockshared.MockTransactor, called chan<- struct{}, times int) {
	t.Helper()
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error, opts ...shared.TxOption) error {
			assertReadCommitted(t, opts)
			err := fn(nil)
			called <- struct{}{}
			return err
		}).
		Times(times)
}

// waitForCalls は called チャネルから n 回シグナルを受け取るまで待つ。
// 規定時間を超えても満たさない場合は t.Fatal でテストを失敗させる。
func waitForCalls(t *testing.T, called <-chan struct{}, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for i := range n {
		select {
		case <-called:
		case <-deadline:
			t.Fatalf("waitForCalls: got %d/%d signals before timeout", i, n)
		}
	}
}

// stopAndWait は cancel して worker goroutine の終了を待つ。
func stopAndWait(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop within 2s after cancel")
	}
}

func mustMarshalRankingScoreAdded(t *testing.T, p outboxdomain.RankingScoreAddedPayload) []byte {
	t.Helper()
	return outboxdomain.MarshalRankingScoreAddedPayload(p)
}

// newScoreEvent は ranking_score_added イベントを1件組み立てる。
func newScoreEvent(t *testing.T, id uint64, guildID, userID, points int64) outboxdomain.Event {
	t.Helper()
	return outboxdomain.Event{
		ID:   id,
		Type: outboxdomain.EventTypeRankingScoreAdded,
		Payload: mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
			UserID: userID, GuildID: guildID, Points: points,
		}),
	}
}

// newUnknownEventWithRetry は retry_count を指定した poison イベントを組み立てる。
// 「この失敗で上限に達するか」は claim 時点の retry_count で決まるため、
// dead-letter の判定を検証するテストはここから作る。
func newUnknownEventWithRetry(id uint64, retryCount uint32) outboxdomain.Event {
	ev := newUnknownEvent(id)
	ev.RetryCount = retryCount
	return ev
}

// newUnknownEvent は event_type が未知の（＝デコード不能な）イベントを1件組み立てる。
// 恒久失敗イベント（poison）を模すために使う。
func newUnknownEvent(id uint64) outboxdomain.Event {
	return outboxdomain.Event{ID: id, Type: "unknown_event", Payload: []byte("{}")}
}

// runWorkerAndWaitCalls は w.Run をバックグラウンド goroutine で起動し、DoInTx が n 回
// 呼ばれるまで待ってから ctx を cancel し、Run の終了を待つ。
// PollInterval は十分大きく（time.Hour 等）設定し、テスト中に想定外の追加ティックが
// 発火しないようにすることを呼び出し側の責務とする。
func runWorkerAndWaitCalls(t *testing.T, w *workeroutbox.Worker, called <-chan struct{}, n int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()
	waitForCalls(t, called, n, 2*time.Second)

	// cancel 後も worker は数回 DoInTx を呼びうる。called が詰まって DoInTx の
	// 送信がブロックし worker が停止できなくなるのを防ぐため、終了まで受信し続ける。
	cancel()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-called:
			case <-done:
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop within 2s after cancel")
	}
	<-drained
}

// testMaxRetry はテストで使う失敗許容回数。retry_count がこれに達したイベントは
// ListPending / ClaimByID の対象から外れる（docs/testing/outbox-worker.md §0-4）。
// 「上限未満」「上限到達」を1回の IncrementRetry で作り分けられるよう、小さい値にしてある。
const testMaxRetry = 3

// deps はテスト用のモック依存一式。
type deps struct {
	outboxRepo  *mockoutbox.MockRepository
	rankingRepo *mockranking.MockRepository
	store       *mockranking.MockRankingStore
	tx          *mockshared.MockTransactor
}

func newDeps(ctrl *gomock.Controller) deps {
	return deps{
		outboxRepo:  mockoutbox.NewMockRepository(ctrl),
		rankingRepo: mockranking.NewMockRepository(ctrl),
		store:       mockranking.NewMockRankingStore(ctrl),
		tx:          mockshared.NewMockTransactor(ctrl),
	}
}

// newWorker は逐次処理（concurrency=1）の Worker を生成する。
// 副作用の呼び出し順序を検証するテストが決定的になるよう、既定は逐次とする。
func (d deps) newWorker(t *testing.T, batchSize int) *workeroutbox.Worker {
	t.Helper()
	return d.newWorkerWithConcurrency(t, batchSize, 1)
}

// newWorkerWithConcurrency は並列度を指定して Worker を生成する。
func (d deps) newWorkerWithConcurrency(t *testing.T, batchSize, concurrency int) *workeroutbox.Worker {
	t.Helper()
	return workeroutbox.New(workeroutbox.Config{
		Repo:         d.outboxRepo,
		RankingRepo:  d.rankingRepo,
		RankingStore: d.store,
		Tx:           d.tx,
		Logger:       slogtest.NewLogger(t, nil),
		// ticker 駆動の追加呼び出しをテスト中に発生させないため十分大きくする。
		PollInterval: time.Hour,
		BatchSize:    batchSize,
		Concurrency:  concurrency,
		MaxRetry:     testMaxRetry,
	})
}

// newWorkerWithRecorder は逐次処理の Worker を、ログを捕捉する Logger 付きで生成する。
// 「特定のログが出た / 出ていない」こと自体が仕様になっている検証にだけ使う。
func (d deps) newWorkerWithRecorder(t *testing.T, batchSize int) (*workeroutbox.Worker, *slogtest.Recorder) {
	t.Helper()
	logger, rec := slogtest.NewRecordingLogger(t, nil)
	return workeroutbox.New(workeroutbox.Config{
		Repo:         d.outboxRepo,
		RankingRepo:  d.rankingRepo,
		RankingStore: d.store,
		Tx:           d.tx,
		Logger:       logger,
		PollInterval: time.Hour,
		BatchSize:    batchSize,
		Concurrency:  1,
		MaxRetry:     testMaxRetry,
	}), rec
}

// pendingEmptyAnyTimes は ListPending が「候補なし」を任意回数返すよう設定する。
func pendingEmptyAnyTimes(outboxRepo *mockoutbox.MockRepository) {
	outboxRepo.EXPECT().
		ListPending(gomock.Any(), gomock.Any(), gomock.Any(), uint32(testMaxRetry)).
		Return(nil, nil).
		AnyTimes()
}

// expectPerEventApply はフォールバック経路（イベント単位 tx）が1イベントを処理しきる
// 一連の呼び出しを登録する。Redis 反映は COMMIT 後に ApplyScoreDeltas 1本で行う。
func expectPerEventApply(d deps, ev outboxdomain.Event, guildID, userID, points int64) {
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), ev.ID, uint32(testMaxRetry)).Return(ev, true, nil)
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, points).Return(nil)
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, points).Return(nil)
	d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), ev.ID).Return(nil)
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(),
		[]rankingdomain.UserPointDelta{{UserID: userID, Points: points}},
		[]rankingdomain.GuildScoreDelta{{GuildID: guildID, Points: points}}).Return(nil)
}

// ---------------------------------------------------------------------------
// 主経路（applyBatch: バッチ単位トランザクション）
//
// 並びは docs/testing/outbox-worker.md §2-1 のテスト仕様表（図のパスが短い順）と対応する。
// ---------------------------------------------------------------------------

// [§2-1 ケース1] TestWorker_applyBatch_pending無しは即座に終了する は
// 候補ゼロ件で副作用を発行しないことを確認する。
func TestWorker_applyBatch_pending無しは即座に終了する(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 1)
}

// [§2-1 ケース2] TestWorker_applyBatch_全件デコード不能ならMySQLもRedisも呼ばれない は、
// 適用対象が1件も残らない場合に副作用を一切発行しないことを確認する。
// マーク対象が空のまま MarkProcessedByIDs / bulk 系を呼ぶと無駄な DB 往復になる。
func TestWorker_applyBatch_全件デコード不能ならMySQLもRedisも呼ばれない(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{newUnknownEvent(99)}, nil)
	// bulk 系 / ApplyScoreDeltas / MarkProcessedByIDs は EXPECT しない
	// （呼ばれたら gomock が未設定呼び出しとして落とす）。
	d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(99), gomock.Any()).Return(nil)

	// バッチ tx 1回 + retry 記録 tx 1回。
	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 2)
}

// [§2-1 ケース3] TestWorker_applyBatch_IncrementRetry失敗はログのみ は、retry 記録自体が失敗しても
// ティックがエラーにならず（イベントは pending のままなので次ティックで再処理される）
// worker が継続することを確認する。
func TestWorker_applyBatch_IncrementRetry失敗はログのみ(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{newUnknownEvent(50)}, nil)
	d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(50), gomock.Any()).
		Return(errors.New("retry failed"))

	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 2)
}

// [§2-1 ケース4] TestWorker_applyBatch_COMMIT後のRedis失敗はログのみでフォールバックしない は、
// COMMIT 確定後の Redis 反映が失敗しても再処理させないことを確認する。
//
// MySQL 側は既にコミット済み（加算・履歴・処理済みマークが確定）なので、イベントを
// 再適用すると Redis だけ二重に加算される。したがって Redis の失敗では
//   - エラーを返さない（＝フォールバック経路に落ちない。ListPending を再発行しない）
//   - IncrementRetry も記録しない（イベントは正常に処理済み）
//
// ずれの復旧は RankingSyncer の焼き直しに委ねる（docs/testing/outbox-worker.md §0-1）。
func TestWorker_applyBatch_COMMIT後のRedis失敗はログのみでフォールバックしない(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)

	ev := newScoreEvent(t, 1, 1, 10, 100)
	// Times は既定で1回。フォールバックに落ちれば listCandidates が2回目を呼ぶため失敗する。
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{ev}, nil)

	d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{1}).Return(nil)
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("redis down"))
	// ClaimByID / IncrementRetry は EXPECT しない（呼ばれたら失敗する）。

	// バッチ tx 1回のみ。Redis は tx の外なので DoInTx を増やさない。
	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 1)
}

// [§2-1 ケース5] TestWorker_applyBatch_各ステップの失敗でRedisに到達せずフォールバックする は、
// バッチ tx 内のどのステップで失敗しても
//   - 同一バッチ内の以降のステップが実行されない（＝tx がロールバックされ
//     MarkProcessedByIDs に到達しない = イベントを取りこぼさない）
//   - **Redis 反映（ApplyScoreDeltas）に到達しない**
//   - その後イベント単位のフォールバック経路へ切り替わり、処理が前進する
//
// ことを検証する。
//
// 失敗時にイベントを処理済みにしてしまうとイベントが失われるため、
// 「失敗したら絶対にバッチでマークしない」ことが本経路の最重要不変条件。
// あわせて「tx が巻き戻る失敗では Redis を触っていない」ことが、再試行時の
// 二重加算を防ぐ不変条件になる（docs/testing/outbox-worker.md §0-1）。
// ApplyScoreDeltas はフォールバック経路でも使うため Times(1) で固定する。
// バッチ経路でも呼ばれていれば2回になり gomock が検知する。
func TestWorker_applyBatch_各ステップの失敗でRedisに到達せずフォールバックする(t *testing.T) {
	t.Parallel()

	errStep := errors.New("step failed")

	tests := []struct {
		name  string
		setup func(d deps, guildDeltas []rankingdomain.GuildScoreDelta,
			histories []rankingdomain.GuildScoreHistoryEntry)
	}{
		{
			name: "BulkIncrementGuildScores 失敗（履歴/マーク/Redis には進まない）",
			setup: func(d deps, guildDeltas []rankingdomain.GuildScoreDelta,
				_ []rankingdomain.GuildScoreHistoryEntry) {
				d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), guildDeltas).Return(errStep)
			},
		},
		{
			name: "BulkInsertGuildScoreHistories 失敗（マーク/Redis には進まない）",
			setup: func(d deps, guildDeltas []rankingdomain.GuildScoreDelta,
				histories []rankingdomain.GuildScoreHistoryEntry) {
				d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), guildDeltas).Return(nil)
				d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), histories).Return(errStep)
			},
		},
		{
			name: "MarkProcessedByIDs 失敗（tx ロールバックで全副作用が巻き戻り、Redis にも到達しない）",
			setup: func(d deps, guildDeltas []rankingdomain.GuildScoreDelta,
				histories []rankingdomain.GuildScoreHistoryEntry) {
				d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), guildDeltas).Return(nil)
				d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), histories).Return(nil)
				d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{1}).Return(errStep)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			d := newDeps(ctrl)
			called := make(chan struct{}, 16)
			invokeDoInTxAndSignal(t, d.tx, called)

			ev := newScoreEvent(t, 1, 1, 10, 100)
			// applyBatch（主経路）と listCandidates（フォールバック）で1回ずつ呼ばれる。
			d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
				Return([]outboxdomain.Event{ev}, nil).Times(2)

			tt.setup(d,
				[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 100}},
				[]rankingdomain.GuildScoreHistoryEntry{{GuildID: 1, UserID: 10, Points: 100}},
			)

			// フォールバック経路（イベント単位 tx）が同じイベントを処理しきる。
			// 単数形の IncrementGuildScore / InsertGuildScoreHistory / MarkProcessed が
			// 呼ばれること自体が、経路の切り替わりが起きた証拠になる。
			expectPerEventApply(d, ev, 1, 10, 100)

			// バッチ適用 tx + listCandidates tx + processOne tx。
			runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 3)
		})
	}
}

// [§2-1 ケース6] TestWorker_applyBatch_デコード不能イベントは除外され個別にIncrementRetryされる は、
// 未知の event_type / 壊れた payload を「決定的な失敗」とみなしてバッチから外し、
// 正常なイベントだけを適用することを検証する。
//
// 旧実装（イベント単位）では未知 event_type は applyEventInTx の default で弾かれていたが、
// バッチ適用ではデコード段階で除外され、フォールバック経路には回らない。
// retry 記録はバッチ commit 後に別 tx で行う（バッチ tx に混ぜると巻き添えで
// ロールバックされ、retry_count が増えないため）。
func TestWorker_applyBatch_デコード不能イベントは除外され個別にIncrementRetryされる(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)

	ok := newScoreEvent(t, 1, 1, 10, 100)
	unknownType := outboxdomain.Event{ID: 2, Type: "unknown_event", Payload: []byte("{}"), RetryCount: 3}
	brokenPayload := outboxdomain.Event{
		ID: 3, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: []byte("{broken"),
	}

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{ok, unknownType, brokenPayload}, nil)

	// 正常な1件だけが適用され、マーク対象も ID=1 のみ。
	d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(),
		[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 100}}).Return(nil)
	d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(),
		[]rankingdomain.GuildScoreHistoryEntry{{GuildID: 1, UserID: 10, Points: 100}}).Return(nil)
	d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{1}).Return(nil)
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(),
		[]rankingdomain.UserPointDelta{{UserID: 10, Points: 100}},
		[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 100}}).Return(nil)

	var unknownErr, brokenErr string
	d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(2), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ shared.Tx, _ uint64, lastError string) error {
			unknownErr = lastError
			return nil
		})
	d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(3), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ shared.Tx, _ uint64, lastError string) error {
			brokenErr = lastError
			return nil
		})

	// バッチ tx 1回 + デコード不能 2件の retry 記録 tx 2回。
	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 3)

	assert.Contains(t, unknownErr, outboxdomain.ErrUnknownEventType.Error())
	assert.Contains(t, brokenErr, "unmarshal ranking score added payload")
}

// [§2-1 ケース7] TestWorker_applyBatch_適用順序 は MySQL（source of truth）2本 →
// MarkProcessedByIDs までを tx 内で行い、**COMMIT が確定してから** Redis（キャッシュ）へ
// 反映することを固定する。
//
// Redis を tx の中に置くと、後続の MarkProcessedByIDs や COMMIT が失敗したときに
// MySQL だけロールバックされ、Redis の加算だけが残る。イベントは未マークのまま
// 再取得されるため、再適用で Redis だけ二重に加算される（しかもリトライのたびに累積する）。
// COMMIT 後に回すことで、ずれを「欠落・高々1バッチぶん・累積しない」側へ倒す。
func TestWorker_applyBatch_適用順序_MySQL2本_MarkProcessedByIDs_COMMIT後にRedis(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)

	// COMMIT（= DoInTx の復帰）と Redis 反映の前後関係を検証するため、
	// tx の内側を抜けた時点を committed へ記録する。
	committed := make(chan struct{})
	d.tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error, opts ...shared.TxOption) error {
			assertReadCommitted(t, opts)
			err := fn(nil)
			close(committed)
			called <- struct{}{}
			return err
		}).
		Times(1)

	ev := newScoreEvent(t, 1, 1, 10, 100)
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{ev}, nil)

	// 1件だけなので集約結果は決定的。引数まで含めて順序を固定する。
	guildDeltas := []rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 100}}
	gomock.InOrder(
		d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), guildDeltas).Return(nil),
		d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(),
			[]rankingdomain.GuildScoreHistoryEntry{{GuildID: 1, UserID: 10, Points: 100}}).Return(nil),
		d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{1}).Return(nil),
	)

	redisApplied := make(chan bool, 1)
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(),
		[]rankingdomain.UserPointDelta{{UserID: 10, Points: 100}}, guildDeltas).
		DoAndReturn(func(_ context.Context, _ []rankingdomain.UserPointDelta,
			_ []rankingdomain.GuildScoreDelta) error {
			// tx の外で呼ばれていれば committed は既にクローズされている。
			select {
			case <-committed:
				redisApplied <- true
			default:
				redisApplied <- false
			}
			return nil
		})

	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 1)

	select {
	case afterCommit := <-redisApplied:
		assert.True(t, afterCommit, "Redis 反映は COMMIT 確定後に行うべき（tx 内だと二重加算の窓が開く）")
	default:
		t.Fatal("ApplyScoreDeltas が呼ばれなかった")
	}
}

// [§2-1 ケース8] TestWorker_applyBatch_同一ギルドは合算され履歴はイベント単位で作られる は
// バッチ集約（buildBatchWork）の責務を検証する。
// スコア加算は可換なのでギルド／ユーザー単位に合算して DB 往復を減らせるが、
// 履歴は「誰がいつ何点入れたか」を残す必要があるためイベント単位のまま保持する。
// MarkProcessedByIDs が処理対象の全 ID を受け取ることもここで確認する。
func TestWorker_applyBatch_同一ギルドは合算され履歴はイベント単位で作られる(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)

	// guild=1 に2件、guild=2 に1件。user=10 は guild をまたいで2件。
	events := []outboxdomain.Event{
		newScoreEvent(t, 1, 1, 10, 100),
		newScoreEvent(t, 2, 1, 11, 200),
		newScoreEvent(t, 3, 2, 10, 50),
	}
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).Return(events, nil)

	var (
		gotGuildDeltas    []rankingdomain.GuildScoreDelta
		gotRedisGuildDeps []rankingdomain.GuildScoreDelta
		gotUserDeltas     []rankingdomain.UserPointDelta
		gotHistories      []rankingdomain.GuildScoreHistoryEntry
		gotIDs            []uint64
	)
	d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ shared.Tx, deltas []rankingdomain.GuildScoreDelta) error {
			gotGuildDeltas = deltas
			return nil
		})
	d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ shared.Tx, entries []rankingdomain.GuildScoreHistoryEntry) error {
			gotHistories = entries
			return nil
		})
	d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ shared.Tx, ids []uint64) error {
			gotIDs = ids
			return nil
		})
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, users []rankingdomain.UserPointDelta, guilds []rankingdomain.GuildScoreDelta) error {
			gotUserDeltas, gotRedisGuildDeps = users, guilds
			return nil
		})

	// バッチ適用の tx 1回のみ（候補 3 件 < batchSize 100 なのでドレイン終了）。
	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 1)

	// 集約結果は map 由来のため順序不定。件数と内容の一致だけを検証する。
	assert.ElementsMatch(t, []rankingdomain.GuildScoreDelta{
		{GuildID: 1, Points: 300}, // 100 + 200 が1件に合算される
		{GuildID: 2, Points: 50},
	}, gotGuildDeltas)
	assert.ElementsMatch(t, []rankingdomain.UserPointDelta{
		{UserID: 10, Points: 150}, // 100 + 50
		{UserID: 11, Points: 200},
	}, gotUserDeltas)
	// Redis へは MySQL と同一の guildDeltas が渡る。
	assert.ElementsMatch(t, gotGuildDeltas, gotRedisGuildDeps)
	// 履歴は集約せず、イベント件数ぶんをイベント順で作る。
	assert.Equal(t, []rankingdomain.GuildScoreHistoryEntry{
		{GuildID: 1, UserID: 10, Points: 100},
		{GuildID: 1, UserID: 11, Points: 200},
		{GuildID: 2, UserID: 10, Points: 50},
	}, gotHistories)
	assert.Equal(t, []uint64{1, 2, 3}, gotIDs)
}

// ---------------------------------------------------------------------------
// runOnce（1ティックぶんの処理・ドレイン判定）
//
// 並びは docs/testing/outbox-worker.md §2 のテスト仕様表と対応する。
// ---------------------------------------------------------------------------

// [§2 ケース1] TestWorker_runOnce_ListPendingエラー は候補取得自体の失敗でティックが
// 即座に中断されることを確認する。副作用にも retry 記録にも到達しない。
func TestWorker_runOnce_ListPendingエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
		Return(nil, errors.New("db down")).AnyTimes()
	// bulk 系 / IncrementRetry / MarkProcessedByIDs は呼ばれない

	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 1)
}

// [§2 ケース2] TestWorker_runOnce_appliesTickTimeout は TickTimeout > 0 のとき
// runOnce が deadline 付き context を DoInTx へ渡すことを確認する。
// これにより DB/Redis のブロッキングでループがハングしない。
func TestWorker_runOnce_appliesTickTimeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)

	gotDeadline := make(chan bool, 1)
	d.tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(shared.Tx) error, opts ...shared.TxOption) error {
			assertReadCommitted(t, opts)
			_, ok := ctx.Deadline()
			select {
			case gotDeadline <- ok:
			default:
			}
			return fn(nil)
		}).
		AnyTimes()
	pendingEmptyAnyTimes(d.outboxRepo)

	w := workeroutbox.New(workeroutbox.Config{
		MaxRetry: testMaxRetry,
		Repo:     d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx,
		Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 100,
		TickTimeout: time.Minute,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	select {
	case ok := <-gotDeadline:
		assert.True(t, ok, "runOnce は deadline 付き context を DoInTx に渡すべき")
	case <-time.After(2 * time.Second):
		t.Fatal("DoInTx が呼ばれなかった")
	}
	stopAndWait(t, cancel, done)
}

// [§2 ケース3] TestWorker_runOnce_全件前進しなければドレインを打ち切る は、
// 取得件数が batchSize ちょうどでも**1件も前進しなかった**場合にドレインを止めることを確認する。
//
// ListPendingOutboxEvents は retry_count を条件に含めないため、デコード不能な恒久失敗
// イベント（poison）は何度でも取得される。枯渇判定を「取得件数 < batchSize」だけで行うと、
// poison が batchSize 件以上滞留したときに毎回ちょうど batchSize 件が返り続け、
// ドレインが永久に終わらない（tickTimeout で打ち切られても drainNow がスリープなしで
// 再入するため、ListPending と IncrementRetry を連打するビジーループになる）。
//
// ListPending を Times(1) の strict モックにしてあるため、取得件数だけで判定する実装に
// 戻すと2回目の呼び出しで gomock が未設定呼び出しとして検知する。
func TestWorker_runOnce_全件前進しなければドレインを打ち切る(t *testing.T) {
	t.Parallel()

	const batchSize = 2

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)

	// 取得件数はちょうど batchSize だが、全件デコード不能なので前進件数は 0。
	d.outboxRepo.EXPECT().
		ListPending(gomock.Any(), gomock.Any(), int32(batchSize), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{newUnknownEvent(1), newUnknownEvent(2)}, nil)
	d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(1), gomock.Any()).Return(nil)
	d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(2), gomock.Any()).Return(nil)
	// bulk 系 / MarkProcessedByIDs / ApplyScoreDeltas は呼ばれない。

	// バッチ tx 1回 + retry 記録 tx 2回。以降ドレインは打ち切られる。
	runWorkerAndWaitCalls(t, d.newWorker(t, batchSize), called, 3)
}

// [§2 ケース4] TestWorker_runOnce_候補が枯れるまでドレインする は、ListPending が batchSize
// ちょうどを返す限り1ティック内でバッチ適用を繰り返し、batchSize 未満になった時点で
// 止まることを確認する。
// batchSize で打ち切ると、通知が途切れた時点で残りが pollInterval 待ちになり
// バックログの消化が停止するため、この挙動が必要。
func TestWorker_runOnce_候補が枯れるまでドレインする(t *testing.T) {
	t.Parallel()

	const batchSize = 2

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	// バッチ単位 tx なので 1巡 = DoInTx 1回。
	// 1巡目: ちょうど batchSize 件かつ前進あり → 継続、2巡目: batchSize 未満 → 打ち切り。
	// Times(2) により3巡目が走らない（＝打ち切っている）ことも担保する。
	const wantDoInTx = 2
	invokeDoInTxAndSignalTimes(t, d.tx, called, wantDoInTx)

	// 集約結果を決定的にするため全イベントを同一 guild/user にする。
	ev1, ev2, ev3 := newScoreEvent(t, 1, 1, 1, 10), newScoreEvent(t, 2, 1, 1, 10), newScoreEvent(t, 3, 1, 1, 10)

	// limit=batchSize で呼ばれること、および「ちょうど batchSize 件 → 継続」
	// 「batchSize 未満 → 打ち切り」の順で返ることを gomock.InOrder で固定する。
	gomock.InOrder(
		d.outboxRepo.EXPECT().
			ListPending(gomock.Any(), gomock.Any(), int32(batchSize), uint32(testMaxRetry)).
			Return([]outboxdomain.Event{ev1, ev2}, nil),
		d.outboxRepo.EXPECT().
			ListPending(gomock.Any(), gomock.Any(), int32(batchSize), uint32(testMaxRetry)).
			Return([]outboxdomain.Event{ev3}, nil),
	)

	d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
	d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
	// 各巡でその巡の候補だけがマークされる。
	gomock.InOrder(
		d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{1, 2}).Return(nil),
		d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{3}).Return(nil),
	)

	runWorkerAndWaitCalls(t, d.newWorker(t, batchSize), called, wantDoInTx)
}

// [§2 ケース5] TestWorker_runOnce_バッチ失敗時はフォールバックへ切り替わり処理が前進する は、
// Run（実際のティック駆動）を通した経路切り替えの回帰テスト。
//
// runOnce が applyBatch のエラーを絶対に握りつぶさず、かつフォールバックの手前で
// return しないことを担保する。以前の実装は absorbTickDeadline の戻り値を先に返しており、
// 実エラーでも applyPerEvent へ到達しなかった（バッチ内に恒久失敗イベントがあると
// そのバッチが永久に前進しない head-of-line blocking が発生していた）。
// その実装に戻すと ClaimByID / MarkProcessed が呼ばれず、DoInTx も1回しか走らないため
// 本テストは待機タイムアウトで失敗する。
func TestWorker_runOnce_バッチ失敗時はフォールバックへ切り替わり処理が前進する(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)

	// 集約結果を決定的にするため同一 guild/user のイベントを2件用意する。
	ev1 := newScoreEvent(t, 1, 1, 10, 100)
	ev2 := newScoreEvent(t, 2, 1, 10, 100)
	events := []outboxdomain.Event{ev1, ev2}
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).Return(events, nil).Times(2)

	// 主経路のバッチ適用は失敗する（例: 一括 upsert のデッドロック）。
	d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(),
		[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 200}}).
		Return(errors.New("deadlock found when trying to get lock"))
	// バッチ経路の以降のステップには進まない（EXPECT を置かないため呼ばれたら失敗する）。

	// フォールバック経路で各イベントが個別に claim → 適用 → MarkProcessed → Redis される。
	marked := make(chan uint64, len(events))
	for _, ev := range events {
		d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), ev.ID, uint32(testMaxRetry)).Return(ev, true, nil)
		d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), ev.ID).
			DoAndReturn(func(_ context.Context, _ shared.Tx, id uint64) error {
				marked <- id
				return nil
			})
	}
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(100)).Return(nil).Times(len(events))
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(10), int64(100)).Return(nil).Times(len(events))
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(),
		[]rankingdomain.UserPointDelta{{UserID: 10, Points: 100}},
		[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 100}}).Return(nil).Times(len(events))

	// バッチ適用 tx + listCandidates tx + processOne tx × 2 = 4。
	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 4)

	close(marked)
	var gotMarked []uint64
	for id := range marked {
		gotMarked = append(gotMarked, id)
	}
	assert.ElementsMatch(t, []uint64{1, 2}, gotMarked,
		"バッチ失敗後もフォールバック経路で全イベントが処理済みになるべき")
}

// ---------------------------------------------------------------------------
// フォールバック経路（applyPerEvent: イベント単位トランザクション）
//
// runOnce が applyBatch の失敗を検知して切り替える経路。切り替わること自体は
// TestWorker_runOnce_バッチ失敗時はフォールバックへ切り替わり処理が前進する で検証済みのため、
// 以下は ApplyPerEventForTest 経由で経路内部の分岐を決定的に検証する
// （seam を置く理由は export_test.go のコメント参照）。
// ---------------------------------------------------------------------------

// TestWorker_applyPerEvent_フォールバック経路_正常系_異常系 はイベント単位トランザクションの
// dispatch 挙動を検証する。ListPending で候補を取得 → ClaimByID(id) で確保 →
// applyEventInTx（MySQL 副作用）→ MarkProcessed → COMMIT → Redis 反映 の流れと、
// applyEventInTx 失敗時の IncrementRetry 記録を確認する。
//
// ケースの並びは docs/testing/outbox-worker.md §2-2 のテスト仕様表と 1 対 1 で対応する。
// wantListed は ListPending の取得件数、wantApplied は処理済みマークまで到達した件数。
// 両者を分けて検証するのは、枯渇判定が wantApplied 側に依存しているため（§0-2）。
func TestWorker_applyPerEvent_フォールバック経路_正常系_異常系(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker
		wantListed  int
		wantApplied int
	}{
		{
			name: "正常系: 候補なしなら 0 件を返し tx を張らない",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)
				pendingEmptyAnyTimes(d.outboxRepo)

				return d.newWorker(t, 100)
			},
			wantListed:  0,
			wantApplied: 0,
		},
		{
			name: "正常系: ClaimByID が found=false（処理済み/他worker確保中）なら skip し前進として数える",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)

				ev := newScoreEvent(t, 5, 1, 10, 500)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(5), uint32(testMaxRetry)).
					Return(outboxdomain.Event{}, false, nil)
				// 副作用 / MarkProcessed / IncrementRetry は呼ばれない

				return d.newWorker(t, 100)
			},
			wantListed:  1,
			wantApplied: 1,
		},
		{
			// 主経路（applyBatch）では未知 event_type はデコード段階で除外されるが、
			// フォールバック経路では applyEventInTx の default に落ちて同じく IncrementRetry される。
			name: "異常系: 未知の event_type は ErrUnknownEventType で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)

				ev := newUnknownEvent(99)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(99), uint32(testMaxRetry)).Return(ev, true, nil)
				// last_error には ErrUnknownEventType のセンチネル文言が残る。
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(99),
					gomock.Cond(func(s string) bool {
						return strings.Contains(s, outboxdomain.ErrUnknownEventType.Error())
					})).Return(nil)

				return d.newWorker(t, 100)
			},
			wantListed:  1,
			wantApplied: 0,
		},
		{
			// payload が壊れたイベントも applyEventInTx 内の Unmarshal 失敗として retry 記録される。
			name: "異常系: payload デコード失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)

				ev := outboxdomain.Event{
					ID: 100, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: []byte("{broken"),
				}
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(100), uint32(testMaxRetry)).Return(ev, true, nil)
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(100), gomock.Any()).Return(nil)

				return d.newWorker(t, 100)
			},
			wantListed:  1,
			wantApplied: 0,
		},
		{
			// retry 記録自体の失敗はログのみ。イベントは pending のままなので次ティックで再処理される。
			name: "異常系: IncrementRetry 失敗はログのみでエラーを伝播しない",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)

				ev := newUnknownEvent(50)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(50), uint32(testMaxRetry)).Return(ev, true, nil)
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(50), gomock.Any()).
					Return(errors.New("retry failed"))

				return d.newWorker(t, 100)
			},
			wantListed:  1,
			wantApplied: 0,
		},
		{
			name: "異常系: MySQL IncrementGuildScore 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)

				ev := newScoreEvent(t, 7, 1, 10, 500)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(7), uint32(testMaxRetry)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(500)).
					Return(errors.New("mysql down"))
				// InsertGuildScoreHistory / MarkProcessed / Redis は呼ばれない
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(7), gomock.Any()).Return(nil)

				return d.newWorker(t, 100)
			},
			wantListed:  1,
			wantApplied: 0,
		},
		{
			name: "異常系: InsertGuildScoreHistory 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)

				ev := newScoreEvent(t, 8, 2, 11, 300)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(8), uint32(testMaxRetry)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(2), int64(300)).Return(nil)
				d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(2), int64(11), int64(300)).
					Return(errors.New("mysql history down"))
				// MarkProcessed / Redis は呼ばれない
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(8), gomock.Any()).Return(nil)

				return d.newWorker(t, 100)
			},
			wantListed:  1,
			wantApplied: 0,
		},
		{
			// COMMIT 後の Redis 失敗は再処理させない。MySQL は確定済みなので、
			// 再適用すると Redis だけ二重に加算される（§0-1）。
			name: "異常系: COMMIT 後の Redis 反映が失敗してもログのみ（retry 記録しない）",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)

				ev := newScoreEvent(t, 9, 3, 12, 200)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(9), uint32(testMaxRetry)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(3), int64(200)).Return(nil)
				d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(3), int64(12), int64(200)).Return(nil)
				d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(9)).Return(nil)
				d.store.EXPECT().ApplyScoreDeltas(gomock.Any(),
					[]rankingdomain.UserPointDelta{{UserID: 12, Points: 200}},
					[]rankingdomain.GuildScoreDelta{{GuildID: 3, Points: 200}}).
					Return(errors.New("redis down"))
				// IncrementRetry は呼ばれない（イベントは処理済み）。

				return d.newWorker(t, 100)
			},
			wantListed:  1,
			wantApplied: 1,
		},
		{
			name: "正常系: MySQL(IncrementGuildScore→InsertGuildScoreHistory)→MarkProcessed→COMMIT 後に Redis",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)

				ev := newScoreEvent(t, 1, 1, 10, 500)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(1), uint32(testMaxRetry)).Return(ev, true, nil)

				gomock.InOrder(
					d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(500)).Return(nil),
					d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(10), int64(500)).Return(nil),
					d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(1)).Return(nil),
					d.store.EXPECT().ApplyScoreDeltas(gomock.Any(),
						[]rankingdomain.UserPointDelta{{UserID: 10, Points: 500}},
						[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 500}}).Return(nil),
				)

				return d.newWorker(t, 100)
			},
			wantListed:  1,
			wantApplied: 1,
		},
		{
			// フォールバック経路の存在意義そのものの検証。
			// 候補列の先頭イベントが恒久失敗（poison）しても、後続の候補は影響を受けず処理される。
			name: "正常系: 先頭イベントが失敗しても後続の候補は処理される（head-of-line blocking 回避）",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(t, d.tx)

				poison := newScoreEvent(t, 1, 2, 20, 100)
				ok := newScoreEvent(t, 2, 3, 21, 200)

				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
					Return([]outboxdomain.Event{poison, ok}, nil)

				// 先頭イベント: claim 成功だが MySQL 加算が失敗 → IncrementRetry。
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(1), uint32(testMaxRetry)).Return(poison, true, nil)
				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(2), int64(100)).
					Return(errors.New("mysql down (poison)"))
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(1), gomock.Any()).Return(nil)

				// 後続イベント: 先頭の失敗の影響を受けず claim → 適用 → MarkProcessed → Redis が成功する。
				expectPerEventApply(d, ok, 3, 21, 200)

				return d.newWorker(t, 100)
			},
			wantListed:  2,
			wantApplied: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			w := tt.setup(t, ctrl)

			listed, applied, err := w.ApplyPerEventForTest(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tt.wantListed, listed, "listed（取得件数）")
			assert.Equal(t, tt.wantApplied, applied, "applied（前進件数）")
		})
	}
}

// TestWorker_applyPerEvent_フォールバック経路_ListPendingエラー は候補取得自体の失敗で
// エラーが伝播し、claim / handle に到達しないことを確認する。
func TestWorker_applyPerEvent_フォールバック経路_ListPendingエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	invokeDoInTx(t, d.tx)

	errDB := errors.New("db down")
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).Return(nil, errDB)
	// ClaimByID / IncrementRetry / MarkProcessed は呼ばれない

	listed, applied, err := d.newWorker(t, 100).ApplyPerEventForTest(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, errDB)
	assert.Equal(t, 0, listed)
	assert.Equal(t, 0, applied)
}

// TestWorker_applyPerEvent_フォールバック経路_ClaimByIDエラー は claim 自体の失敗
// （applyEventInTx 以外のインフラ起因エラー）でティックが打ち切られることを確認する。
// applyEventInTx の失敗ではないため IncrementRetry は呼ばれない。
func TestWorker_applyPerEvent_フォールバック経路_ClaimByIDエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	invokeDoInTx(t, d.tx)

	ev := newScoreEvent(t, 42, 1, 1, 100)
	errClaim := errors.New("claim db down")
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{ev}, nil)
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(42), uint32(testMaxRetry)).
		Return(outboxdomain.Event{}, false, errClaim)
	// IncrementRetry / MarkProcessed は呼ばれない

	listed, applied, err := d.newWorker(t, 100).ApplyPerEventForTest(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, errClaim)
	assert.Equal(t, 0, listed)
	assert.Equal(t, 0, applied)
}

// TestWorker_applyPerEvent_フォールバック経路_MarkProcessedエラー は MarkProcessed 失敗時に
// ティックが打ち切られ、IncrementRetry は呼ばれず（MySQL 副作用自体は成功しているため）、
// Redis 反映にも後続候補にも到達しないことを確認する。
func TestWorker_applyPerEvent_フォールバック経路_MarkProcessedエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	invokeDoInTx(t, d.tx)

	ev1 := newScoreEvent(t, 42, 1, 1, 100)
	ev2 := newScoreEvent(t, 43, 2, 2, 200)
	errMark := errors.New("mark failed")

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{ev1, ev2}, nil)
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(42), uint32(testMaxRetry)).Return(ev1, true, nil)
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(100)).Return(nil)
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(1), int64(100)).Return(nil)
	d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(42)).Return(errMark)
	// COMMIT していないので ApplyScoreDeltas は呼ばれない（二重加算の窓を開けない）。
	// claim/mark の失敗のため IncrementRetry も呼ばれない。
	// concurrency=1 なので ev1 でティックが中断し ev2 の ClaimByID は呼ばれない。

	listed, applied, err := d.newWorker(t, 100).ApplyPerEventForTest(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, errMark)
	assert.Equal(t, 0, listed)
	assert.Equal(t, 0, applied)
}

// TestWorker_recordRetry_上限到達でdeadLetterログを出す は max retry / DLQ の観測面を検証する
// （docs/testing/outbox-worker.md §0-4）。retry_count が maxRetry に達したイベントは以降
// ListPending / ClaimByID から外れるため、運用が気づける唯一の手がかりがこの ERROR ログになる。
//
// 「出る／出ない」の3ケースを同じ関数にまとめているのは、判定条件
// （IncrementRetry の成否 × retry_count+1 が上限に達するか）の組み合わせを1箇所で見るため。
func TestWorker_recordRetry_上限到達でdeadLetterログを出す(t *testing.T) {
	t.Parallel()

	const deadLetterMsg = "outbox event dead-lettered"

	tests := []struct {
		name string
		// retryCount は claim した時点の値。この失敗で retryCount+1 になる。
		retryCount     uint32
		incrementErr   error
		wantDeadLetter int
	}{
		{
			name:           "上限未満: dead-letter ログを出さない",
			retryCount:     0,
			wantDeadLetter: 0,
		},
		{
			// testMaxRetry=3 なので 2 からの加算でちょうど上限に達する（境界）。
			name:           "上限到達: dead-letter ログを1回出す",
			retryCount:     testMaxRetry - 1,
			wantDeadLetter: 1,
		},
		{
			// 記録できていない = retry_count は据え置きで、まだ打ち切られていない。
			// ここでログを出すと「打ち切った」という誤った信号になる。
			name:           "上限相当だが IncrementRetry が失敗: dead-letter ログを出さない",
			retryCount:     testMaxRetry - 1,
			incrementErr:   errors.New("increment failed"),
			wantDeadLetter: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			d := newDeps(ctrl)
			invokeDoInTx(t, d.tx)

			poison := newUnknownEventWithRetry(1, tt.retryCount)
			d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100), uint32(testMaxRetry)).
				Return([]outboxdomain.Event{poison}, nil)
			d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(1), uint32(testMaxRetry)).
				Return(poison, true, nil)
			d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(1), gomock.Any()).
				Return(tt.incrementErr)

			w, rec := d.newWorkerWithRecorder(t, 100)
			listed, applied, err := w.ApplyPerEventForTest(context.Background())
			require.NoError(t, err)
			assert.Equal(t, 1, listed)
			assert.Equal(t, 0, applied)

			assert.Equal(t, tt.wantDeadLetter, rec.Count("level=ERROR", deadLetterMsg),
				"dead-letter ログの回数")
			// 失敗そのものの記録（WARN）は打ち切りの有無に関係なく必ず出る。
			assert.Equal(t, 1, rec.Count("level=WARN", "outbox event handling failed"))
		})
	}
}

// TestWorker_applyPerEvent_フォールバック経路_並列処理 は concurrency > 1 のとき候補が並列に
// 処理され、かつ各イベントがちょうど1回ずつ MarkProcessed されることを確認する。
// 二重処理が起きないことの担保は ClaimByID の FOR UPDATE SKIP LOCKED（DB側）だが、
// worker 側でも同一 ID を複数 goroutine へ配らないことを検証する。
// race 検出は make test/race で行う。
func TestWorker_applyPerEvent_フォールバック経路_並列処理(t *testing.T) {
	t.Parallel()

	const (
		batchSize   = 4
		concurrency = 4
	)

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	invokeDoInTx(t, d.tx)

	events := make([]outboxdomain.Event, 0, batchSize)
	for id := uint64(1); id <= batchSize; id++ {
		events = append(events, newScoreEvent(t, id, 1, 1, 10))
	}
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(batchSize), uint32(testMaxRetry)).Return(events, nil)

	// 各 ID はちょうど1回ずつ claim / mark されること（Times は既定で1回）。
	for _, ev := range events {
		d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), ev.ID, uint32(testMaxRetry)).Return(ev, true, nil)
		d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), ev.ID).Return(nil)
	}
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(10)).Return(nil).Times(batchSize)
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(1), int64(10)).Return(nil).Times(batchSize)
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(),
		[]rankingdomain.UserPointDelta{{UserID: 1, Points: 10}},
		[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 10}}).Return(nil).Times(batchSize)

	w := d.newWorkerWithConcurrency(t, batchSize, concurrency)
	listed, applied, err := w.ApplyPerEventForTest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, batchSize, listed)
	assert.Equal(t, batchSize, applied)
}

// TestWorker_applyPerEvent_フォールバック経路_claim不可はスキップ は、他 goroutine / 他 worker が
// 先に確保したイベント（found=false）をエラーにせず読み飛ばすことを確認する。
func TestWorker_applyPerEvent_フォールバック経路_claim不可はスキップ(t *testing.T) {
	t.Parallel()

	const (
		batchSize   = 2
		concurrency = 2
	)

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	invokeDoInTx(t, d.tx)

	ev1 := newScoreEvent(t, 1, 1, 1, 10)
	ev2 := newScoreEvent(t, 2, 1, 1, 10)
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(batchSize), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{ev1, ev2}, nil)

	// ev1 は確保できる、ev2 は他が処理済み（found=false）。
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(2), uint32(testMaxRetry)).
		Return(outboxdomain.Event{}, false, nil)
	// 副作用は ev1 のぶんだけ発生し、ev2 では一切発生しない。
	expectPerEventApply(d, ev1, 1, 1, 10)

	w := d.newWorkerWithConcurrency(t, batchSize, concurrency)
	listed, applied, err := w.ApplyPerEventForTest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, batchSize, listed)
	// found=false も「この候補はもう我々の担当ではない」ため前進として数える。
	assert.Equal(t, batchSize, applied)
}

// ---------------------------------------------------------------------------
// Run ループ（通知購読・ポーリング・滞留時の抑止・ティック期限）
//
// 並びは docs/testing/outbox-worker.md §1 のテスト仕様表と対応する。
// ---------------------------------------------------------------------------

// [§1 ケース1] TestWorker_Run_Subscribe_failure は Subscribe 失敗時にポーリングのみで
// 継続することを確認する。
func TestWorker_Run_Subscribe_failure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	sub := mockoutbox.NewMockSubscriber(ctrl)

	sub.EXPECT().Subscribe(gomock.Any()).Return(nil, errors.New("subscribe failed"))
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	w := workeroutbox.New(workeroutbox.Config{
		MaxRetry: testMaxRetry,
		Repo:     d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx, Subscriber: sub,
		Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 100,
	})

	runWorkerAndWaitCalls(t, w, called, 1)
}

// [§1 ケース2] TestWorker_Run_notify_channel_closed は通知チャネルがクローズされた後
// ポーリングのみで継続することを確認する。
func TestWorker_Run_notify_channel_closed(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	sub := mockoutbox.NewMockSubscriber(ctrl)

	notifyCh := make(chan struct{})
	close(notifyCh)
	sub.EXPECT().Subscribe(gomock.Any()).Return((<-chan struct{})(notifyCh), nil)

	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	w := workeroutbox.New(workeroutbox.Config{
		MaxRetry: testMaxRetry,
		Repo:     d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx, Subscriber: sub,
		Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 100,
	})

	// 初回 runOnce が走ったことだけ確認できれば close 後の notifyCh = nil ループ継続が示される
	// （閉じた notifyCh から無限に runOnce が走らないことは AnyTimes + 後続 stopAndWait で担保）。
	runWorkerAndWaitCalls(t, w, called, 1)
}

// [§1 ケース3] TestWorker_Run_notify_triggered は通知チャネル経由で drainNow が起動することを
// 確認する。wall-clock sleep ではなく DoInTx 呼び出し回数のシグナルで同期する。
func TestWorker_Run_notify_triggered(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	sub := mockoutbox.NewMockSubscriber(ctrl)

	notifyCh := make(chan struct{}, 1)
	notifyCh <- struct{}{}
	sub.EXPECT().Subscribe(gomock.Any()).Return((<-chan struct{})(notifyCh), nil)

	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	w := workeroutbox.New(workeroutbox.Config{
		MaxRetry: testMaxRetry,
		Repo:     d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx, Subscriber: sub,
		Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 100,
	})

	// 初回起動 + 通知駆動の合計 2 回呼ばれるまで決定的に待つ。
	runWorkerAndWaitCalls(t, w, called, 2)
}

// [§1 ケース4] TestWorker_Run_ticker_driven は ticker 駆動で drainNow が走ることを確認する。
func TestWorker_Run_ticker_driven(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)

	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(t, d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	ctx, cancel := context.WithCancel(context.Background())
	w := workeroutbox.New(workeroutbox.Config{
		MaxRetry: testMaxRetry,
		Repo:     d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx,
		// 短い間隔にしても wall-clock 依存ではなくシグナル待ちで同期するため flaky にならない。
		Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
	})

	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	// 初回 + ticker 駆動の最低 2 回。timeout は 2 秒なので CI 高負荷でも余裕を持って届く。
	waitForCalls(t, called, 2, 2*time.Second)
	// cancel 後の残シグナルで DoInTx の送信がブロックしないよう、終了まで受信を続ける。
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-called:
			case <-done:
				return
			}
		}
	}()
	stopAndWait(t, cancel, done)
	<-drained
}

// [§1 ケース5・6] TestWorker_Run_ティック処理の失敗はループを止めない は、drainNow が失敗しても
// Run がエラーを返さずポーリングを継続することを確認する。
//
// 一過性の DB/Redis 障害で worker プロセスが落ちてはならない、という運用上の要件。
// ticker 経路と notify 経路で呼び分けの分岐が独立しているため、両方を通す。
func TestWorker_Run_ティック処理の失敗はループを止めない(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// notifyDriven が true なら通知チャネル経由でティックを起こす。
		notifyDriven bool
	}{
		{name: "ticker 経由で runOnce が失敗しても継続する"},
		{name: "通知経由で runOnce が失敗しても継続する", notifyDriven: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			repo := mockoutbox.NewMockRepository(ctrl)
			store := mockranking.NewMockRankingStore(ctrl)
			tx := mockshared.NewMockTransactor(ctrl)

			called := make(chan struct{}, 16)
			invokeDoInTxAndSignal(t, tx, called)
			// 毎ティック失敗させる。Run はエラーを返さずログのみで継続するはず。
			repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any(), uint32(testMaxRetry)).
				Return(nil, errors.New("db down")).
				AnyTimes()

			cfg := workeroutbox.Config{
				MaxRetry: testMaxRetry,
				Repo:     repo, RankingStore: store, Tx: tx,
				Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
			}

			notifyCh := make(chan struct{}, 8)
			if tt.notifyDriven {
				sub := mockoutbox.NewMockSubscriber(ctrl)
				sub.EXPECT().Subscribe(gomock.Any()).Return((<-chan struct{})(notifyCh), nil)
				cfg.Subscriber = sub
				// ticker 待ちではなく通知でティックを起こす。
				cfg.PollInterval = time.Hour
			}

			ctx, cancel := context.WithCancel(context.Background())
			w := workeroutbox.New(cfg)

			done := make(chan struct{})
			go func() {
				_ = w.Run(ctx)
				close(done)
			}()

			if tt.notifyDriven {
				// 初回実行ぶんを消費してから通知でもう1回起こす。
				waitForCalls(t, called, 1, 2*time.Second)
				notifyCh <- struct{}{}
				waitForCalls(t, called, 1, 2*time.Second)
			} else {
				// 初回 + ticker 駆動で最低 2 回。
				waitForCalls(t, called, 2, 2*time.Second)
			}

			stopAndWait(t, cancel, done)
		})
	}
}

// [§1 ケース7] TestWorker_Run_滞留中は通知を捨てtickerで再開する は、
// 「窓が恒久失敗イベントで埋まって前進しない」状態（滞留）のあいだ、通知駆動のドレインを
// 抑止することを確認する。
//
// OutboxSubscriber はバッファ1のチャネルへノンブロッキング送信するため、API の書き込みが
// 続く限り Run の select にはほぼ常に通知が入っている。滞留中に通知で再入すると、
// スリープを挟まず同じ窓を読み直し続けることになり、ドレインを打ち切っただけでは
// ビジーループが止まらない（docs/testing/outbox-worker.md §0-3）。
//
// 抑止は ticker で解除する。滞留中は新規イベントも窓の外にあって処理できないため、
// 通知を捨てても失うものはない。
func TestWorker_Run_滞留中は通知を捨てtickerで再開する(t *testing.T) {
	t.Parallel()

	// stalledDeps は「取得件数 = batchSize かつ全件デコード不能」を返す依存一式を作る。
	// 1ドレインあたり DoInTx は「バッチ tx 1 + retry 記録 tx 1」= 2 回。
	// t は呼び出し元のサブテストのものを受け取る（外側の t を閉じ込めると、
	// 分離レベル検証の失敗が親テストに紐づいてしまうため）。
	stalledDeps := func(t *testing.T, ctrl *gomock.Controller, called chan struct{}, listTimes int) deps {
		t.Helper()
		d := newDeps(ctrl)
		invokeDoInTxAndSignal(t, d.tx, called)
		ev := newUnknownEvent(1)
		list := d.outboxRepo.EXPECT().
			ListPending(gomock.Any(), gomock.Any(), int32(1), uint32(testMaxRetry)).
			Return([]outboxdomain.Event{ev}, nil)
		retry := d.outboxRepo.EXPECT().
			IncrementRetry(gomock.Any(), gomock.Any(), uint64(1), gomock.Any()).
			Return(nil)
		if listTimes > 0 {
			list.Times(listTimes)
			retry.Times(listTimes)
		} else {
			list.AnyTimes()
			retry.AnyTimes()
		}
		return d
	}

	t.Run("滞留中の通知は捨てられる", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		called := make(chan struct{}, 16)
		// ListPending はちょうど1回（初回ドレイン）だけ。通知で再入すれば2回目が発生し、
		// strict モックが未設定呼び出しとして検知する。
		d := stalledDeps(t, ctrl, called, 1)

		notifyCh := make(chan struct{}, 1)
		sub := mockoutbox.NewMockSubscriber(ctrl)
		sub.EXPECT().Subscribe(gomock.Any()).Return((<-chan struct{})(notifyCh), nil)

		w := workeroutbox.New(workeroutbox.Config{
			MaxRetry: testMaxRetry,
			Repo:     d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx, Subscriber: sub,
			// ticker では再開してしまうので、この部分テストでは発火させない。
			Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 1, Concurrency: 1,
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			_ = w.Run(ctx)
			close(done)
		}()

		// 初回ドレイン（バッチ tx + retry tx）が終わって滞留状態になるまで待つ。
		waitForCalls(t, called, 2, 2*time.Second)

		notifyCh <- struct{}{}

		// 抑止されていれば追加の DoInTx は発生しない。
		// 抑止が壊れた場合は ListPending の2回目で gomock が検知する（この待ちはその猶予）。
		select {
		case <-called:
			t.Fatal("滞留中の通知でドレインが再入した（ビジーループの原因になる）")
		case <-time.After(300 * time.Millisecond):
		}

		stopAndWait(t, cancel, done)
	})

	t.Run("滞留後も ticker では再開する", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		called := make(chan struct{}, 64)
		d := stalledDeps(t, ctrl, called, 0)

		w := workeroutbox.New(workeroutbox.Config{
			MaxRetry: testMaxRetry,
			Repo:     d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx,
			Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond,
			BatchSize: 1, Concurrency: 1,
		})

		// 1ドレイン = DoInTx 2回。6回に達すること自体が
		// 「滞留していても ticker では再開している」ことの証明になる。
		runWorkerAndWaitCalls(t, w, called, 6)
	})
}

// [§1 ケース8] TestWorker_drainNow_ティック期限切れ後も自走する は、tickTimeout でティックが
// 打ち切られても次の通知/ポーリングを待たずに処理を継続することを確認する。
//
// これがないと、通知が途切れたタイミングで残りのバックログが pollInterval（既定10分）の
// あいだ完全に停止する。実測で 8,959 件が 440 秒間放置される事象が発生したため回帰テストを置く。
// Run が drainNow ではなく runOnce を直接呼ぶ実装に戻すと、最初のティック期限切れで止まり
// このテストは待機タイムアウトで失敗する。
func TestWorker_drainNow_ティック期限切れ後も自走する(t *testing.T) {
	t.Parallel()

	const batchSize = 1

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 64)
	invokeDoInTxAndSignal(t, d.tx, called)

	ev := newScoreEvent(t, 1, 1, 1, 10)

	// batchSize ちょうどを前進させながら返し続ける = 何度ドレインしても枯れない状態。
	// tickTimeout を 1ns にすることで runOnce は毎回「期限切れで未枯渇のまま復帰」する。
	// wall-clock sleep に頼らず deadline を確実に踏ませるための値。
	d.outboxRepo.EXPECT().
		ListPending(gomock.Any(), gomock.Any(), int32(batchSize), uint32(testMaxRetry)).
		Return([]outboxdomain.Event{ev}, nil).
		AnyTimes()
	d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{1}).Return(nil).AnyTimes()
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	w := workeroutbox.New(workeroutbox.Config{
		MaxRetry:     testMaxRetry,
		Repo:         d.outboxRepo,
		RankingRepo:  d.rankingRepo,
		RankingStore: d.store,
		Tx:           d.tx,
		Logger:       slogtest.NewLogger(t, nil),
		// ポーリングでの再開に頼っていないことを示すため十分大きくする。
		// 自走していなければ最初のティック期限切れで止まり、待機がタイムアウトする。
		PollInterval: time.Hour,
		BatchSize:    batchSize,
		Concurrency:  1,
		TickTimeout:  time.Nanosecond,
	})

	// 1ティックあたり DoInTx は1回のため、20回に達すること自体が
	// 「期限切れ後に自走してティックを重ねている」ことの証明になる。
	runWorkerAndWaitCalls(t, w, called, 20)
}
