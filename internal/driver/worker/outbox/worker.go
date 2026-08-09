// Package outbox は Outbox を購読して外部副作用を実行する worker を提供する。
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	outboxusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// Worker は outbox_events をポーリングし、event_type に応じて副作用を実行する。
//
// 処理経路は2つある。通常は「バッチ経路」で処理し、失敗時のみ「イベント単位経路」へ退避する。
//
// # バッチ経路（applyBatch・通常時）
//
// 1ティックで batchSize 件ずつ取得し、候補が尽きるまで繰り返す（ドレイン）。
// MySQL 側を単一トランザクションで適用し、COMMIT が確定してから Redis を反映する:
//  1. ListPending が FOR UPDATE SKIP LOCKED で候補を取得（そのまま claim として働く）
//  2. ギルド単位にスコアを集約し、bulk upsert で一括加算（履歴はイベント単位のまま bulk INSERT）
//  3. MarkProcessedByIDs で一括マーク → COMMIT
//  4. COMMIT 後に Redis へパイプラインで一括反映（1往復）
//
// バッチ単位 tx にする理由: イベント単位 tx では1件ごとに COMMIT（fsync）が発生し、
// これがスループットの上限を決めていた（実測 約27 events/sec、並列化しても約200 events/sec に対し
// 生産 約407/sec）。バッチ化で COMMIT 回数が 1/batchSize になる。
//
// ドレインする理由: 1ティック batchSize 件で打ち切ると、通知が途切れた時点で残りが
// pollInterval（既定10分）待ちになり、バックログの消化が事実上停止する。
//
// # イベント単位経路（applyPerEvent・バッチ失敗時のフォールバック）
//
// バッチは1件でも失敗するイベントがあると全体がロールバックされるため、恒久失敗する
// イベントが混ざるとそのバッチが永久に前進しない（head-of-line blocking）。
// バッチ適用が失敗したときは1件ずつ独立した tx で処理し、失敗イベントを隔離して残りを前進させる:
//  1. ClaimByID で id 指定に FOR UPDATE SKIP LOCKED 確保（並行処理・複数 worker 安全）
//  2. applyEventInTx で MySQL 副作用を実行
//  3. 成功なら同一 tx で MarkProcessed → COMMIT → COMMIT 後に Redis 反映
//  4. 失敗なら tx を ROLLBACK し、別 tx で IncrementRetry + last_error 記録
//
// この経路は concurrency 本の goroutine で並列に処理する（バッチ経路は単一 tx のため
// concurrency の影響を受けない）。
//
// # 共通の前提
//
// 本 worker のトランザクションはすべて READ COMMITTED で開始する。
// MySQL 既定の REPEATABLE READ では ListPending の SELECT ... FOR UPDATE が
// idx_outbox_events_pending 上でギャップロックを取得し、走査が未処理範囲の末尾に届いたときに
// 新規 INSERT が入る隙間までロックしてしまう。その結果 API 側の InsertOutboxEvent が
// INSERT_INTENTION 待ちでブロックされる（SKIP LOCKED はレコードロックを飛ばすだけで
// ギャップロックは回避しない）。実測では API の p95 が 108ms → 4.6s へ悪化していた。
// worker は同一トランザクション内での読み取り一貫性に依存しないため RC で問題ない。
// 一方 RankingSyncer はスナップショット一貫性が必要なため、意図的に既定（REPEATABLE READ）のまま。
//
// Redis 反映は必ず COMMIT の後に行う（理由は applyRedisAfterCommit のコメント）。
//
// payload のデコード失敗・未知の event_type はバッチ適用の前に検出して対象から除外し、
// 個別に retry 記録する。決定的な失敗でバッチ全体を巻き添えにしないため。
//
// 処理順序は保証しない。扱う操作がスコア加算（可換）のみのため問題ないが、
// 順序に依存する event_type を追加する場合はこの前提を見直すこと。
//
// max retry 上限・DLQ・処理済みレコードの GC は本実装では未対応（後続課題）。
// そのため恒久失敗イベント（poison）は ListPending に何度でも現れる。ドレインと通知駆動が
// これを読み直し続けてビジーループにならないよう、前進件数による打ち切り（runOnce）と
// 滞留中の通知抑止（Run）を入れてある。詳細は docs/testing/outbox-worker.md §0-2・§0-3。
type Worker struct {
	repo         outboxusecase.Repository
	rankingRepo  rankingusecase.Repository
	rankingStore rankingusecase.RankingStore
	tx           shared.Transactor
	subscriber   outboxusecase.Subscriber
	logger       *slog.Logger
	pollInterval time.Duration
	batchSize    int
	concurrency  int
	tickTimeout  time.Duration
}

// Config は Worker のコンストラクタ引数。
type Config struct {
	Repo         outboxusecase.Repository
	RankingRepo  rankingusecase.Repository
	RankingStore rankingusecase.RankingStore
	Tx           shared.Transactor
	Subscriber   outboxusecase.Subscriber
	Logger       *slog.Logger
	PollInterval time.Duration
	BatchSize    int
	// Concurrency は1ティック内でイベントを並列処理する goroutine 数。
	// 同時に張るトランザクション数と等しいため、DB 接続プールの上限以下にすること。
	// 0 以下の場合は 1（逐次処理）に丸める。
	Concurrency int
	// TickTimeout は1ティック（runOnce）の処理時間上限。
	// DB/Redis のブロッキングでループがハングするのを防ぐ。0 の場合は無制限。
	TickTimeout time.Duration
}

// New は Worker を生成する。Config.Logger は呼び出し側で必ず初期化済みのものを渡す。
func New(cfg Config) *Worker {
	concurrency := cfg.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	return &Worker{
		repo:         cfg.Repo,
		rankingRepo:  cfg.RankingRepo,
		rankingStore: cfg.RankingStore,
		tx:           cfg.Tx,
		subscriber:   cfg.Subscriber,
		logger:       cfg.Logger,
		pollInterval: cfg.PollInterval,
		batchSize:    cfg.BatchSize,
		concurrency:  concurrency,
		tickTimeout:  cfg.TickTimeout,
	}
}

// Run は ctx がキャンセルされるまでポーリングと通知購読を並行する。
// 1ティック/通知の処理が失敗してもループは継続し、次のトリガでリトライする。
// 通常時は通知（Subscriber）駆動で処理し、ポーリングは取りこぼし時のフォールバック。
//
// 直近のドレインが「滞留」（候補は batchSize 件あるのに1件も前進しない）で終わった場合、
// 次の ticker まで通知駆動のドレインを抑止する。Subscriber はバッファ1のチャネルへ
// ノンブロッキング送信するため、書き込みが続く限り select にはほぼ常に通知が入っている。
// 滞留状態で通知に反応し続けると、スリープを挟まず同じ窓を読み直すビジーループになる。
// 滞留中は新規イベントも id 順で窓の外にあって処理できないため、通知を捨てても失うものはない。
func (w *Worker) Run(ctx context.Context) error {
	w.logger.InfoContext(ctx, "outbox worker started",
		slog.Duration("poll_interval", w.pollInterval),
		slog.Int("batch_size", int(w.batchSize)),
	)
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	// 通知チャネル購読開始。失敗時はポーリングのみで継続。
	var notifyCh <-chan struct{}
	if w.subscriber != nil {
		ch, err := w.subscriber.Subscribe(ctx)
		if err != nil {
			w.logger.WarnContext(ctx, "outbox subscribe failed (poll only)",
				slog.Any("error", err))
		} else {
			notifyCh = ch
		}
	}

	// 初回はティックを待たずに即実行
	stalled := w.drainAndLog(ctx, "outbox tick failed")

	for {
		select {
		case <-ctx.Done():
			w.logger.InfoContext(ctx, "outbox worker stopped")
			return nil
		case <-ticker.C:
			// ticker では滞留していても必ず再試行する（運用側で poison を除去したら復帰する）。
			stalled = w.drainAndLog(ctx, "outbox tick failed")
		case _, ok := <-notifyCh:
			if !ok {
				// 購読チャネルがクローズされた場合は以降ポーリングのみで継続。
				notifyCh = nil
				continue
			}
			if stalled {
				// 読み直しても同じ結果になるため通知を捨てる。解除は ticker が行う。
				continue
			}
			stalled = w.drainAndLog(ctx, "outbox notify-triggered run failed")
		}
	}
}

// drainAndLog は drainNow を実行し、失敗を errMsg でログに落として滞留状態を返す。
// 一過性の DB/Redis 障害で worker プロセスが落ちてはならないため、エラーは伝播させない。
// 失敗で終わった場合は滞留とみなさない（次の通知で普通に再試行してよい）。
func (w *Worker) drainAndLog(ctx context.Context, errMsg string) bool {
	stalled, err := w.drainNow(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		w.logger.ErrorContext(ctx, errMsg, slog.Any("error", err))
	}
	return stalled
}

// drainNow は未処理イベントが枯れる（もしくは前進しなくなる）まで runOnce を繰り返し、
// 最後のティックが滞留で終わったかどうかを返す。
//
// runOnce は tickTimeout で打ち切られるため1回で枯れるとは限らない。打ち切られた時点で
// 次のトリガ（通知 or ポーリング）を待ってしまうと、通知が途切れたタイミングでバックログの
// 消化が pollInterval（既定10分）のあいだ完全に停止する。実測ではこれにより
// 8,959 件が 440 秒間まったく処理されない状態が発生したため、枯れるまで自走させる。
func (w *Worker) drainNow(ctx context.Context) (stalled bool, err error) {
	for {
		res, err := w.runOnce(ctx)
		if err != nil {
			return false, err
		}
		if res.drained {
			return res.stalled, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
	}
}

// tickResult は1ティック（runOnce）の結果。
type tickResult struct {
	// drained はドレインを終えてよいことを表す（候補が枯れた、または前進しないので打ち切った）。
	drained bool
	// stalled は「候補が batchSize 件あるのに1件も前進しなかった」ことを表す。
	// この状態では読み直しても必ず同じ結果になるため、Run が通知駆動の再入を抑止する。
	stalled bool
}

// runOnce は1ティックぶんの処理を実行し、ドレインを続けるべきかを返す。
// ListPending で候補を batchSize 件ずつ取得して適用することを繰り返す。
// 各バッチは単一 tx（フォールバック時は concurrency 本の goroutine で並列）で処理する。
//
// 枯渇判定には「取得件数（listed）」ではなく「前進件数（applied）」を使う。
// ListPendingOutboxEvents は retry_count を条件に含めないため、デコード不能な恒久失敗
// イベントは何度でも取得される。取得件数だけで判定すると、それが batchSize 件以上
// 滞留したときに毎回ちょうど batchSize 件が返り続けてドレインが終わらない。
//
//	listed == 0 / listed < batchSize      → 枯渇
//	listed == batchSize && applied == 0   → 滞留。打ち切って Run に通知抑止させる
//	それ以外                               → ドレイン継続
//
// 滞留判定を listed == batchSize に限定するのが要点。listed < batchSize（恒久失敗が数件だけ）で
// 滞留扱いすると、窓に入るはずの新規イベントまで pollInterval 待ちにしてしまう。
//
// tickTimeout > 0 のとき、ティック全体に deadline 付き context を被せる。これは DB/Redis の
// ブロッキングでループがハングするのを防ぐための安全弁。deadline に達した場合は
// drained=false, err=nil を返し、呼び出し元（drainNow）が新しいティックで処理を継続する。
// バッチ単位 tx のため打ち切りによる取りこぼしは生じない。
func (w *Worker) runOnce(ctx context.Context) (tickResult, error) {
	tickCtx := ctx
	if w.tickTimeout > 0 {
		var cancel context.CancelFunc
		tickCtx, cancel = context.WithTimeout(ctx, w.tickTimeout)
		defer cancel()
	}

	for {
		listed, applied, err := w.applyBatch(tickCtx)
		if err != nil {
			switch {
			case ctx.Err() != nil:
				// 親のキャンセル / タイムアウト。worker 自体を終える。
				return tickResult{}, ctx.Err()
			case tickCtx.Err() != nil:
				// ティック期限切れ。未枯渇のまま復帰し、drainNow が新しいティックで継続する。
				w.logger.DebugContext(ctx, "outbox tick deadline reached, continuing in next tick",
					slog.Duration("tick_timeout", w.tickTimeout))
				return tickResult{}, nil
			}
			// 実エラー。バッチ内に1件でも失敗するイベントがあると全体がロールバックされ、
			// 再試行しても同じ場所で止まり続ける（head-of-line blocking）。
			// イベント単位経路へフォールバックして失敗イベントを隔離し、残りを前進させる。
			w.logger.WarnContext(ctx, "outbox batch apply failed, falling back to per-event processing",
				slog.Any("error", err))
			listed, applied, err = w.applyPerEvent(tickCtx)
			if err != nil {
				return tickResult{}, w.absorbTickDeadline(ctx, tickCtx, err)
			}
		}
		// batchSize に満たない = 未処理イベントが枯れた。
		if listed == 0 || listed < w.batchSize {
			return tickResult{drained: true}, nil
		}
		if applied == 0 {
			// 窓が恒久失敗イベントで埋まっている。読み直しても同じ結果なのでドレインを打ち切る。
			w.logger.WarnContext(ctx, "outbox drain stalled: no event made progress",
				slog.Int("listed", listed),
				slog.Int("batch_size", w.batchSize))
			return tickResult{drained: true, stalled: true}, nil
		}
		if tickCtx.Err() != nil {
			return tickResult{}, w.absorbTickDeadline(ctx, tickCtx, tickCtx.Err())
		}
	}
}

// redisWork は COMMIT 後に Redis へ反映する加算差分。
type redisWork struct {
	users  []rankingdomain.UserPointDelta
	guilds []rankingdomain.GuildScoreDelta
}

// batchWork は1バッチぶんの適用内容。イベント群をギルド単位に集約した結果を保持する。
type batchWork struct {
	// ids は処理済みにマークする対象。
	ids []uint64
	// guildDeltas はギルド単位に合算したスコア加算。件数はギルド数まで縮む。
	guildDeltas []rankingdomain.GuildScoreDelta
	// userDeltas はユーザー単位に合算したポイント加算（Redis 反映用）。
	userDeltas []rankingdomain.UserPointDelta
	// histories はイベント単位で残す必要がある履歴行。集約しない。
	histories []rankingdomain.GuildScoreHistoryEntry
}

// applyRedisAfterCommit は COMMIT 確定後に Redis へ加算を反映する。
//
// Redis は MySQL のトランザクションの外側にある。tx の中で加算すると
// 「Redis 加算済み → MarkProcessed(ByIDs) か COMMIT が失敗 → MySQL だけ巻き戻る」が起こり、
// イベントが未マークのまま再取得されて Redis だけ二重に加算される。しかも再試行のたびに
// 積み増されるため、ずれは1回ぶんに留まらず累積する。
// COMMIT を確定させてから反映することで、誤差を「欠落・高々1バッチぶん・累積しない」側へ倒す。
//
// 失敗しても再試行させずログのみに留める。MySQL は確定済みでロールバックできず、
// 再適用すると今度こそ二重加算になるため。欠落の復旧は RankingSyncer の焼き直しに委ねる。
func (w *Worker) applyRedisAfterCommit(ctx context.Context, work redisWork, eventCount int) {
	if len(work.users) == 0 && len(work.guilds) == 0 {
		return
	}
	if err := w.rankingStore.ApplyScoreDeltas(ctx, work.users, work.guilds); err != nil {
		w.logger.ErrorContext(ctx, "outbox redis apply failed after commit (ranking cache lags until resync)",
			slog.Int("event_count", eventCount),
			slog.Int("user_delta_count", len(work.users)),
			slog.Int("guild_delta_count", len(work.guilds)),
			slog.Any("error", err),
		)
	}
}

// applyBatch は候補を1つのトランザクションでまとめて適用し、
// 取得件数（listed）と処理済みマークまで到達した件数（applied）を返す。
//
// イベント単位トランザクションでは1イベントごとに COMMIT（fsync）が発生し、これが
// スループットの上限を決めていた。バッチ単位にすることで COMMIT 回数が 1/batchSize になる。
// ListPending の FOR UPDATE SKIP LOCKED がそのまま claim として働くため、
// 別途 ClaimByID を発行する必要はない（tx を開いたままロックを保持する）。
//
// payload のデコードはトランザクションの外側の判定で先に済ませ、壊れたイベントは
// バッチから除外して個別に retry 記録する。決定的な失敗でバッチ全体を巻き添えにしないため。
func (w *Worker) applyBatch(ctx context.Context) (listed, applied int, err error) {
	var (
		work       batchWork
		undecoded  []outboxdomain.Event
		decodeErrs []error
	)

	if err = w.tx.DoInTx(ctx, func(tx shared.Tx) error {
		events, lerr := w.repo.ListPending(ctx, tx, int32(w.batchSize))
		if lerr != nil {
			return fmt.Errorf("list pending: %w", lerr)
		}
		listed = len(events)
		if listed == 0 {
			return nil
		}

		work, undecoded, decodeErrs = buildBatchWork(events)
		if len(work.ids) == 0 {
			return nil
		}

		// MySQL（source of truth）を tx 内で適用しきる。Redis は COMMIT 後に回す。
		if err := w.rankingRepo.BulkIncrementGuildScores(ctx, tx, work.guildDeltas); err != nil {
			return fmt.Errorf("mysql bulk increment guild scores: %w", err)
		}
		if err := w.rankingRepo.BulkInsertGuildScoreHistories(ctx, tx, work.histories); err != nil {
			return fmt.Errorf("mysql bulk insert guild score histories: %w", err)
		}
		if err := w.repo.MarkProcessedByIDs(ctx, tx, work.ids); err != nil {
			return fmt.Errorf("mark processed (count=%d): %w", len(work.ids), err)
		}
		return nil
	}, shared.WithIsolation(shared.IsolationReadCommitted)); err != nil {
		return 0, 0, err
	}

	w.applyRedisAfterCommit(ctx,
		redisWork{users: work.userDeltas, guilds: work.guildDeltas}, len(work.ids))

	// デコード不能なイベントはバッチのコミット後に個別で retry 記録する
	// （バッチ tx に混ぜると巻き添えでロールバックされるため）。
	for i, ev := range undecoded {
		w.recordRetry(ctx, ev.ID, ev.Type, ev.RetryCount, decodeErrs[i])
	}
	return listed, len(work.ids), nil
}

// buildBatchWork はイベント群を集約し、バッチ適用用のデータを組み立てる。
// デコードできない / 未知の event_type のイベントは適用対象から外し、
// エラーとあわせて返す（呼び出し元が個別に retry 記録する）。
func buildBatchWork(events []outboxdomain.Event) (batchWork, []outboxdomain.Event, []error) {
	var (
		work      batchWork
		undecoded []outboxdomain.Event
		errs      []error
	)
	guildSums := make(map[int64]int64)
	userSums := make(map[int64]int64)

	for _, ev := range events {
		if ev.Type != outboxdomain.EventTypeRankingScoreAdded {
			undecoded = append(undecoded, ev)
			errs = append(errs, fmt.Errorf("%w: %s", outboxdomain.ErrUnknownEventType, ev.Type))
			continue
		}
		p, err := outboxdomain.UnmarshalRankingScoreAddedPayload(ev.Payload)
		if err != nil {
			undecoded = append(undecoded, ev)
			errs = append(errs, err)
			continue
		}
		work.ids = append(work.ids, ev.ID)
		// 履歴は「誰がいつ何点入れたか」を残す必要があるためイベント単位で保持する。
		work.histories = append(work.histories, rankingdomain.GuildScoreHistoryEntry{
			GuildID: p.GuildID, UserID: p.UserID, Points: p.Points,
		})
		guildSums[p.GuildID] += p.Points
		userSums[p.UserID] += p.Points
	}

	for guildID, points := range guildSums {
		work.guildDeltas = append(work.guildDeltas, rankingdomain.GuildScoreDelta{
			GuildID: guildID, Points: points,
		})
	}
	for userID, points := range userSums {
		work.userDeltas = append(work.userDeltas, rankingdomain.UserPointDelta{
			UserID: userID, Points: points,
		})
	}
	return work, undecoded, errs
}

// applyPerEvent はイベント単位トランザクションで候補を処理し、
// 取得件数（listed）と前進した件数（applied）を返す。
// バッチ適用が失敗したときのフォールバック経路。1件ずつ独立して commit/rollback するため、
// 恒久的に失敗するイベントがあっても他のイベントは前進する。
func (w *Worker) applyPerEvent(ctx context.Context) (listed, applied int, err error) {
	candidates, err := w.listCandidates(ctx)
	if err != nil {
		return 0, 0, err
	}
	if len(candidates) == 0 {
		return 0, 0, nil
	}
	applied, err = w.processBatch(ctx, candidates)
	if err != nil {
		return 0, 0, err
	}
	return len(candidates), applied, nil
}

// absorbTickDeadline はティック期限切れ由来のエラーを nil に吸収する。
// 親 ctx が生きている限り、期限切れは「処理継続すべき正常な打ち切り」であり
// エラーログに出すべき異常ではない。親がキャンセル済みならそのまま伝播させる。
func (w *Worker) absorbTickDeadline(ctx, tickCtx context.Context, err error) error {
	if ctx.Err() == nil && tickCtx.Err() != nil {
		w.logger.DebugContext(ctx, "outbox tick deadline reached, continuing in next tick",
			slog.Duration("tick_timeout", w.tickTimeout))
		return nil
	}
	return err
}

// listCandidates は未処理イベントの候補を batchSize 件まで取得する。
func (w *Worker) listCandidates(ctx context.Context) ([]outboxdomain.Event, error) {
	var candidates []outboxdomain.Event
	if err := w.tx.DoInTx(ctx, func(tx shared.Tx) error {
		events, err := w.repo.ListPending(ctx, tx, int32(w.batchSize))
		if err != nil {
			return fmt.Errorf("list pending: %w", err)
		}
		candidates = events
		return nil
	}, shared.WithIsolation(shared.IsolationReadCommitted)); err != nil {
		return nil, err
	}
	return candidates, nil
}

// processBatch は候補を concurrency 本の goroutine で並列に処理し、前進した件数を返す。
// processOne が返すのは claim/mark 自体の失敗（インフラ起因）のみで、applyEventInTx の失敗は
// processOne 内で retry 記録に変換される。したがってここでエラーを受けたらティックを打ち切る。
// 最初のエラーで派生 context を cancel し、残りの goroutine と投入ループを速やかに終わらせる。
func (w *Worker) processBatch(ctx context.Context, candidates []outboxdomain.Event) (int, error) {
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ids := make(chan uint64)
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		firstErr   error
		progressed int
	)

	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				ok, err := w.processOne(batchCtx, id)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					// 投入ループと他の goroutine を止める。ids は投入側が必ず close するため
					// ここで return せずに読み切っても良いが、無駄な処理を避けるため打ち切る。
					cancel()
					return
				}
				if ok {
					mu.Lock()
					progressed++
					mu.Unlock()
				}
			}
		}()
	}

	for _, ev := range candidates {
		select {
		case ids <- ev.ID:
		case <-batchCtx.Done():
			// 全 goroutine が return 済み / cancel 済み。投入を打ち切る。
			close(ids)
			wg.Wait()
			mu.Lock()
			defer mu.Unlock()
			if firstErr != nil {
				return 0, firstErr
			}
			return 0, batchCtx.Err()
		}
	}
	close(ids)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if firstErr != nil {
		return 0, firstErr
	}
	return progressed, nil
}

// processOne は指定 ID のイベントを1件、個別トランザクションで claim して処理し、
// 前進したか（処理済みにできたか）を返す。
// 既に処理済み / 他 worker がロック中で claim できない場合は何もしないが、その候補は
// もう自分の担当ではないため前進として数える。
// applyEventInTx が失敗した場合は claim tx をロールバックし（MySQL 副作用も巻き戻る）、
// 別 tx で IncrementRetry を記録する。retry 記録自体の失敗はログのみ（次ティックで再処理）。
func (w *Worker) processOne(ctx context.Context, id uint64) (progressed bool, err error) {
	var (
		handleErr  error
		failedType outboxdomain.EventType
		retryCount uint32
		redis      redisWork
	)

	if err = w.tx.DoInTx(ctx, func(tx shared.Tx) error {
		ev, found, cerr := w.repo.ClaimByID(ctx, tx, id)
		if cerr != nil {
			return fmt.Errorf("claim id=%d: %w", id, cerr)
		}
		if !found {
			return nil
		}

		work, herr := w.applyEventInTx(ctx, tx, ev)
		if herr != nil {
			// エラーを返して tx をロールバックさせ、MySQL 副作用を巻き戻す。
			// retry 記録は別 tx で行うため、ここでは情報だけ退避する。
			handleErr = herr
			failedType = ev.Type
			retryCount = ev.RetryCount
			return herr
		}
		if err := w.repo.MarkProcessed(ctx, tx, ev.ID); err != nil {
			return fmt.Errorf("mark processed id=%d: %w", ev.ID, err)
		}
		redis = work
		return nil
	}, shared.WithIsolation(shared.IsolationReadCommitted)); err != nil {
		if handleErr == nil {
			// claim / mark 自体の失敗（applyEventInTx 以外）。ティックを止めて次回リトライ。
			return false, err
		}
		// applyEventInTx 失敗: 別 tx で retry を記録する（ベストエフォート）。
		w.recordRetry(ctx, id, failedType, retryCount, handleErr)
		return false, nil
	}

	// claim できなかった場合 redis は空なので、この呼び出しは何もしない。
	w.applyRedisAfterCommit(ctx, redis, 1)
	return true, nil
}

// recordRetry は失敗イベントの retry_count と last_error を独立したトランザクションで記録する。
// 業務側の tx とは分離する（業務 tx をロールバックさせつつ retry だけ残すため）。
// 記録自体の失敗はログのみに留める。イベントは pending のままなので次ティックで再処理される。
func (w *Worker) recordRetry(
	ctx context.Context,
	id uint64,
	eventType outboxdomain.EventType,
	retryCount uint32,
	cause error,
) {
	w.logger.WarnContext(ctx, "outbox event handling failed",
		slog.Uint64("event_id", id),
		slog.String("event_type", string(eventType)),
		slog.Uint64("retry_count", uint64(retryCount)),
		slog.Any("error", cause),
	)
	if rerr := w.tx.DoInTx(ctx, func(tx shared.Tx) error {
		return w.repo.IncrementRetry(ctx, tx, id, cause.Error())
	}, shared.WithIsolation(shared.IsolationReadCommitted)); rerr != nil {
		w.logger.ErrorContext(ctx, "outbox increment retry failed",
			slog.Uint64("event_id", id),
			slog.Any("error", rerr),
		)
	}
}

// applyEventInTx は event_type に応じて MySQL 副作用（source of truth）を実行し、
// COMMIT 後に Redis へ反映すべき差分を返す。tx は claim と同一トランザクション。
// Redis をここで触らない理由は applyRedisAfterCommit のコメントを参照。
// 新しい event_type を増やす際はここに case を追加する。
func (w *Worker) applyEventInTx(ctx context.Context, tx shared.Tx, ev outboxdomain.Event) (redisWork, error) {
	switch ev.Type {
	case outboxdomain.EventTypeRankingScoreAdded:
		p, err := outboxdomain.UnmarshalRankingScoreAddedPayload(ev.Payload)
		if err != nil {
			return redisWork{}, err
		}
		// MySQL: ギルドスコア累計加算と履歴挿入（同期リクエストから移設した集計処理）。
		if err := w.rankingRepo.IncrementGuildScore(ctx, tx, p.GuildID, p.Points); err != nil {
			return redisWork{}, fmt.Errorf("mysql increment guild score: %w", err)
		}
		if err := w.rankingRepo.InsertGuildScoreHistory(ctx, tx, p.GuildID, p.UserID, p.Points); err != nil {
			return redisWork{}, fmt.Errorf("mysql insert guild score history: %w", err)
		}
		return redisWork{
			users:  []rankingdomain.UserPointDelta{{UserID: p.UserID, Points: p.Points}},
			guilds: []rankingdomain.GuildScoreDelta{{GuildID: p.GuildID, Points: p.Points}},
		}, nil
	default:
		return redisWork{}, fmt.Errorf("%w: %s", outboxdomain.ErrUnknownEventType, ev.Type)
	}
}
