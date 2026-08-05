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

// invokeDoInTx は DoInTx 呼び出しごとに fn(nil) を実行するだけのヘルパー。
// Worker のメソッドを直接呼ぶ（Run を経由しない）テストで使う。
func invokeDoInTx(tx *mockshared.MockTransactor) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error, _ ...shared.TxOption) error {
			return fn(nil)
		}).
		AnyTimes()
}

// invokeDoInTxAndSignal は DoInTx 呼び出しごとに fn(nil) を実行し、called チャネルへ通知する
// ヘルパー。runOnce は1ティックで「バッチ適用 tx」「デコード不能イベントの retry 記録 tx」など
// 複数回 DoInTx を呼びうるため、AnyTimes で登録し、呼び出し回数はテスト側で
// called チャネルの受信数を見て検証する。
// wall-clock sleep に頼らず「N 回呼ばれるまで待つ」同期点を作るために使う。
func invokeDoInTxAndSignal(tx *mockshared.MockTransactor, called chan<- struct{}) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error, _ ...shared.TxOption) error {
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
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error, _ ...shared.TxOption) error {
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
	})
}

// pendingEmptyAnyTimes は ListPending が「候補なし」を任意回数返すよう設定する。
func pendingEmptyAnyTimes(outboxRepo *mockoutbox.MockRepository) {
	outboxRepo.EXPECT().
		ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, nil).
		AnyTimes()
}

// ---------------------------------------------------------------------------
// 主経路（applyBatch: バッチ単位トランザクション）
// ---------------------------------------------------------------------------

// TestWorker_applyBatch_同一ギルドは合算され履歴はイベント単位で作られる は
// バッチ集約（buildBatchWork）の責務を検証する。
// スコア加算は可換なのでギルド／ユーザー単位に合算して DB 往復を減らせるが、
// 履歴は「誰がいつ何点入れたか」を残す必要があるためイベント単位のまま保持する。
// MarkProcessedByIDs が処理対象の全 ID を受け取ることもここで確認する。
func TestWorker_applyBatch_同一ギルドは合算され履歴はイベント単位で作られる(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	// guild=1 に2件、guild=2 に1件。user=10 は guild をまたいで2件。
	events := []outboxdomain.Event{
		newScoreEvent(t, 1, 1, 10, 100),
		newScoreEvent(t, 2, 1, 11, 200),
		newScoreEvent(t, 3, 2, 10, 50),
	}
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return(events, nil)

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
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, users []rankingdomain.UserPointDelta, guilds []rankingdomain.GuildScoreDelta) error {
			gotUserDeltas, gotRedisGuildDeps = users, guilds
			return nil
		})
	d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ shared.Tx, ids []uint64) error {
			gotIDs = ids
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

// TestWorker_applyBatch_適用順序 は MySQL（source of truth）2本 → Redis（キャッシュ）→
// MarkProcessedByIDs の順で適用されることを固定する。
// Redis を最後に置くことで、Redis 失敗時にバッチ tx がロールバックされ MySQL も巻き戻り、
// 未マークのまま次ティックで再適用される（exactly-once）。
func TestWorker_applyBatch_適用順序_MySQL2本_Redis_MarkProcessedByIDs(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	ev := newScoreEvent(t, 1, 1, 10, 100)
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
		Return([]outboxdomain.Event{ev}, nil)

	// 1件だけなので集約結果は決定的。引数まで含めて順序を固定する。
	guildDeltas := []rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 100}}
	gomock.InOrder(
		d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), guildDeltas).Return(nil),
		d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(),
			[]rankingdomain.GuildScoreHistoryEntry{{GuildID: 1, UserID: 10, Points: 100}}).Return(nil),
		d.store.EXPECT().ApplyScoreDeltas(gomock.Any(),
			[]rankingdomain.UserPointDelta{{UserID: 10, Points: 100}}, guildDeltas).Return(nil),
		d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{1}).Return(nil),
	)

	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 1)
}

// TestWorker_applyBatch_デコード不能イベントは除外され個別にIncrementRetryされる は、
// 未知の event_type / 壊れた payload を「決定的な失敗」とみなしてバッチから外し、
// 正常なイベントだけを適用することを検証する。
//
// 旧実装（イベント単位）では未知 event_type は handleEvent の default で弾かれていたが、
// バッチ適用ではデコード段階で除外され、フォールバック経路には回らない。
// retry 記録はバッチ commit 後に別 tx で行う（バッチ tx に混ぜると巻き添えで
// ロールバックされ、retry_count が増えないため）。
func TestWorker_applyBatch_デコード不能イベントは除外され個別にIncrementRetryされる(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	ok := newScoreEvent(t, 1, 1, 10, 100)
	unknownType := outboxdomain.Event{ID: 2, Type: "unknown_event", Payload: []byte("{}"), RetryCount: 3}
	brokenPayload := outboxdomain.Event{
		ID: 3, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: []byte("{broken"),
	}

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
		Return([]outboxdomain.Event{ok, unknownType, brokenPayload}, nil)

	// 正常な1件だけが適用され、マーク対象も ID=1 のみ。
	d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(),
		[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 100}}).Return(nil)
	d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(),
		[]rankingdomain.GuildScoreHistoryEntry{{GuildID: 1, UserID: 10, Points: 100}}).Return(nil)
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(),
		[]rankingdomain.UserPointDelta{{UserID: 10, Points: 100}},
		[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 100}}).Return(nil)
	d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{1}).Return(nil)

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

// TestWorker_applyBatch_全件デコード不能ならMySQLもRedisも呼ばれない は、
// 適用対象が1件も残らない場合に副作用を一切発行しないことを確認する。
// マーク対象が空のまま MarkProcessedByIDs / bulk 系を呼ぶと無駄な DB 往復になる。
func TestWorker_applyBatch_全件デコード不能ならMySQLもRedisも呼ばれない(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	ev := outboxdomain.Event{ID: 99, Type: "unknown_event", Payload: []byte("{}")}
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
		Return([]outboxdomain.Event{ev}, nil)
	// bulk 系 / ApplyScoreDeltas / MarkProcessedByIDs は EXPECT しない
	// （呼ばれたら gomock が未設定呼び出しとして落とす）。
	d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(99), gomock.Any()).Return(nil)

	// バッチ tx 1回 + retry 記録 tx 1回。
	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 2)
}

// TestWorker_applyBatch_IncrementRetry失敗はログのみ は、retry 記録自体が失敗しても
// ティックがエラーにならず（イベントは pending のままなので次ティックで再処理される）
// worker が継続することを確認する。
func TestWorker_applyBatch_IncrementRetry失敗はログのみ(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	ev := outboxdomain.Event{ID: 50, Type: "unknown_event", Payload: []byte("{}")}
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
		Return([]outboxdomain.Event{ev}, nil)
	d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(50), gomock.Any()).
		Return(errors.New("retry failed"))

	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 2)
}

// TestWorker_applyBatch_pending無しは即座に終了する は候補ゼロ件で副作用を発行しないことを確認する。
func TestWorker_applyBatch_pending無しは即座に終了する(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)
	pendingEmptyAnyTimes(d.outboxRepo)

	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 1)
}

// TestWorker_applyBatch_各ステップの失敗でフォールバック経路に切り替わる は、バッチ適用の
// どのステップで失敗しても
//   - 同一バッチ内の以降のステップが実行されない（＝tx がロールバックされ
//     MarkProcessedByIDs に到達しない = イベントを取りこぼさない）
//   - その後イベント単位のフォールバック経路へ切り替わり、処理が前進する
//
// ことを検証する。
//
// 失敗時にイベントを処理済みにしてしまうとイベントが失われるため、
// 「失敗したら絶対にバッチでマークしない」ことが本経路の最重要不変条件。
// バッチ内ステップ（Bulk* / ApplyScoreDeltas / MarkProcessedByIDs）は各ケースで
// 明示的に EXPECT したものしか設定しないため、余分に呼ばれれば gomock が検知する。
func TestWorker_applyBatch_各ステップの失敗でフォールバック経路に切り替わる(t *testing.T) {
	t.Parallel()

	errStep := errors.New("step failed")

	tests := []struct {
		name  string
		setup func(t *testing.T, d deps, guildDeltas []rankingdomain.GuildScoreDelta,
			histories []rankingdomain.GuildScoreHistoryEntry, userDeltas []rankingdomain.UserPointDelta)
	}{
		{
			name: "BulkIncrementGuildScores 失敗（履歴/Redis/マークには進まない）",
			setup: func(_ *testing.T, d deps, guildDeltas []rankingdomain.GuildScoreDelta,
				_ []rankingdomain.GuildScoreHistoryEntry, _ []rankingdomain.UserPointDelta) {
				d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), guildDeltas).Return(errStep)
			},
		},
		{
			name: "BulkInsertGuildScoreHistories 失敗（Redis / マークには進まない）",
			setup: func(_ *testing.T, d deps, guildDeltas []rankingdomain.GuildScoreDelta,
				histories []rankingdomain.GuildScoreHistoryEntry, _ []rankingdomain.UserPointDelta) {
				d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), guildDeltas).Return(nil)
				d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), histories).Return(errStep)
			},
		},
		{
			name: "Redis ApplyScoreDeltas 失敗（MySQL は tx ロールバックで巻き戻り、マークもされない）",
			setup: func(_ *testing.T, d deps, guildDeltas []rankingdomain.GuildScoreDelta,
				histories []rankingdomain.GuildScoreHistoryEntry, userDeltas []rankingdomain.UserPointDelta) {
				d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), guildDeltas).Return(nil)
				d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), histories).Return(nil)
				d.store.EXPECT().ApplyScoreDeltas(gomock.Any(), userDeltas, guildDeltas).Return(errStep)
			},
		},
		{
			name: "MarkProcessedByIDs 失敗（tx ロールバックで全副作用が巻き戻る）",
			setup: func(_ *testing.T, d deps, guildDeltas []rankingdomain.GuildScoreDelta,
				histories []rankingdomain.GuildScoreHistoryEntry, userDeltas []rankingdomain.UserPointDelta) {
				d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), guildDeltas).Return(nil)
				d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), histories).Return(nil)
				d.store.EXPECT().ApplyScoreDeltas(gomock.Any(), userDeltas, guildDeltas).Return(nil)
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
			invokeDoInTxAndSignal(d.tx, called)

			ev := newScoreEvent(t, 1, 1, 10, 100)
			// applyBatch（主経路）と listCandidates（フォールバック）で1回ずつ呼ばれる。
			d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
				Return([]outboxdomain.Event{ev}, nil).Times(2)

			tt.setup(t, d,
				[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 100}},
				[]rankingdomain.GuildScoreHistoryEntry{{GuildID: 1, UserID: 10, Points: 100}},
				[]rankingdomain.UserPointDelta{{UserID: 10, Points: 100}},
			)

			// フォールバック経路（イベント単位 tx）が同じイベントを処理しきる。
			// 単数形の IncrementGuildScore / InsertGuildScoreHistory / MarkProcessed が
			// 呼ばれること自体が、経路の切り替わりが起きた証拠になる。
			d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(1)).Return(ev, true, nil)
			d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(100)).Return(nil)
			d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(10), int64(100)).Return(nil)
			d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(10), int64(100)).Return(nil)
			d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(100)).Return(nil)
			d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(1)).Return(nil)

			// バッチ適用 tx + listCandidates tx + processOne tx。
			runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 3)
		})
	}
}

// TestWorker_runOnce_バッチ失敗時はフォールバックへ切り替わり処理が前進する は、
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
	invokeDoInTxAndSignal(d.tx, called)

	// 集約結果を決定的にするため同一 guild/user のイベントを2件用意する。
	ev1 := newScoreEvent(t, 1, 1, 10, 100)
	ev2 := newScoreEvent(t, 2, 1, 10, 100)
	events := []outboxdomain.Event{ev1, ev2}
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return(events, nil).Times(2)

	// 主経路のバッチ適用は失敗する（例: 一括 upsert のデッドロック）。
	d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(),
		[]rankingdomain.GuildScoreDelta{{GuildID: 1, Points: 200}}).
		Return(errors.New("deadlock found when trying to get lock"))
	// バッチ経路の以降のステップには進まない（EXPECT を置かないため呼ばれたら失敗する）。

	// フォールバック経路で各イベントが個別に claim → 適用 → MarkProcessed される。
	marked := make(chan uint64, len(events))
	for _, ev := range events {
		d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), ev.ID).Return(ev, true, nil)
		d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), ev.ID).
			DoAndReturn(func(_ context.Context, _ shared.Tx, id uint64) error {
				marked <- id
				return nil
			})
	}
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(100)).Return(nil).Times(len(events))
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(10), int64(100)).Return(nil).Times(len(events))
	d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(10), int64(100)).Return(nil).Times(len(events))
	d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(100)).Return(nil).Times(len(events))

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

// TestWorker_runOnce_ListPendingエラー は候補取得自体の失敗でティックが即座に中断されることを確認する。
// 副作用にも retry 記録にも到達しない。
func TestWorker_runOnce_ListPendingエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	invokeDoInTxAndSignal(d.tx, called)

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
		Return(nil, errors.New("db down")).AnyTimes()
	// bulk 系 / IncrementRetry / MarkProcessedByIDs は呼ばれない

	runWorkerAndWaitCalls(t, d.newWorker(t, 100), called, 1)
}

// TestWorker_runOnce_候補が枯れるまでドレインする は、ListPending が batchSize ちょうどを
// 返す限り1ティック内でバッチ適用を繰り返し、batchSize 未満になった時点で止まることを確認する。
// batchSize で打ち切ると、通知が途切れた時点で残りが pollInterval 待ちになり
// バックログの消化が停止するため、この挙動が必要。
func TestWorker_runOnce_候補が枯れるまでドレインする(t *testing.T) {
	t.Parallel()

	const batchSize = 2

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	called := make(chan struct{}, 16)
	// バッチ単位 tx なので 1巡 = DoInTx 1回。
	// 1巡目: ちょうど batchSize 件 → 継続、2巡目: batchSize 未満 → 打ち切り。
	// Times(2) により3巡目が走らない（＝打ち切っている）ことも担保する。
	const wantDoInTx = 2
	invokeDoInTxAndSignalTimes(d.tx, called, wantDoInTx)

	// 集約結果を決定的にするため全イベントを同一 guild/user にする。
	ev1, ev2, ev3 := newScoreEvent(t, 1, 1, 1, 10), newScoreEvent(t, 2, 1, 1, 10), newScoreEvent(t, 3, 1, 1, 10)

	// limit=batchSize で呼ばれること、および「ちょうど batchSize 件 → 継続」
	// 「batchSize 未満 → 打ち切り」の順で返ることを gomock.InOrder で固定する。
	gomock.InOrder(
		d.outboxRepo.EXPECT().
			ListPending(gomock.Any(), gomock.Any(), int32(batchSize)).
			Return([]outboxdomain.Event{ev1, ev2}, nil),
		d.outboxRepo.EXPECT().
			ListPending(gomock.Any(), gomock.Any(), int32(batchSize)).
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

// TestWorker_drainNow_ティック期限切れ後も自走する は、tickTimeout でティックが打ち切られても
// 次の通知/ポーリングを待たずに処理を継続することを確認する。
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
	invokeDoInTxAndSignal(d.tx, called)

	ev := newScoreEvent(t, 1, 1, 1, 10)

	// batchSize ちょうどを返し続ける = 何度ドレインしても枯れない状態。
	// tickTimeout を 1ns にすることで runOnce は毎回「期限切れで未枯渇のまま復帰」する。
	// wall-clock sleep に頼らず deadline を確実に踏ませるための値。
	d.outboxRepo.EXPECT().
		ListPending(gomock.Any(), gomock.Any(), int32(batchSize)).
		Return([]outboxdomain.Event{ev}, nil).
		AnyTimes()
	d.rankingRepo.EXPECT().BulkIncrementGuildScores(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	d.rankingRepo.EXPECT().BulkInsertGuildScoreHistories(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	d.store.EXPECT().ApplyScoreDeltas(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	d.outboxRepo.EXPECT().MarkProcessedByIDs(gomock.Any(), gomock.Any(), []uint64{1}).Return(nil).AnyTimes()

	w := workeroutbox.New(workeroutbox.Config{
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

// ---------------------------------------------------------------------------
// フォールバック経路（applyPerEvent: イベント単位トランザクション）
//
// runOnce が applyBatch の失敗を検知して切り替える経路。切り替わること自体は
// TestWorker_runOnce_バッチ失敗時はフォールバックへ切り替わり処理が前進する で検証済みのため、
// 以下は ApplyPerEventForTest 経由で経路内部の分岐を決定的に検証する
// （seam を置く理由は export_test.go のコメント参照）。
// ---------------------------------------------------------------------------

// TestWorker_applyPerEvent_フォールバック経路_正常系_異常系 はイベント単位トランザクションの
// dispatch 挙動を検証する。ListPending で候補を取得 → ClaimByID(id) で確保 → handleEvent →
// MarkProcessed の流れと、handleEvent 失敗時の IncrementRetry 記録を確認する。
func TestWorker_applyPerEvent_フォールバック経路_正常系_異常系(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker
		wantCount int
	}{
		{
			name: "正常系: MySQL(IncrementGuildScore→InsertGuildScoreHistory)→Redis(IncrementUserPoints→IncrementGuildScore)→MarkProcessed",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				ev := newScoreEvent(t, 1, 1, 10, 500)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(1)).Return(ev, true, nil)

				gomock.InOrder(
					d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(500)).Return(nil),
					d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(10), int64(500)).Return(nil),
					d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(10), int64(500)).Return(nil),
					d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(500)).Return(nil),
				)
				d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(1)).Return(nil)

				return d.newWorker(t, 100)
			},
			wantCount: 1,
		},
		{
			// フォールバック経路の存在意義そのものの検証。
			// 候補列の先頭イベントが恒久失敗（poison）しても、後続の候補は影響を受けず処理される。
			name: "正常系: 先頭イベントが失敗しても後続の候補は処理される（head-of-line blocking 回避）",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				poison := newScoreEvent(t, 1, 2, 20, 100)
				ok := newScoreEvent(t, 2, 3, 21, 200)

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

				return d.newWorker(t, 100)
			},
			wantCount: 2,
		},
		{
			name: "正常系: ClaimByID が found=false（処理済み/他worker確保中）なら skip する",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				ev := newScoreEvent(t, 5, 1, 10, 500)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(5)).
					Return(outboxdomain.Event{}, false, nil)
				// handleEvent / MarkProcessed / IncrementRetry は呼ばれない

				return d.newWorker(t, 100)
			},
			wantCount: 1,
		},
		{
			name: "正常系: 候補なしなら 0 件を返し tx を張らない",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)
				pendingEmptyAnyTimes(d.outboxRepo)

				return d.newWorker(t, 100)
			},
			wantCount: 0,
		},
		{
			name: "異常系: MySQL IncrementGuildScore 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				ev := newScoreEvent(t, 7, 1, 10, 500)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(7)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(500)).
					Return(errors.New("mysql down"))
				// InsertGuildScoreHistory / Redis 系は呼ばれない
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(7), gomock.Any()).Return(nil)

				return d.newWorker(t, 100)
			},
			wantCount: 1,
		},
		{
			name: "異常系: InsertGuildScoreHistory 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				ev := newScoreEvent(t, 8, 2, 11, 300)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(8)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(2), int64(300)).Return(nil)
				d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(2), int64(11), int64(300)).
					Return(errors.New("mysql history down"))
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(8), gomock.Any()).Return(nil)

				return d.newWorker(t, 100)
			},
			wantCount: 1,
		},
		{
			name: "異常系: Redis IncrementUserPoints 失敗で IncrementRetry（MySQL は tx ロールバックで巻き戻る）",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				ev := newScoreEvent(t, 9, 3, 12, 200)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(9)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(3), int64(200)).Return(nil)
				d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(3), int64(12), int64(200)).Return(nil)
				d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(12), int64(200)).
					Return(errors.New("redis down"))
				// store.IncrementGuildScore は呼ばれない
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(9), gomock.Any()).Return(nil)

				return d.newWorker(t, 100)
			},
			wantCount: 1,
		},
		{
			name: "異常系: Redis IncrementGuildScore 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				ev := newScoreEvent(t, 10, 4, 13, 100)
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(10)).Return(ev, true, nil)

				d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(4), int64(100)).Return(nil)
				d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(4), int64(13), int64(100)).Return(nil)
				d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(13), int64(100)).Return(nil)
				d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(4), int64(100)).
					Return(errors.New("redis guild down"))
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(10), gomock.Any()).Return(nil)

				return d.newWorker(t, 100)
			},
			wantCount: 1,
		},
		{
			// 主経路（applyBatch）では未知 event_type はデコード段階で除外されるが、
			// フォールバック経路では handleEvent の default に落ちて同じく IncrementRetry される。
			name: "異常系: 未知の event_type は ErrUnknownEventType で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				ev := outboxdomain.Event{ID: 99, Type: "unknown_event", Payload: []byte("{}")}
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(99)).Return(ev, true, nil)
				// last_error には ErrUnknownEventType のセンチネル文言が残る。
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(99),
					gomock.Cond(func(s string) bool {
						return strings.Contains(s, outboxdomain.ErrUnknownEventType.Error())
					})).Return(nil)

				return d.newWorker(t, 100)
			},
			wantCount: 1,
		},
		{
			// payload が壊れたイベントも handleEvent 内の Unmarshal 失敗として retry 記録される。
			name: "異常系: payload デコード失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				ev := outboxdomain.Event{
					ID: 100, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: []byte("{broken"),
				}
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(100)).Return(ev, true, nil)
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(100), gomock.Any()).Return(nil)

				return d.newWorker(t, 100)
			},
			wantCount: 1,
		},
		{
			// retry 記録自体の失敗はログのみ。イベントは pending のままなので次ティックで再処理される。
			name: "異常系: IncrementRetry 失敗はログのみでエラーを伝播しない",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				d := newDeps(ctrl)
				invokeDoInTx(d.tx)

				ev := outboxdomain.Event{ID: 50, Type: "unknown_event", Payload: []byte("{}")}
				d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
					Return([]outboxdomain.Event{ev}, nil)
				d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(50)).Return(ev, true, nil)
				d.outboxRepo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(50), gomock.Any()).
					Return(errors.New("retry failed"))

				return d.newWorker(t, 100)
			},
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			w := tt.setup(t, ctrl)

			n, err := w.ApplyPerEventForTest(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tt.wantCount, n)
		})
	}
}

// TestWorker_applyPerEvent_フォールバック経路_ListPendingエラー は候補取得自体の失敗で
// エラーが伝播し、claim / handle に到達しないことを確認する。
func TestWorker_applyPerEvent_フォールバック経路_ListPendingエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	invokeDoInTx(d.tx)

	errDB := errors.New("db down")
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).Return(nil, errDB)
	// ClaimByID / IncrementRetry / MarkProcessed は呼ばれない

	n, err := d.newWorker(t, 100).ApplyPerEventForTest(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, errDB)
	assert.Equal(t, 0, n)
}

// TestWorker_applyPerEvent_フォールバック経路_ClaimByIDエラー は claim 自体の失敗
// （handleEvent 以外のインフラ起因エラー）でティックが打ち切られることを確認する。
// handleEvent の失敗ではないため IncrementRetry は呼ばれない。
func TestWorker_applyPerEvent_フォールバック経路_ClaimByIDエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	invokeDoInTx(d.tx)

	ev := newScoreEvent(t, 42, 1, 1, 100)
	errClaim := errors.New("claim db down")
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
		Return([]outboxdomain.Event{ev}, nil)
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(42)).
		Return(outboxdomain.Event{}, false, errClaim)
	// IncrementRetry / MarkProcessed は呼ばれない

	n, err := d.newWorker(t, 100).ApplyPerEventForTest(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, errClaim)
	assert.Equal(t, 0, n)
}

// TestWorker_applyPerEvent_フォールバック経路_MarkProcessedエラー は MarkProcessed 失敗時に
// ティックが打ち切られ、IncrementRetry は呼ばれず（handleEvent 自体は成功しているため）、
// 後続候補も処理されないことを確認する。
func TestWorker_applyPerEvent_フォールバック経路_MarkProcessedエラー(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)
	invokeDoInTx(d.tx)

	ev1 := newScoreEvent(t, 42, 1, 1, 100)
	ev2 := newScoreEvent(t, 43, 2, 2, 200)
	errMark := errors.New("mark failed")

	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(100)).
		Return([]outboxdomain.Event{ev1, ev2}, nil)
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(42)).Return(ev1, true, nil)
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(100)).Return(nil)
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(1), int64(100)).Return(nil)
	d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(1), int64(100)).Return(nil)
	d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(100)).Return(nil)
	d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(42)).Return(errMark)
	// claim/mark の失敗のため IncrementRetry は呼ばれない。
	// concurrency=1 なので ev1 でティックが中断し ev2 の ClaimByID は呼ばれない。

	n, err := d.newWorker(t, 100).ApplyPerEventForTest(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, errMark)
	assert.Equal(t, 0, n)
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
	invokeDoInTx(d.tx)

	events := make([]outboxdomain.Event, 0, batchSize)
	for id := uint64(1); id <= batchSize; id++ {
		events = append(events, newScoreEvent(t, id, 1, 1, 10))
	}
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(batchSize)).Return(events, nil)

	// 各 ID はちょうど1回ずつ claim / mark されること（Times は既定で1回）。
	for _, ev := range events {
		d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), ev.ID).Return(ev, true, nil)
		d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), ev.ID).Return(nil)
	}
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(10)).Return(nil).Times(batchSize)
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(1), int64(10)).Return(nil).Times(batchSize)
	d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(1), int64(10)).Return(nil).Times(batchSize)
	d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(10)).Return(nil).Times(batchSize)

	w := d.newWorkerWithConcurrency(t, batchSize, concurrency)
	n, err := w.ApplyPerEventForTest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, batchSize, n)
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
	invokeDoInTx(d.tx)

	ev1 := newScoreEvent(t, 1, 1, 1, 10)
	ev2 := newScoreEvent(t, 2, 1, 1, 10)
	d.outboxRepo.EXPECT().ListPending(gomock.Any(), gomock.Any(), int32(batchSize)).
		Return([]outboxdomain.Event{ev1, ev2}, nil)

	// ev1 は確保できる、ev2 は他が処理済み（found=false）。
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(1)).Return(ev1, true, nil)
	d.outboxRepo.EXPECT().ClaimByID(gomock.Any(), gomock.Any(), uint64(2)).
		Return(outboxdomain.Event{}, false, nil)

	// 副作用は ev1 のぶんだけ発生し、ev2 では一切発生しない。
	d.rankingRepo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), int64(1), int64(10)).Return(nil)
	d.rankingRepo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), int64(1), int64(1), int64(10)).Return(nil)
	d.store.EXPECT().IncrementUserPoints(gomock.Any(), int64(1), int64(10)).Return(nil)
	d.store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(10)).Return(nil)
	d.outboxRepo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(1)).Return(nil)

	w := d.newWorkerWithConcurrency(t, batchSize, concurrency)
	n, err := w.ApplyPerEventForTest(context.Background())
	require.NoError(t, err)
	assert.Equal(t, batchSize, n)
}

// ---------------------------------------------------------------------------
// Run ループ（通知購読・ポーリング・ティック期限）
// ---------------------------------------------------------------------------

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

// TestWorker_runOnce_appliesTickTimeout は TickTimeout > 0 のとき
// runOnce が deadline 付き context を DoInTx へ渡すことを確認する。
// これにより DB/Redis のブロッキングでループがハングしない。
func TestWorker_runOnce_appliesTickTimeout(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newDeps(ctrl)

	gotDeadline := make(chan bool, 1)
	d.tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, fn func(shared.Tx) error, _ ...shared.TxOption) error {
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
