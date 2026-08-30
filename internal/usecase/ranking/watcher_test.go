package ranking_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
)

// ---------------------------------------------------------------------------
// RankingWatcher（ランキング更新通知のファンアウトハブ）のテスト。
//
// 設計（フロー図・テスト仕様表）は docs/testing/ranking-watch.md にある。
// 分岐を追加・変更したら、まず図と表を更新してからここを直す。
//
// 同期は wall-clock sleep ではなく「モックが呼ばれた」「チャネルに値が来た」で取る。
// 通知チャネルを **バッファなし** にしてあるのが要点で、`notify <- struct{}{}` が
// 返った時点でハブが受信済みであることが保証される。2回連続で送れば1回目の
// broadcast が完了していることまで保証できる（フェンス）。
// ---------------------------------------------------------------------------

// watchTimeout は「起きるはずのこと」を待つ上限。CI の高負荷を見込み、
// ローカル想定（ミリ秒未満）の数百倍を取る（AGENTS.md §3 Flaky 防止）。
const watchTimeout = 2 * time.Second

// watchTestLimit はテストで使う購読 limit。NormalizeLimit の上限・下限に掛からない値。
const watchTestLimit = 5

// watchDeps は RankingWatcher の依存一式。
type watchDeps struct {
	uc  *mockranking.MockUsecase
	sub *mockranking.MockRankingUpdateSubscriber
	// notify はハブが購読する通知チャネル。バッファなし（同期点として使う）。
	notify chan struct{}
}

func newWatchDeps(ctrl *gomock.Controller) watchDeps {
	return watchDeps{
		uc:     mockranking.NewMockUsecase(ctrl),
		sub:    mockranking.NewMockRankingUpdateSubscriber(ctrl),
		notify: make(chan struct{}),
	}
}

// expectSubscribe は Subscribe が notify チャネルを返すよう設定する。
func (d watchDeps) expectSubscribe() {
	d.sub.EXPECT().Subscribe(gomock.Any()).DoAndReturn(
		func(context.Context) (<-chan struct{}, error) {
			return d.notify, nil
		})
}

// newWatcher は RankingWatcher を生成する。
func (d watchDeps) newWatcher(t *testing.T) *ranking.RankingWatcher {
	t.Helper()
	return ranking.NewRankingWatcher(d.uc, d.sub, slogtest.NewLogger(t, nil))
}

// newWatcherWithRecorder はログを捕捉する Logger 付きで RankingWatcher を生成する。
// 「ログが間引かれている」こと自体が仕様になっている検証にだけ使う。
func (d watchDeps) newWatcherWithRecorder(t *testing.T) (*ranking.RankingWatcher, *slogtest.Recorder) {
	t.Helper()
	logger, rec := slogtest.NewRecordingLogger(t, nil)
	return ranking.NewRankingWatcher(d.uc, d.sub, logger), rec
}

// startRun は Run をバックグラウンドで起動し、終了を待つ関数を返す。
// 返した stop は cancel してから Run の戻り値を返す（テスト終了時に必ず呼ぶ）。
func startRun(t *testing.T, w *ranking.RankingWatcher) (stop func() error) {
	t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(runCtx) }()

	var once sync.Once
	var runErr error
	return func() error {
		once.Do(func() {
			cancel()
			select {
			case runErr = <-errCh:
			case <-time.After(watchTimeout):
				t.Error("Run が cancel 後に終了しなかった")
			}
		})
		return runErr
	}
}

// rankings は検証用のランキング結果を組み立てる。score でスナップショットを識別する。
func rankings(score int64, n int) ranking.RankingsResult {
	entries := make([]rankingdomain.RankEntry, n)
	for i := range entries {
		entries[i] = rankingdomain.RankEntry{
			Rank:  int64(i + 1),
			ID:    int64(i + 1),
			Name:  "user" + strconv.Itoa(i+1),
			Score: score - int64(i),
		}
	}
	return ranking.RankingsResult{Rankings: entries, TotalCount: int64(n)}
}

// recvResult は購読チャネルから1件受け取る。届かなければ失敗させる。
func recvResult(t *testing.T, ch <-chan ranking.RankingsResult) ranking.RankingsResult {
	t.Helper()
	select {
	case v, ok := <-ch:
		require.True(t, ok, "購読チャネルが閉じられた（値が来ていない）")
		return v
	case <-time.After(watchTimeout):
		t.Fatal("購読チャネルに値が来なかった")
		return ranking.RankingsResult{}
	}
}

// assertClosed は購読チャネルが閉じられることを確認する。
// 残っている値は読み捨てる（クローズの検証が目的のため）。
// テスト goroutine 以外からも呼ぶため、失敗は t.Error で報告する（t.Fatal は使わない）。
func assertClosed(t *testing.T, ch <-chan ranking.RankingsResult) {
	t.Helper()
	deadline := time.After(watchTimeout)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Error("購読チャネルが閉じられなかった（goroutine / チャネルリーク）")
			return
		}
	}
}

// notifyOnce はハブへ通知を1件送る。バッファなしチャネルなので、
// 送信が返った時点でハブが受信済み。
func notifyOnce(t *testing.T, d watchDeps) {
	t.Helper()
	select {
	case d.notify <- struct{}{}:
	case <-time.After(watchTimeout):
		t.Fatal("ハブが通知を受け取らなかった")
	}
}

// notifyAndFence は通知を送り、その broadcast が完了するまで待つ。
// 2件目の送信が受理される = ハブが select に戻っている = 1件目の broadcast は完了、
// という関係を同期点に使う（2件目の broadcast は呼び出し側が織り込むこと）。
func notifyAndFence(t *testing.T, d watchDeps) {
	t.Helper()
	notifyOnce(t, d)
	notifyOnce(t, d)
}

// ---------------------------------------------------------------------------
// §1 WatchUserRankings（購読の登録と初回スナップショット）
// 並びは docs/testing/ranking-watch.md §1 の仕様表（図のパスが短い順）と対応する。
// ---------------------------------------------------------------------------

// [§1 ケース1] 停止済みのハブは購読を受け付けない。
// 下位層を触らないこと（fetch しないこと）も strict モックで担保する。
func TestRankingWatcher_WatchUserRankings_停止済みハブ_ErrWatcherStoppedを返す(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)

	stop := startRun(t, w)
	require.NoError(t, stop())

	ch, err := w.WatchUserRankings(context.Background(), watchTestLimit)

	require.ErrorIs(t, err, ranking.ErrWatcherStopped)
	assert.Nil(t, ch)
}

// [§1 ケース2] 初回 fetch の失敗ではチャネルを返さない。
// 登録も巻き戻すため、後続のブロードキャストが「居ない購読者」へ配ろうとしない。
func TestRankingWatcher_WatchUserRankings_初回fetch失敗_チャネルを返さない(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	errFetch := rankingdomain.ErrRankingUnavailable
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(ranking.RankingsResult{}, errFetch)

	ch, err := w.WatchUserRankings(context.Background(), watchTestLimit)

	require.ErrorIs(t, err, errFetch)
	assert.Nil(t, ch)

	// 登録が残っていれば、この通知で GetUserRankings がもう一度呼ばれて
	// strict モックが「未登録の呼び出し」として落とす。
	notifyAndFence(t, d)
}

// [§1 ケース3] 登録直後にブロードキャストが先着した場合、初回 fetch の結果は
// それより古い。素直に送ると「更新されたのに古いランキングが最後に残る」ため、
// 初回値は捨てる。
//
// 「先着」を再現するために、初回 fetch のモックの中で通知を1件流し、
// ブロードキャストの fetch が完了してから古い値を返す。
func TestRankingWatcher_WatchUserRankings_登録直後に更新が先着_古い初回値で上書きしない(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	const (
		staleScore = int64(100)
		freshScore = int64(999)
	)
	broadcastDone := make(chan struct{})

	// 1本目 = 初回スナップショット用。中でブロードキャストを完了させてから古い値を返す。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		DoAndReturn(func(context.Context, ranking.GetRankingsInput) (ranking.RankingsResult, error) {
			notifyOnce(t, d)
			select {
			case <-broadcastDone:
			case <-time.After(watchTimeout):
				t.Error("ブロードキャストが完了しなかった")
			}
			return rankings(staleScore, watchTestLimit), nil
		})

	// 2本目 = ブロードキャスト用。新しい値を返す。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		DoAndReturn(func(context.Context, ranking.GetRankingsInput) (ranking.RankingsResult, error) {
			defer close(broadcastDone)
			return rankings(freshScore, watchTestLimit), nil
		})

	ch, err := w.WatchUserRankings(context.Background(), watchTestLimit)
	require.NoError(t, err)

	got := recvResult(t, ch)
	assert.Equal(t, freshScore, got.Rankings[0].Score, "古い初回値で上書きしてはならない")
}

// [§1 ケース4] 正常系。購読登録の直後に現在値が1件だけ届き、
// limit は domain の NormalizeLimit を通ってから下位層へ渡る。
func TestRankingWatcher_WatchUserRankings_正常系_初回スナップショットを1件受け取る(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	// limit=0 は範囲外なので DefaultRankingLimit に正規化されて渡る。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: rankingdomain.DefaultRankingLimit}).
		Return(rankings(500, 3), nil)

	ch, err := w.WatchUserRankings(context.Background(), 0)
	require.NoError(t, err)

	got := recvResult(t, ch)
	assert.Len(t, got.Rankings, 3)
	assert.Equal(t, int64(500), got.Rankings[0].Score)

	// 初回スナップショットは1件だけ。2件目は来ない。
	select {
	case v := <-ch:
		t.Fatalf("初回スナップショットが2件届いた: %+v", v)
	default:
	}
}

// ---------------------------------------------------------------------------
// §2 Run（ハブの常駐ループ）
// ---------------------------------------------------------------------------

// [§2 ケース1] Subscribe の失敗ではループに入らずエラーを返す。
// 購読も受け付けない（ハブとして機能していないため）。
func TestRankingWatcher_Run_Subscribe失敗_エラーを返す(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	errSub := errors.New("redis down")
	d.sub.EXPECT().Subscribe(gomock.Any()).Return(nil, errSub)
	w := d.newWatcher(t)

	err := w.Run(context.Background())

	require.ErrorIs(t, err, errSub)
}

// [§2 ケース3] ctx キャンセルでは nil を返し、購読者のチャネルを閉じる。
// 閉じ忘れるとクライアント側の受信ループが永久に残る。
func TestRankingWatcher_Run_ctxキャンセル_購読者のチャネルを閉じてnilを返す(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)

	d.uc.EXPECT().GetUserRankings(gomock.Any(), gomock.Any()).Return(rankings(100, 3), nil)
	ch, err := w.WatchUserRankings(context.Background(), watchTestLimit)
	require.NoError(t, err)

	require.NoError(t, stop(), "ctx キャンセルは nil を返す")
	assertClosed(t, ch)
}

// [§2 ケース4] 通知チャネルが閉じたら ErrWatchSubscriptionClosed を返して終了する。
// ポーリングのフォールバックが無いため、黙って続けると「更新が永久に来ない
// ストリーム」になる。購読者のチャネルもここで閉じる。
func TestRankingWatcher_Run_通知チャネルが閉じる_購読者を切ってエラーを返す(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)

	errCh := make(chan error, 1)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { errCh <- w.Run(runCtx) }()

	d.uc.EXPECT().GetUserRankings(gomock.Any(), gomock.Any()).Return(rankings(100, 3), nil)
	ch, err := w.WatchUserRankings(context.Background(), watchTestLimit)
	require.NoError(t, err)

	close(d.notify)

	select {
	case runErr := <-errCh:
		require.ErrorIs(t, runErr, ranking.ErrWatchSubscriptionClosed)
	case <-time.After(watchTimeout):
		t.Fatal("購読断で Run が終了しなかった")
	}
	assertClosed(t, ch)
}

// ---------------------------------------------------------------------------
// §3 broadcast（通知1件ぶんの配信）
// ---------------------------------------------------------------------------

// [§3 ケース1] 購読者が居なければ下位層を触らない。
// 誰も見ていないランキングを通知のたびに読み直さないため。
//
// GetUserRankings は EXPECT しない。broadcast が読みに行けば strict モックが
// 「未登録の呼び出し」として即座に落とす。2回続けて通知を送ることで、
// 1件目の broadcast が完了している（ハブが select に戻っている）ことまで保証する。
func TestRankingWatcher_broadcast_購読者なし_下位層を読まない(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	notifyAndFence(t, d)
}

// [§3 ケース2] 前回と ID/Rank/Score が同じなら配らない。
// worker のバッチが空振り気味のときに同じ値を毎秒 push しないため。
// 名前だけが変わったケースも「変化なし」として扱う（比較キーに Name を含めない）。
func TestRankingWatcher_broadcast_前回と同じ_配信しない(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(rankings(100, 3), nil)
	ch, err := w.WatchUserRankings(context.Background(), watchTestLimit)
	require.NoError(t, err)
	require.Equal(t, int64(100), recvResult(t, ch).Rankings[0].Score)

	// 1回目のブロードキャストで比較対象（前回値）を確定させる。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(rankings(200, 3), nil)
	notifyOnce(t, d)
	require.Equal(t, int64(200), recvResult(t, ch).Rankings[0].Score)

	// 2回目以降は順位・スコアが同じで名前だけが違う結果。配信されない。
	renamed := rankings(200, 3)
	renamed.Rankings[0].Name = "renamed"
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(renamed, nil).Times(2)
	notifyAndFence(t, d)

	select {
	case v := <-ch:
		t.Fatalf("変化が無いのに配信された: %+v", v)
	default:
	}
}

// [§3 ケース3] 再取得の失敗でストリームを切らない。
// ZSet 揮発は全クライアントに同時に起きるため、切ると同時再接続で雪崩になる。
// 次の通知で復帰することまで確認する。
func TestRankingWatcher_broadcast_fetch失敗_ストリームを切らず次の通知で復帰する(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(rankings(100, 3), nil)
	ch, err := w.WatchUserRankings(context.Background(), watchTestLimit)
	require.NoError(t, err)
	require.Equal(t, int64(100), recvResult(t, ch).Rankings[0].Score)

	// 1件目の通知: ZSet 揮発で失敗。ストリームは切らない。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(ranking.RankingsResult{}, rankingdomain.ErrRankingUnavailable)
	notifyOnce(t, d)

	// 2件目の通知: 復帰して配信が続く。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(rankings(200, 3), nil)
	notifyOnce(t, d)

	assert.Equal(t, int64(200), recvResult(t, ch).Rankings[0].Score, "失敗の後も配信が続く")
}

// [§3 ケース4] 変化があれば配る。fetch は購読者の数によらず1回で、
// 引数の limit は購読者の最大値。各購読者へは自分の limit で切って配る。
func TestRankingWatcher_broadcast_変化あり_1回のfetchから購読者ごとのlimitで配る(t *testing.T) {
	t.Parallel()

	const (
		smallLimit = 3
		largeLimit = 10
	)

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: smallLimit}).
		Return(rankings(100, smallLimit), nil)
	small, err := w.WatchUserRankings(context.Background(), smallLimit)
	require.NoError(t, err)
	require.Len(t, recvResult(t, small).Rankings, smallLimit)

	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: largeLimit}).
		Return(rankings(100, largeLimit), nil)
	large, err := w.WatchUserRankings(context.Background(), largeLimit)
	require.NoError(t, err)
	require.Len(t, recvResult(t, large).Rankings, largeLimit)

	// 通知1件につき fetch は1回だけ。引数は購読者の最大 limit。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: largeLimit}).
		Return(rankings(300, largeLimit), nil)
	notifyOnce(t, d)

	gotSmall := recvResult(t, small)
	gotLarge := recvResult(t, large)
	assert.Len(t, gotSmall.Rankings, smallLimit, "自分の limit で切られる")
	assert.Len(t, gotLarge.Rankings, largeLimit)
	assert.Equal(t, int64(300), gotSmall.Rankings[0].Score)
	assert.Equal(t, int64(300), gotLarge.Rankings[0].Score)
}

// [§3 ケース5] 受信が追いつかない購読者には、未読の値を捨てて最新を届ける。
// 「埋まっていたら新しい方を捨てる」実装に戻すと、詰まったクライアントに
// 永久に古いランキングを見せることになる。切断もしない。
func TestRankingWatcher_broadcast_遅い購読者_最新値で上書きしdropを数える(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w, rec := d.newWatcherWithRecorder(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	// 初回スナップショットを受け取らずに放置する（＝バッファが埋まったまま）。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(rankings(100, 3), nil)
	ch, err := w.WatchUserRankings(context.Background(), watchTestLimit)
	require.NoError(t, err)

	// 通知3件ぶんの結果を先に登録しておく（3件目は fence 用。値が同じなので配信されない）。
	gomock.InOrder(
		d.uc.EXPECT().
			GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
			Return(rankings(200, 3), nil),
		d.uc.EXPECT().
			GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
			Return(rankings(300, 3), nil).Times(2),
	)
	notifyOnce(t, d)
	notifyAndFence(t, d)

	got := recvResult(t, ch)
	assert.Equal(t, int64(300), got.Rankings[0].Score, "残るのは最新値（古い方を捨てる）")
	assert.Positive(t, rec.Count("ranking watch subscriber is slow"), "drop はログに残す")

	// 切断していないので、以降も配信を受け取れる。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(rankings(400, 3), nil)
	notifyOnce(t, d)
	assert.Equal(t, int64(400), recvResult(t, ch).Rankings[0].Score)
}

// ---------------------------------------------------------------------------
// 手続きが異なるため別に切り出したもの（§3 の補足表と対応）
// ---------------------------------------------------------------------------

// 購読者の ctx をキャンセルすると、チャネルが閉じ、以降のブロードキャストの
// 対象からも外れる（登録が残っていると誰も読まないチャネルへ push し続ける）。
func TestRankingWatcher_WatchUserRankings_購読者のctxキャンセル_チャネルが閉じ配信対象から外れる(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	subCtx, cancelSub := context.WithCancel(context.Background())
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(rankings(100, 3), nil)
	ch, err := w.WatchUserRankings(subCtx, watchTestLimit)
	require.NoError(t, err)
	require.Equal(t, int64(100), recvResult(t, ch).Rankings[0].Score)

	cancelSub()
	assertClosed(t, ch)

	// 購読者が居なくなったので、以降の通知では fetch しない
	// （EXPECT していないので、呼ばれたら strict モックが落とす）。
	notifyAndFence(t, d)
}

// 複数の購読者が同時に出入りしているあいだもハブは配信を続け、
// 最終的に全チャネルが閉じる（登録解除の取りこぼし = チャネルリークの検出）。
//
// goleak は依存を増やすので使わない。代わりに「返したチャネルが必ず閉じる」ことを
// 全購読者について確認する（閉じ忘れれば requireClosed がタイムアウトする）。
func TestRankingWatcher_並行購読とキャンセル_配信を続け全チャネルが閉じる(t *testing.T) {
	t.Parallel()

	const (
		watcherCount = 8
		notifyRounds = 20
	)

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w := d.newWatcher(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	// 毎回異なるスコアを返し、必ず「変化あり」と判定させる。
	var seq atomic.Int64
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in ranking.GetRankingsInput) (ranking.RankingsResult, error) {
			return rankings(seq.Add(1), in.Limit), nil
		}).AnyTimes()

	var wg sync.WaitGroup
	for i := range watcherCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			subCtx, cancelSub := context.WithCancel(context.Background())
			// キャンセルの時期をずらす（購読直後・数件受信後・受信せず放置）。
			ch, err := w.WatchUserRankings(subCtx, i+1)
			if !assert.NoError(t, err) {
				cancelSub()
				return
			}
			for range i % 3 {
				select {
				case <-ch:
				case <-time.After(watchTimeout):
					t.Error("配信が止まった")
					cancelSub()
					assertClosed(t, ch)
					return
				}
			}
			cancelSub()
			assertClosed(t, ch)
		}()
	}

	// 購読者が出入りしているあいだ、ハブは配信を続ける。
	for range notifyRounds {
		select {
		case d.notify <- struct{}{}:
		case <-time.After(watchTimeout):
			t.Fatal("ハブが通知を受け取らなくなった")
		}
	}
	wg.Wait()
}

// 同種の失敗が連続しても、ログは間引かれて1件目だけが出る。
// ZSet 揮発は全購読者に同時に起きるため、素直に出すと同一ログが他のエラーを埋める。
func TestRankingWatcher_ログ間引き_連続する同種の失敗は1件目だけ出す(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	d := newWatchDeps(ctrl)
	d.expectSubscribe()
	w, rec := d.newWatcherWithRecorder(t)
	stop := startRun(t, w)
	defer func() { require.NoError(t, stop()) }()

	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(rankings(100, 3), nil)
	ch, err := w.WatchUserRankings(context.Background(), watchTestLimit)
	require.NoError(t, err)
	require.Equal(t, int64(100), recvResult(t, ch).Rankings[0].Score)

	// 3 回続けて失敗させる（fence のぶんを含めて 3 回 fetch される）。
	d.uc.EXPECT().
		GetUserRankings(gomock.Any(), ranking.GetRankingsInput{Limit: watchTestLimit}).
		Return(ranking.RankingsResult{}, rankingdomain.ErrRankingUnavailable).Times(3)
	notifyOnce(t, d)
	notifyAndFence(t, d)

	assert.Equal(t, 1, rec.Count("ranking watch fetch failed"),
		"同じ失敗が続いてもログは1件目だけ")
}
