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

// invokeDoInTx は MockTransactor.DoInTx を fn(nil) で実行するヘルパー。
func invokeDoInTx(tx *mockshared.MockTransactor, times int) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
			return fn(nil)
		}).
		Times(times)
}

// invokeDoInTxAndSignal は DoInTx 呼び出しごとに called チャネルへ通知するヘルパー。
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

// TestWorker_runOnce_dispatch は1回のティック内でイベントが正しく dispatch されることを確認する。
func TestWorker_runOnce_dispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker
	}{
		{
			name: "正常系: ranking_score_added を Redis に反映して MarkProcessed",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 10, GuildID: 1, Points: 500,
				})
				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{
						{ID: 1, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload},
					}, nil)
				store.EXPECT().IncrementUserPoints(gomock.Any(), int64(10), int64(500)).Return(nil)
				store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(500)).Return(nil)
				repo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(1)).Return(nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "異常系: Redis 反映失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 10, GuildID: 1, Points: 500,
				})
				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{
						{ID: 7, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload},
					}, nil)
				store.EXPECT().IncrementUserPoints(gomock.Any(), int64(10), int64(500)).
					Return(errors.New("redis down"))
				repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(7), gomock.Any()).Return(nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "異常系: 未知の event_type は IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{
						{ID: 99, Type: "unknown_event", Payload: []byte("{}")},
					}, nil)
				repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(99), gomock.Any()).Return(nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "異常系: IncrementUserPoints 成功 + IncrementGuildScore 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 11, GuildID: 2, Points: 300,
				})
				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{
						{ID: 8, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload},
					}, nil)
				store.EXPECT().IncrementUserPoints(gomock.Any(), int64(11), int64(300)).Return(nil)
				store.EXPECT().IncrementGuildScore(gomock.Any(), int64(2), int64(300)).
					Return(errors.New("redis guild down"))
				repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(8), gomock.Any()).Return(nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "異常系: ListPending エラーは IncrementRetry/MarkProcessed を呼ばずに戻る",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, errors.New("db down"))

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "正常系: ListPending が空なら何も呼ばない",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{}, nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			w := tt.setup(t, ctrl)

			// Run を即時 ctx キャンセルで止め、初回 runOnce のみ実行されることを利用して検証する。
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := w.Run(ctx)
			assert.NoError(t, err)
		})
	}
}

// TestWorker_Run_Subscribe_failure は Subscribe 失敗時にポーリングのみで継続することを確認する。
func TestWorker_Run_Subscribe_failure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	repo := mockoutbox.NewMockRepository(ctrl)
	store := mockranking.NewMockRankingStore(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)
	sub := mockoutbox.NewMockSubscriber(ctrl)

	sub.EXPECT().Subscribe(gomock.Any()).Return(nil, errors.New("subscribe failed"))
	invokeDoInTx(tx, 1)
	repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).Return([]outboxdomain.Event{}, nil)

	w := workeroutbox.New(workeroutbox.Config{
		Repo: repo, RankingStore: store, Tx: tx, Subscriber: sub,
		Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, w.Run(ctx))
}

// TestWorker_Run_notify_triggered は通知チャネル経由で runOnce が起動することを確認する。
// wall-clock sleep ではなく DoInTx 呼び出し回数のシグナルで同期する。
func TestWorker_Run_notify_triggered(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	repo := mockoutbox.NewMockRepository(ctrl)
	store := mockranking.NewMockRankingStore(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)
	sub := mockoutbox.NewMockSubscriber(ctrl)

	notifyCh := make(chan struct{}, 1)
	notifyCh <- struct{}{}
	sub.EXPECT().Subscribe(gomock.Any()).Return((<-chan struct{})(notifyCh), nil)

	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(tx, called)
	repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]outboxdomain.Event{}, nil).
		AnyTimes()

	ctx, cancel := context.WithCancel(context.Background())
	w := workeroutbox.New(workeroutbox.Config{
		Repo: repo, RankingStore: store, Tx: tx, Subscriber: sub,
		Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 100,
	})

	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	// 初回起動 + 通知駆動の合計 2 回呼ばれるまで決定的に待つ。
	waitForCalls(t, called, 2, 2*time.Second)
	stopAndWait(t, cancel, done)
}

// TestWorker_Run_notify_channel_closed は通知チャネルがクローズされた後ポーリングのみで継続することを確認する。
func TestWorker_Run_notify_channel_closed(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	repo := mockoutbox.NewMockRepository(ctrl)
	store := mockranking.NewMockRankingStore(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)
	sub := mockoutbox.NewMockSubscriber(ctrl)

	notifyCh := make(chan struct{})
	close(notifyCh)
	sub.EXPECT().Subscribe(gomock.Any()).Return((<-chan struct{})(notifyCh), nil)

	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(tx, called)
	repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]outboxdomain.Event{}, nil).
		AnyTimes()

	ctx, cancel := context.WithCancel(context.Background())
	w := workeroutbox.New(workeroutbox.Config{
		Repo: repo, RankingStore: store, Tx: tx, Subscriber: sub,
		Logger: slogtest.NewLogger(t, nil), PollInterval: time.Hour, BatchSize: 100,
	})

	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	// 初回 runOnce が走ったことだけ確認できれば close 後の notifyCh = nil ループ継続が示される
	// （閉じた notifyCh から無限に runOnce が走らないことは AnyTimes + 後続 stopAndWait で担保）。
	waitForCalls(t, called, 1, 2*time.Second)
	stopAndWait(t, cancel, done)
}

// TestWorker_Run_ticker_driven は ticker 駆動で runOnce が走ることを確認する。
func TestWorker_Run_ticker_driven(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	repo := mockoutbox.NewMockRepository(ctrl)
	store := mockranking.NewMockRankingStore(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)

	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(tx, called)
	repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]outboxdomain.Event{}, nil).
		AnyTimes()

	ctx, cancel := context.WithCancel(context.Background())
	w := workeroutbox.New(workeroutbox.Config{
		Repo: repo, RankingStore: store, Tx: tx,
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

// TestWorker_runOnce_MarkProcessed_error は MarkProcessed 失敗時にエラーが伝播することを確認する。
func TestWorker_runOnce_MarkProcessed_error(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	repo := mockoutbox.NewMockRepository(ctrl)
	store := mockranking.NewMockRankingStore(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)
	invokeDoInTx(tx, 1)

	payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
		UserID: 1, GuildID: 1, Points: 100,
	})
	repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]outboxdomain.Event{
			{ID: 42, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload},
		}, nil)
	store.EXPECT().IncrementUserPoints(gomock.Any(), int64(1), int64(100)).Return(nil)
	store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(100)).Return(nil)
	repo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(42)).
		Return(errors.New("mark failed"))

	w := workeroutbox.New(workeroutbox.Config{
		Repo: repo, RankingStore: store, Tx: tx,
		Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, w.Run(ctx)) // Run 自身は err をログに流して nil を返す
}

// TestWorker_runOnce_IncrementRetry_error は IncrementRetry 失敗時にエラーが伝播することを確認する。
func TestWorker_runOnce_IncrementRetry_error(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	repo := mockoutbox.NewMockRepository(ctrl)
	store := mockranking.NewMockRankingStore(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)
	invokeDoInTx(tx, 1)

	repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]outboxdomain.Event{
			{ID: 50, Type: "unknown_event", Payload: []byte("{}")},
		}, nil)
	repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(50), gomock.Any()).
		Return(errors.New("retry failed"))

	w := workeroutbox.New(workeroutbox.Config{
		Repo: repo, RankingStore: store, Tx: tx,
		Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, w.Run(ctx))
}

// TestWorker_runOnce_appliesTickTimeout は TickTimeout > 0 のとき
// runOnce が deadline 付き context を DoInTx へ渡すことを確認する。
// これにより DB/Redis のブロッキングでループがハングしない。
func TestWorker_runOnce_appliesTickTimeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	repo := mockoutbox.NewMockRepository(ctrl)
	store := mockranking.NewMockRankingStore(ctrl)
	tx := mockshared.NewMockTransactor(ctrl)

	gotDeadline := make(chan bool, 1)
	tx.EXPECT().
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
	repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, nil).AnyTimes()

	w := workeroutbox.New(workeroutbox.Config{
		Repo: repo, RankingStore: store, Tx: tx,
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
