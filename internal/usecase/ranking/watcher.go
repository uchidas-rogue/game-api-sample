package ranking

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
)

// watchLogSampleInterval は間引きログの間隔（件数ベース）。
// 1 件目と、その後この件数ごとにだけ出力する。
//
// 間引きが必須なのは、ここで起きる失敗（ZSet 揮発・遅い購読者）が
// 「全購読者に同時に起きる」性質を持つため。素直に出すと1件の障害が毎秒数千行の
// 同一ログになり、他のエラーを埋めてしまう。
//
// 時間ベース（直近 N 秒は出さない）にしないのは、time.Now を直接呼ばない規約
// （AGENTS.md §2）に加えて、時間依存のテストを増やさないため
// （docs/testing/ranking-watch.md §0-6）。
const watchLogSampleInterval = 100

// RankingWatcher が Watcher を満たすことをコンパイル時に検証する。
var _ Watcher = (*RankingWatcher)(nil)

// RankingWatcher はランキング更新通知のファンアウトハブ。
//
// Redis の購読は Run が **プロセスにつき1本だけ** 張り、通知1件につきランキングを
// 1回だけ読み直して全購読者へ配る。接続ごとに SUBSCRIBE を張ると
// クライアント数ぶんの Redis 接続が必要になり、接続数を有限化した設計と矛盾するため。
//
// 購読者は「バッファ1のチャネル + 最新値で上書き」で保護する。受信が追いつかない
// 購読者は切断せず、途中の値を捨てて最新だけを届ける（詳細と理由は
// docs/testing/ranking-watch.md §0-4）。
//
// Run は1プロセスに1回だけ呼ぶ。複数回の同時呼び出しは想定していない。
type RankingWatcher struct {
	uc     Usecase
	sub    RankingUpdateSubscriber
	logger *slog.Logger

	// mu は購読者の集合と last を保護する。
	mu      sync.Mutex
	stopped bool
	nextID  uint64
	subs    map[uint64]*watchSubscriber
	// last は前回配ったランキングの比較キー。差分が無ければ配信しない。
	last []rankEntryKey

	// fetchErrs / drops は間引きログの母数。ログ以外の判断には使わない。
	fetchErrs atomic.Int64
	drops     atomic.Int64
}

// rankEntryKey は「ランキングが変化したか」の比較に使うキー。
//
// Name を含めないのは、名前が MySQL 由来でランキング更新とは無関係に変わりうるため
// （改名だけで全購読者へ配ることになる）。TotalCount も同じ理由で比較しない
// （docs/testing/ranking-watch.md §0-3）。
type rankEntryKey struct {
	id    int64
	rank  int64
	score int64
}

// NewRankingWatcher は RankingWatcher を生成する。
//
// uc を受け取るのは、名前解決（MySQL）と初期化チェック（IsInitialized）を含む
// GetUserRankings をそのまま再利用するため。同一層内の依存になる。
func NewRankingWatcher(uc Usecase, sub RankingUpdateSubscriber, logger *slog.Logger) *RankingWatcher {
	return &RankingWatcher{
		uc:     uc,
		sub:    sub,
		logger: logger,
		subs:   make(map[uint64]*watchSubscriber),
	}
}

// Run は更新通知を購読し、ctx がキャンセルされるまで購読者へ配り続ける常駐ループ。
//
// 終了時（ctx キャンセル・購読断のいずれでも）は全購読者を登録解除してチャネルを閉じる。
// 閉じ忘れるとクライアント側の受信ループが永久に残るため、ここが唯一の出口になる。
func (w *RankingWatcher) Run(ctx context.Context) error {
	notifyCh, err := w.sub.Subscribe(ctx)
	if err != nil {
		return fmt.Errorf("subscribe ranking updates: %w", err)
	}
	w.logger.InfoContext(ctx, "ranking watcher started")

	for {
		select {
		case <-ctx.Done():
			w.shutdown()
			w.logger.InfoContext(ctx, "ranking watcher stopped")
			return nil
		case _, ok := <-notifyCh:
			if !ok {
				// ポーリングのフォールバックが無いため、黙って続けると
				// 「更新が永久に来ないストリーム」になる。呼び出し側に再起動を委ねる。
				w.logger.WarnContext(ctx, "ranking update subscription closed")
				w.shutdown()
				return ErrWatchSubscriptionClosed
			}
			w.broadcast(ctx)
		}
	}
}

// WatchUserRankings はユーザーランキングの購読を開始し、更新のたびに最新値が流れる
// チャネルを返す。ctx のキャンセルで登録解除され、チャネルはクローズされる。
//
// 登録を先に行い、その後で初回スナップショットを取る。逆順にすると fetch と登録の
// 隙間に来た更新を取りこぼす。初回 fetch が失敗した場合はチャネルを返さずエラーを返す
// （まだ何も見せていない段階なので、沈黙するストリームより失敗の方が観測しやすい）。
func (w *RankingWatcher) WatchUserRankings(ctx context.Context, limit int) (<-chan RankingsResult, error) {
	limit = rankingdomain.NormalizeLimit(limit)

	s, id, err := w.register(ctx, limit)
	if err != nil {
		return nil, err
	}

	res, err := w.uc.GetUserRankings(ctx, GetRankingsInput{Limit: limit})
	if err != nil {
		w.remove(id)
		return nil, fmt.Errorf("initial user rankings: %w", err)
	}
	s.pushInitial(truncateResult(res, limit))

	return s.ch, nil
}

// register は購読者を登録する。ctx キャンセル時の登録解除は context.AfterFunc に任せ、
// 購読者ごとに待機用の goroutine を常駐させない（接続数ぶんの goroutine を作らないため）。
func (w *RankingWatcher) register(ctx context.Context, limit int) (*watchSubscriber, uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.stopped {
		return nil, 0, ErrWatcherStopped
	}
	w.nextID++
	id := w.nextID
	s := &watchSubscriber{limit: limit, ch: make(chan RankingsResult, 1)}
	w.subs[id] = s
	// ctx が既に終わっている場合、AfterFunc は別 goroutine で即座に f を呼ぶ。
	// f は w.mu を取るため、ここで保持したままでもデッドロックしない。
	s.stop = context.AfterFunc(ctx, func() { w.remove(id) })
	return s, id, nil
}

// remove は購読者を登録解除し、返却済みチャネルを閉じる。多重呼び出しに耐える。
func (w *RankingWatcher) remove(id uint64) {
	w.mu.Lock()
	s, ok := w.subs[id]
	delete(w.subs, id)
	w.mu.Unlock()

	if !ok {
		return
	}
	s.stop()
	s.close()
}

// shutdown は全購読者を登録解除し、以降の購読受付を止める。
func (w *RankingWatcher) shutdown() {
	w.mu.Lock()
	w.stopped = true
	subs := make([]*watchSubscriber, 0, len(w.subs))
	for id, s := range w.subs {
		subs = append(subs, s)
		delete(w.subs, id)
	}
	w.mu.Unlock()

	for _, s := range subs {
		s.stop()
		s.close()
	}
}

// broadcast は通知1件ぶんの配信を行う。
//
// fetch は購読者の数によらず1回だけ（購読者ごとに読むと Redis/MySQL の読み取りが
// クライアント数に比例する）。取得は購読者の最大 limit で行い、配るときに
// 各購読者の limit へ切り詰める。
func (w *RankingWatcher) broadcast(ctx context.Context) {
	subs, maxLimit := w.snapshot()
	if len(subs) == 0 {
		// 誰も見ていないランキングは読まない。
		return
	}

	res, err := w.uc.GetUserRankings(ctx, GetRankingsInput{Limit: maxLimit})
	if err != nil {
		// ZSet 揮発（ErrRankingUnavailable）等でストリームを切らない。全クライアントが
		// 同時に切断・再接続すると雪崩になるため（docs/testing/ranking-watch.md §0-5）。
		w.logFetchError(ctx, err)
		return
	}

	if !w.commitIfChanged(res.Rankings) {
		return
	}

	for _, s := range subs {
		if s.push(truncateResult(res, s.limit)) {
			w.logDrop(ctx, s.limit)
		}
	}
}

// snapshot は現在の購読者と、その中の最大 limit を返す。
func (w *RankingWatcher) snapshot() ([]*watchSubscriber, int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	subs := make([]*watchSubscriber, 0, len(w.subs))
	maxLimit := 0
	for _, s := range w.subs {
		subs = append(subs, s)
		if s.limit > maxLimit {
			maxLimit = s.limit
		}
	}
	return subs, maxLimit
}

// commitIfChanged は前回配った内容と比較し、変化があれば記録して true を返す。
func (w *RankingWatcher) commitIfChanged(entries []rankingdomain.RankEntry) bool {
	keys := make([]rankEntryKey, len(entries))
	for i, e := range entries {
		keys[i] = rankEntryKey{id: e.ID, rank: e.Rank, score: e.Score}
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if sameRanking(w.last, keys) {
		return false
	}
	w.last = keys
	return true
}

// sameRanking は2つの比較キー列が等しいかを判定する。
func sameRanking(a, b []rankEntryKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// truncateResult は取得結果を購読者の limit で切り詰める。
// 容量も切り落として、購読者側の append が他の購読者と同じ配列を書き換えないようにする。
func truncateResult(res RankingsResult, limit int) RankingsResult {
	if len(res.Rankings) <= limit {
		return res
	}
	res.Rankings = res.Rankings[:limit:limit]
	return res
}

// logFetchError は再取得の失敗を間引いてログに出す。
func (w *RankingWatcher) logFetchError(ctx context.Context, err error) {
	n := w.fetchErrs.Add(1)
	if !sampledLog(n) {
		return
	}
	w.logger.WarnContext(ctx, "ranking watch fetch failed (stream kept open)",
		slog.Int64("occurrences", n),
		slog.Any("error", err),
	)
}

// logDrop は遅い購読者への配信取りこぼしを間引いてログに出す。
func (w *RankingWatcher) logDrop(ctx context.Context, limit int) {
	n := w.drops.Add(1)
	if !sampledLog(n) {
		return
	}
	w.logger.WarnContext(ctx, "ranking watch subscriber is slow (dropped stale update)",
		slog.Int64("occurrences", n),
		slog.Int("limit", limit),
	)
}

// sampledLog は n 件目を出力対象とするかを判定する（1件目と、その後 interval 件ごと）。
func sampledLog(n int64) bool {
	return n == 1 || n%watchLogSampleInterval == 0
}

// watchSubscriber は購読者1人ぶんの配信口。
//
// ch はバッファ1で、未読の値が残っているときは **捨てて最新で置き換える**。
// 「埋まっていたら新しい方を捨てる」にすると古い値が残り、詰まったクライアントに
// 永久に古いランキングを見せることになる（docs/testing/ranking-watch.md §0-4）。
type watchSubscriber struct {
	limit int
	ch    chan RankingsResult
	// stop は context.AfterFunc の登録解除。ctx への参照を落とすために必ず呼ぶ。
	stop func() bool

	// mu は ch への送信・クローズを直列化する。送信側が複数
	// （ブロードキャストと初回スナップショット）あるため、順序の逆転と
	// クローズ済みチャネルへの送信を防ぐ。
	mu     sync.Mutex
	closed bool
	// delivered は1件でも配信したかを示す。初回スナップショットが
	// 後から来たブロードキャストを上書きしないための判定に使う。
	delivered bool
}

// push は最新値を送る。未読の値を捨てて置き換えた場合に true を返す。
func (s *watchSubscriber) push(v RankingsResult) (dropped bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}
	select {
	case <-s.ch:
		dropped = true
	default:
	}
	// 送信側は mu で直列化されており、直前に空けたバッファを他の送信者が
	// 埋めることはないため、この送信はブロックしない。
	s.ch <- v
	s.delivered = true
	return dropped
}

// pushInitial は初回スナップショットを送る。
// 登録直後にブロードキャストが先着していた場合は何もしない（そちらの方が新しい）。
func (s *watchSubscriber) pushInitial(v RankingsResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.delivered {
		return
	}
	s.ch <- v
	s.delivered = true
}

// close は配信チャネルを閉じる。多重呼び出しに耐える。
func (s *watchSubscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}
