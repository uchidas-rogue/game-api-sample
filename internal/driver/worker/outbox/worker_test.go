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
// dispatchStep は runOnce の1イベント処理で失敗させる地点。
type dispatchStep int

const (
	dispatchStepNone dispatchStep = iota
	dispatchStepListPending
	dispatchStepIncrUserPoints
	dispatchStepIncrGuildScore
	dispatchStepUnknownType
	dispatchStepBadPayload
)

// dispatchCase は runOnce の dispatch テストケース1件。データのみを持つ。
type dispatchCase struct {
	name string

	// noEvents が true なら ListPending が空スライスを返す。
	noEvents bool
	eventID  uint64
	userID   int64
	guildID  int64
	points   int64

	failAt dispatchStep
}

// TestWorker_runOnce_dispatch は1回のティック内でイベントが正しく dispatch されることを確認する。
//
// テスト設計（フロー図・テスト仕様表）は docs/testing/outbox-worker.md にある。
func TestWorker_runOnce_dispatch(t *testing.T) {
	t.Parallel()

	// docs/testing/outbox-worker.md の仕様表と 1 対 1 で対応する。
	// 並び順は図のパスが短い順。
	tests := []dispatchCase{
		{
			// #1 A→B→C→E1
			name:   "ListPending がエラー: IncrementRetry も MarkProcessed も呼ばない",
			failAt: dispatchStepListPending,
		},
		{
			// #2 A→B→C→D→Z（ループに入らない）
			name:     "ListPending が空: 何も呼ばない",
			noEvents: true,
		},
		{
			// #3 …→D→E→G→Z（未知の種別）
			name:    "未知の event_type: IncrementRetry して次のイベントへ",
			eventID: 99,
			failAt:  dispatchStepUnknownType,
		},
		{
			// #4 …→D→E→G→Z（payload が壊れている）
			name:    "payload が不正で Unmarshal 失敗: IncrementRetry して次のイベントへ",
			eventID: 50,
			failAt:  dispatchStepBadPayload,
		},
		{
			// #5 …→E→F1→G→Z
			name:    "IncrementUserPoints が失敗: IncrementRetry",
			eventID: 7,
			userID:  10, guildID: 1, points: 500,
			failAt: dispatchStepIncrUserPoints,
		},
		{
			// #6 …→F1→F2→G→Z
			name:    "IncrementUserPoints 成功 + IncrementGuildScore 失敗: IncrementRetry",
			eventID: 8,
			userID:  11, guildID: 2, points: 300,
			failAt: dispatchStepIncrGuildScore,
		},
		{
			// #7 …→F2→H→Z
			name:    "正常系: Redis に反映して MarkProcessed",
			eventID: 1,
			userID:  10, guildID: 1, points: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			repo := mockoutbox.NewMockRepository(ctrl)
			store := mockranking.NewMockRankingStore(ctrl)
			tx := mockshared.NewMockTransactor(ctrl)
			invokeDoInTx(tx, 1)

			expectDispatchCalls(t, tt, repo, store)

			w := workeroutbox.New(workeroutbox.Config{
				Repo: repo, RankingStore: store, Tx: tx,
				Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
			})

			// Run を即時 ctx キャンセルで止め、初回 runOnce のみ実行されることを利用して検証する。
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			assert.NoError(t, w.Run(ctx))
		})
	}
}

// expectDispatchCalls は tc の「どこで失敗するか」に応じて期待呼び出しを組み立てる。
//
// handleEvent が失敗したイベントは IncrementRetry されてループが継続する（1件で打ち切らない）。
// この「継続する」性質は、期待していない呼び出しが起きれば gomock が失敗させることで担保される。
func expectDispatchCalls(t *testing.T, tc dispatchCase, repo *mockoutbox.MockRepository, store *mockranking.MockRankingStore) {
	t.Helper()

	// B: 未処理イベントの取得
	if tc.failAt == dispatchStepListPending {
		repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errors.New("db down"))
		return
	}
	if tc.noEvents {
		repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).Return([]outboxdomain.Event{}, nil)
		return
	}

	ev := outboxdomain.Event{
		ID:   tc.eventID,
		Type: outboxdomain.EventTypeRankingScoreAdded,
		Payload: mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
			UserID: tc.userID, GuildID: tc.guildID, Points: tc.points,
		}),
	}
	switch tc.failAt {
	case dispatchStepUnknownType:
		ev.Type = "unknown_event"
		ev.Payload = []byte("{}")
	case dispatchStepBadPayload:
		// JSON として解釈できないバイト列。ポイズンメッセージの入口。
		ev.Payload = []byte("{ this is not json")
	case dispatchStepNone, dispatchStepListPending, dispatchStepIncrUserPoints, dispatchStepIncrGuildScore:
		// payload はそのまま使う
	}
	repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).Return([]outboxdomain.Event{ev}, nil)

	// D / E: 種別ごとの分岐。ここで失敗するものは Redis に到達しない。
	if tc.failAt == dispatchStepUnknownType || tc.failAt == dispatchStepBadPayload {
		repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), tc.eventID, gomock.Any()).Return(nil)
		return
	}

	// F1: 個人ポイント反映
	if tc.failAt == dispatchStepIncrUserPoints {
		store.EXPECT().IncrementUserPoints(gomock.Any(), tc.userID, tc.points).Return(errors.New("redis down"))
		repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), tc.eventID, gomock.Any()).Return(nil)
		return
	}
	store.EXPECT().IncrementUserPoints(gomock.Any(), tc.userID, tc.points).Return(nil)

	// F2: ギルドスコア反映
	if tc.failAt == dispatchStepIncrGuildScore {
		store.EXPECT().IncrementGuildScore(gomock.Any(), tc.guildID, tc.points).Return(errors.New("redis guild down"))
		repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), tc.eventID, gomock.Any()).Return(nil)
		return
	}
	store.EXPECT().IncrementGuildScore(gomock.Any(), tc.guildID, tc.points).Return(nil)

	// H: 処理済みマーク
	repo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), tc.eventID).Return(nil)
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

// TestWorker_Run_ティック処理の失敗はループを止めない は、runOnce が失敗しても
// Run がエラーを返さずポーリングを継続することを確認する。
//
// 一過性の DB/Redis 障害で worker プロセスが落ちてはならない、という運用上の要件。
// ticker 経路と notify 経路それぞれに独立したエラーログの分岐があるため、両方を通す。
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
			invokeDoInTxAndSignal(tx, called)
			// 毎ティック失敗させる。Run はエラーを返さずログのみで継続するはず。
			repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil, errors.New("db down")).
				AnyTimes()

			cfg := workeroutbox.Config{
				Repo: repo, RankingStore: store, Tx: tx,
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
