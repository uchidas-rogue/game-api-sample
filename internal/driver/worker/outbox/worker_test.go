package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	workeroutbox "github.com/uchidas-rogue/game-api-sample/internal/driver/worker/outbox"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockoutbox "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox/mock"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
	mockshared "github.com/uchidas-rogue/game-api-sample/internal/usecase/shared/mock"
)

// invokeDoInTxAndSignal は DoInTx 呼び出しごとに fn(nil) を実行し、called チャネルへ通知する
// ヘルパー。runOnce は1ティックで「候補取得 tx」「候補ごとの claim/handle/mark tx」
// 「失敗時の retry 記録 tx」と複数回 DoInTx を呼びうるため、AnyTimes で登録し、
// 呼び出し回数はテスト側で called チャネルの受信数を見て検証する。
// wall-clock sleep に頼らず「N 回呼ばれるまで待つ」同期点を作るために使う。
// called はバッファ十分大に確保しておく前提（送信ブロックを避ける）。
func invokeDoInTxAndSignal(tx *mockshared.MockTransactor, called chan<- struct{}) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
			err := fn(nil)
			called <- struct{}{}
			return err
		}).
		AnyTimes()
}

// invokeDoInTxAndSignalTimes は呼び出し回数を厳密に固定したいケース向けの signal 付きヘルパー。
// batchSize による打ち切りなど「ちょうど N 回だけ呼ばれる」ことを strict モックで担保したい場合に使う。
func invokeDoInTxAndSignalTimes(tx *mockshared.MockTransactor, called chan<- struct{}, times int) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
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
	stopAndWait(t, cancel, done)
}

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

func (d deps) newWorker(t *testing.T, batchSize int) *workeroutbox.Worker {
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
	})
}

// pendingEmptyAnyTimes は ListPending が「候補なし」を任意回数返すよう設定する。
func pendingEmptyAnyTimes(outboxRepo *mockoutbox.MockRepository) {
	outboxRepo.EXPECT().
		ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, nil).
		AnyTimes()
}

// TestWorker_processOne_正常系_異常系 はイベント単位トランザクションの dispatch 挙動を検証する。
// MySQL 先→Redis 後の順序、失敗時の IncrementRetry 記録を確認する。
// ListPending で候補を1件取得 → ClaimByID(id) で確保 → handleEvent → MarkProcessed の
// 新フロー（ListPending + ClaimByID）に基づく。
func TestWorker_processOne_正常系_異常系(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{})
		wantSignals int
	}{
		{
			name: "正常系: MySQL(IncrementGuildScore→InsertGuildScoreHistory)→Redis(IncrementUserPoints→IncrementGuildScore)→MarkProcessed",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{}) {
				t.Helper()
				d := newDeps(ctrl)
				called := make(chan struct{}, 16)
				invokeDoInTxAndSignal(d.tx, called)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 10, GuildID: 1, Points: 500,
				})
				ev := outboxdomain.Event{ID: 1, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload}

				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(1)).Return(ev, true, nil)

				gomock.InOrder(
					d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(500)).Return(nil),
					d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(10), int64(500)).Return(nil),
					d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(10), int64(500)).Return(nil),
					d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(500)).Return(nil),
				)
				d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(1)).Return(nil)

				return d.newWorker(t, 100), called
			},
			wantSignals: 2, // ListPending tx + processOne tx
		},
		{
			// head-of-line blocking 回避の検証（今回の修正の主目的）。
			// 候補列の先頭イベントが恒久失敗（poison）しても、後続の候補は影響を受けず処理される。
			name: "正常系: 先頭イベントが失敗しても後続の候補は処理される（head-of-line blocking 回避）",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{}) {
				t.Helper()
				d := newDeps(ctrl)
				called := make(chan struct{}, 16)
				invokeDoInTxAndSignal(d.tx, called)

				poisonPayload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 20, GuildID: 2, Points: 100,
				})
				okPayload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 21, GuildID: 3, Points: 200,
				})
				poison := outboxdomain.Event{ID: 1, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: poisonPayload}
				ok := outboxdomain.Event{ID: 2, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: okPayload}

				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{poison, ok}, nil)

				// 先頭イベント: claim 成功だが handleEvent（MySQL 加算）が失敗 → IncrementRetry。
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(1)).Return(poison, true, nil)
				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(2), int64(100)).
					Return(errors.New("mysql down (poison)"))
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(1), gomock.Any()).Return(nil)

				// 後続イベント: 先頭の失敗の影響を受けず claim → handleEvent → MarkProcessed が成功する。
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(2)).Return(ok, true, nil)
				gomock.InOrder(
					d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(3), int64(200)).Return(nil),
					d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(3), int64(21), int64(200)).Return(nil),
					d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(21), int64(200)).Return(nil),
					d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(3), int64(200)).Return(nil),
				)
				d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(2)).Return(nil)

				return d.newWorker(t, 100), called
			},
			// ListPending + poison claim tx + retry tx + ok claim tx = 4
			wantSignals: 4,
		},
		{
			name: "正常系: ClaimByID が found=false（処理済み/他worker確保中）なら skip する",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{}) {
				t.Helper()
				d := newDeps(ctrl)
				called := make(chan struct{}, 16)
				invokeDoInTxAndSignal(d.tx, called)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 10, GuildID: 1, Points: 500,
				})
				ev := outboxdomain.Event{ID: 5, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload}

				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(5)).Return(outboxdomain.Event{}, false, nil)
				// handleEvent / MarkProcessed / IncrementRetry は呼ばれない

				return d.newWorker(t, 100), called
			},
			wantSignals: 2, // ListPending tx + claim tx（skip）
		},
		{
			name: "異常系: MySQL IncrementGuildScore 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{}) {
				t.Helper()
				d := newDeps(ctrl)
				called := make(chan struct{}, 16)
				invokeDoInTxAndSignal(d.tx, called)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 10, GuildID: 1, Points: 500,
				})
				ev := outboxdomain.Event{ID: 7, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload}

				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(7)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(500)).
					Return(errors.New("mysql down"))
				// InsertGuildScoreHistory / Redis 系は呼ばれない
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(7), gomock.Any()).Return(nil)

				return d.newWorker(t, 100), called
			},
			wantSignals: 3, // ListPending + claim(handle失敗) + retry記録
		},
		{
			name: "異常系: InsertGuildScoreHistory 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{}) {
				t.Helper()
				d := newDeps(ctrl)
				called := make(chan struct{}, 16)
				invokeDoInTxAndSignal(d.tx, called)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 11, GuildID: 2, Points: 300,
				})
				ev := outboxdomain.Event{ID: 8, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload}

				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(8)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(2), int64(300)).Return(nil)
				d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(2), int64(11), int64(300)).
					Return(errors.New("mysql history down"))
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(8), gomock.Any()).Return(nil)

				return d.newWorker(t, 100), called
			},
			wantSignals: 3,
		},
		{
			name: "異常系: Redis IncrementUserPoints 失敗で IncrementRetry（MySQL は先に成功済み）",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{}) {
				t.Helper()
				d := newDeps(ctrl)
				called := make(chan struct{}, 16)
				invokeDoInTxAndSignal(d.tx, called)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 12, GuildID: 3, Points: 200,
				})
				ev := outboxdomain.Event{ID: 9, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload}

				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(9)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(3), int64(200)).Return(nil)
				d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(3), int64(12), int64(200)).Return(nil)
				d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(12), int64(200)).
					Return(errors.New("redis down"))
				// IncrementGuildScore(store) は呼ばれない
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(9), gomock.Any()).Return(nil)

				return d.newWorker(t, 100), called
			},
			wantSignals: 3,
		},
		{
			name: "異常系: Redis IncrementGuildScore 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{}) {
				t.Helper()
				d := newDeps(ctrl)
				called := make(chan struct{}, 16)
				invokeDoInTxAndSignal(d.tx, called)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 13, GuildID: 4, Points: 100,
				})
				ev := outboxdomain.Event{ID: 10, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload}

				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(10)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(4), int64(100)).Return(nil)
				d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(4), int64(13), int64(100)).Return(nil)
				d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(13), int64(100)).Return(nil)
				d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(4), int64(100)).
					Return(errors.New("redis guild down"))
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(10), gomock.Any()).Return(nil)

				return d.newWorker(t, 100), called
			},
			wantSignals: 3,
		},
		{
			name: "異常系: 未知の event_type は ErrUnknownEventType で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{}) {
				t.Helper()
				d := newDeps(ctrl)
				called := make(chan struct{}, 16)
				invokeDoInTxAndSignal(d.tx, called)

				ev := outboxdomain.Event{ID: 99, Type: "unknown_event", Payload: []byte("{}")}
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(99)).Return(ev, true, nil)
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(99), gomock.Any()).Return(nil)

				return d.newWorker(t, 100), called
			},
			wantSignals: 3,
		},
		{
			name: "正常系: pending 無しは即座に終了する（ListPending 1回のみ）",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*workeroutbox.Worker, chan struct{}) {
				t.Helper()
				d := newDeps(ctrl)
				called := make(chan struct{}, 16)
				invokeDoInTxAndSignal(d.tx, called)

				pendingEmptyAnyTimes(d.outboxRepo)

				return d.newWorker(t, 100), called
			},
			wantSignals: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			w, called := tt.setup(t, ctrl)

			runWorkerAndWaitCalls(t, w, called, tt.wantSignals)
		})
	}
}

// TestWorker_runOnce_ListPendingエラー は候補取得自体の失敗でティックが即座に中断されることを確認する。
// claim/handle には到達しないため IncrementRetry も呼ばれない。
func TestWorker_runOnce_ListPendingエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
		Return(nil, errors.New("db down"))
	// ClaimByID / IncrementRetry / MarkProcessed は呼ばれない

	w := d.newWorker(t, 100)
	runWorkerAndWaitCalls(t, w, called, 1)
}

// TestWorker_runOnce_ClaimByIDエラー は claim 自体の失敗（handleEvent 以外）で
// そのティックが即座に中断されることを確認する。IncrementRetry は呼ばれない。
func TestWorker_runOnce_ClaimByIDエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
		UserID: 1, GuildID: 1, Points: 100,
	})
	ev := outboxdomain.Event{ID: 42, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload}

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev}, nil)
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(42)).
		Return(outboxdomain.Event{}, false, errors.New("claim db down"))
	// IncrementRetry / MarkProcessed は呼ばれない

	w := d.newWorker(t, 100)
	runWorkerAndWaitCalls(t, w, called, 2)
}

// TestWorker_runOnce_MarkProcessedエラー は MarkProcessed 失敗時にそのティックが打ち切られ、
// IncrementRetry は呼ばれないこと（handleEvent 自体は成功しているため）、
// および後続候補が処理されないことを確認する。
func TestWorker_runOnce_MarkProcessedエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	payload1 := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
		UserID: 1, GuildID: 1, Points: 100,
	})
	payload2 := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
		UserID: 2, GuildID: 2, Points: 200,
	})
	ev1 := outboxdomain.Event{ID: 42, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload1}
	ev2 := outboxdomain.Event{ID: 43, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload2}

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev1, ev2}, nil)
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(42)).Return(ev1, true, nil)
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(100)).Return(nil)
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(1), int64(100)).Return(nil)
	d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(1), int64(100)).Return(nil)
	d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(100)).Return(nil)
	d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(42)).
		Return(errors.New("mark failed"))
	// claim/mark の失敗のため IncrementRetry は呼ばれない。
	// ev1 の処理でティックが中断するため ev2 の ClaimByID は呼ばれない。

	w := d.newWorker(t, 100)
	runWorkerAndWaitCalls(t, w, called, 2) // ListPending + ev1 processOne
}

// TestWorker_runOnce_IncrementRetry失敗はログのみ は retry 記録自体が失敗しても
// processOne がエラーを伝播させず次のティックへ継続することを確認する。
func TestWorker_runOnce_IncrementRetry失敗はログのみ(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	ev := outboxdomain.Event{ID: 50, Type: "unknown_event", Payload: []byte("{}")}
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return([]outboxdomain.Event{ev}, nil)
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(50)).Return(ev, true, nil)
	d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(50), gomock.Any()).
		Return(errors.New("retry failed"))

	w := d.newWorker(t, 100)
	// ListPending + claim(handle失敗) + retry記録(失敗するがログのみ) の3回
	runWorkerAndWaitCalls(t, w, called, 3)
}

// TestWorker_runOnce_batchSizeで打ち切り はListPending の limit 引数が batchSize になっており、
// 返却された候補件数ぶんだけ処理されることを確認する（件数の実際の絞り込みは repository 側の責務）。
func TestWorker_runOnce_batchSizeで打ち切り(t *testing.T) {
	t.Parallel()

	const batchSize = 2

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	// ListPending tx 1回 + 候補ごとの processOne tx(batchSize回) = batchSize+1 回に固定し、
	// それ以上呼ばれたらモックが即座に失敗する。
	invokeDoInTxAndSignalTimes(d.tx, called, batchSize+1)

	payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
		UserID: 1, GuildID: 1, Points: 10,
	})
	ev1 := outboxdomain.Event{ID: 1, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload}
	ev2 := outboxdomain.Event{ID: 2, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload}

	// ListPending は limit=batchSize で呼ばれ、ちょうど batchSize 件の候補を返す
	// （3件目以降が存在しても本ティックでは処理されないシナリオ）。
	d.outboxRepo.EXPECT().
		ListPending(gomock.Any(), gomock.Any(), int32(batchSize)).
		Return([]outboxdomain.Event{ev1, ev2}, nil)

	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(1)).Return(ev1, true, nil)
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(2)).Return(ev2, true, nil)
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(10)).Return(nil).Times(batchSize)
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(1), int64(10)).Return(nil).Times(batchSize)
	d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(1), int64(10)).Return(nil).Times(batchSize)
	d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(10)).Return(nil).Times(batchSize)
	d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(1)).Return(nil)
	d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(2)).Return(nil)

	w := d.newWorker(t, batchSize)
	runWorkerAndWaitCalls(t, w, called, batchSize+1)
}

// TestWorker_Run_Subscribe_failure は Subscribe 失敗時にポーリングのみで継続することを確認する。
func TestWorker_Run_Subscribe_failure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	sub := mockoutbox.NewMockSubscriber(ctrl)

	sub.EXPECT().Subscribe(gomock.Any()).Return(nil, errors.New("subscribe failed"))
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	w := workeroutbox.New(workeroutbox.Config{
		Repo: d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx, Subscriber: sub,
		Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 100,
	})

	runWorkerAndWaitCalls(t, w, called, 1)
}

// TestWorker_Run_notify_triggered は通知チャネル経由で runOnce が起動することを確認する。
// wall-clock sleep ではなく DoInTx 呼び出し回数のシグナルで同期する。
func TestWorker_Run_notify_triggered(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	sub := mockoutbox.NewMockSubscriber(ctrl)

	notifyCh := make(chan struct{}, 1)
	notifyCh <- struct{}{}
	sub.EXPECT().Subscribe(gomock.Any()).Return((<-chan struct{})(notifyCh), nil)

	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	w := workeroutbox.New(workeroutbox.Config{
		Repo: d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx, Subscriber: sub,
		Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 100,
	})

	// 初回起動 + 通知駆動の合計 2 回呼ばれるまで決定的に待つ。
	runWorkerAndWaitCalls(t, w, called, 2)
}

// TestWorker_Run_notify_channel_closed は通知チャネルがクローズされた後ポーリングのみで継続することを確認する。
func TestWorker_Run_notify_channel_closed(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	sub := mockoutbox.NewMockSubscriber(ctrl)

	notifyCh := make(chan struct{})
	close(notifyCh)
	sub.EXPECT().Subscribe(gomock.Any()).Return((<-chan struct{})(notifyCh), nil)

	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	w := workeroutbox.New(workeroutbox.Config{
		Repo: d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx, Subscriber: sub,
		Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 100,
	})

	// 初回 runOnce が走ったことだけ確認できれば close 後の notifyCh = nil ループ継続が示される
	// （閉じた notifyCh から無限に runOnce が走らないことは AnyTimes + 後続 stopAndWait で担保）。
	runWorkerAndWaitCalls(t, w, called, 1)
}

// TestWorker_Run_ticker_driven は ticker 駆動で runOnce が走ることを確認する。
func TestWorker_Run_ticker_driven(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)

	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	ctx, cancel := context.WithCancel(context.Background())
	w := workeroutbox.New(workeroutbox.Config{
		Repo: d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx,
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
	stopAndWait(t, cancel, done)
}

// TestWorker_runOnce_appliesTickTimeout は TickTimeout > 0 のとき
// runOnce が deadline 付き context を DoInTx へ渡すことを確認する。
// これにより DB/Redis のブロッキングでループがハングしない。
func TestWorker_runOnce_appliesTickTimeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)

	gotDeadline := make(chan bool, 1)
	d.tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(shared.Tx) error) error {
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
		Repo: d.outboxRepo, RankingRepo: d.rankingRepo, RankingStore: d.store, Tx: d.tx,
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
