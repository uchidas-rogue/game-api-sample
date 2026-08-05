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
// 1バッチを単一トランザクションで適用する:
//  1. ListPending が FOR UPDATE SKIP LOCKED で候補を取得（そのまま claim として働く）
//  2. ギルド単位にスコアを集約し、bulk upsert で一括加算（履歴はイベント単位のまま bulk INSERT）
//  3. Redis へパイプラインで一括反映（1往復）
//  4. MarkProcessedByIDs で一括マーク → COMMIT
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
//  2. handleEvent で MySQL 副作用と Redis 反映を実行
//  3. 成功なら同一 tx で MarkProcessed → COMMIT
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
// payload のデコード失敗・未知の event_type はバッチ適用の前に検出して対象から除外し、
// 個別に retry 記録する。決定的な失敗でバッチ全体を巻き添えにしないため。
//
// 処理順序は保証しない。扱う操作がスコア加算（可換）のみのため問題ないが、
// 順序に依存する event_type を追加する場合はこの前提を見直すこと。
//
// max retry 上限・DLQ・処理済みレコードの GC は本実装では未対応（後続課題）。
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
	if err := w.drainNow(ctx); err != nil && !errors.Is(err, context.Canceled) {
		w.logger.ErrorContext(ctx, "outbox tick failed", slog.Any("error", err))
	}

	for {
		select {
		case <-ctx.Done():
			w.logger.InfoContext(ctx, "outbox worker stopped")
			return nil
		case <-ticker.C:
			if err := w.drainNow(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.ErrorContext(ctx, "outbox tick failed", slog.Any("error", err))
			}
		case _, ok := <-notifyCh:
			if !ok {
				// 購読チャネルがクローズされた場合は以降ポーリングのみで継続。
				notifyCh = nil
				continue
			}
			if err := w.drainNow(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.ErrorContext(ctx, "outbox notify-triggered run failed", slog.Any("error", err))
			}
		}
	}
}

// drainNow は未処理イベントが枯れるまで runOnce を繰り返す。
//
// runOnce は tickTimeout で打ち切られるため1回で枯れるとは限らない。打ち切られた時点で
// 次のトリガ（通知 or ポーリング）を待ってしまうと、通知が途切れたタイミングでバックログの
// 消化が pollInterval（既定10分）のあいだ完全に停止する。実測ではこれにより
// 8,959 件が 440 秒間まったく処理されない状態が発生したため、枯れるまで自走させる。
func (w *Worker) drainNow(ctx context.Context) error {
	for {
		drained, err := w.runOnce(ctx)
		if err != nil {
			return err
		}
		if drained {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// runOnce は1ティックぶんの処理を実行し、未処理イベントが枯れたかどうかを返す。
// ListPending で候補を batchSize 件ずつ取得し、取得件数が batchSize 未満になるまで繰り返す。
// 各バッチは concurrency 本の goroutine で並列処理する。
// 候補を id 指定で claim することで、先頭イベントが恒久失敗しても後続を処理でき
// （head-of-line blocking 回避）、per-event tx の原子性と並行安全性を両立する。
//
// tickTimeout > 0 のとき、ティック全体に deadline 付き context を被せる。これは DB/Redis の
// ブロッキングでループがハングするのを防ぐための安全弁。deadline に達した場合は
// drained=false, err=nil を返し、呼び出し元（drainNow）が新しいティックで処理を継続する。
// per-event tx のため打ち切りによる取りこぼしは生じない。
func (w *Worker) runOnce(ctx context.Context) (drained bool, err error) {
	tickCtx := ctx
	if w.tickTimeout > 0 {
		var cancel context.CancelFunc
		tickCtx, cancel = context.WithTimeout(ctx, w.tickTimeout)
		defer cancel()
	}

	for {
		n, err := w.applyBatch(tickCtx)
		if err != nil {
			switch {
			case ctx.Err() != nil:
				// 親のキャンセル / タイムアウト。worker 自体を終える。
				return false, ctx.Err()
			case tickCtx.Err() != nil:
				// ティック期限切れ。未枯渇のまま復帰し、drainNow が新しいティックで継続する。
				w.logger.DebugContext(ctx, "outbox tick deadline reached, continuing in next tick",
					slog.Duration("tick_timeout", w.tickTimeout))
				return false, nil
			}
			// 実エラー。バッチ内に1件でも失敗するイベントがあると全体がロールバックされ、
			// 再試行しても同じ場所で止まり続ける（head-of-line blocking）。
			// イベント単位経路へフォールバックして失敗イベントを隔離し、残りを前進させる。
			w.logger.WarnContext(ctx, "outbox batch apply failed, falling back to per-event processing",
				slog.Any("error", err))
			n, err = w.applyPerEvent(tickCtx)
			if err != nil {
				return false, w.absorbTickDeadline(ctx, tickCtx, err)
			}
		}
		if n == 0 {
			return true, nil
		}
		// batchSize に満たない = 未処理イベントが枯れた。
		if n < w.batchSize {
			return true, nil
		}
		if tickCtx.Err() != nil {
			return false, w.absorbTickDeadline(ctx, tickCtx, tickCtx.Err())
		}
	}
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

// applyBatch は候補を1つのトランザクションでまとめて適用し、処理した候補数を返す。
//
// イベント単位トランザクションでは1イベントごとに COMMIT（fsync）が発生し、これが
// スループットの上限を決めていた。バッチ単位にすることで COMMIT 回数が 1/batchSize になる。
// ListPending の FOR UPDATE SKIP LOCKED がそのまま claim として働くため、
// 別途 ClaimByID を発行する必要はない（tx を開いたままロックを保持する）。
//
// payload のデコードはトランザクションの外側の判定で先に済ませ、壊れたイベントは
// バッチから除外して個別に retry 記録する。決定的な失敗でバッチ全体を巻き添えにしないため。
func (w *Worker) applyBatch(ctx context.Context) (int, error) {
	var (
		count      int
		undecoded  []outboxdomain.Event
		decodeErrs []error
	)

	if err := w.tx.DoInTx(ctx, func(tx shared.Tx) error {
		events, err := w.repo.ListPending(ctx, tx, int32(w.batchSize))
		if err != nil {
			return fmt.Errorf("list pending: %w", err)
		}
		count = len(events)
		if count == 0 {
			return nil
		}

		work, bad, errs := buildBatchWork(events)
		undecoded, decodeErrs = bad, errs
		if len(work.ids) == 0 {
			return nil
		}

		// MySQL（source of truth）を先に適用する。
		if err := w.rankingRepo.BulkIncrementGuildScores(ctx, tx, work.guildDeltas); err != nil {
			return fmt.Errorf("mysql bulk increment guild scores: %w", err)
		}
		if err := w.rankingRepo.BulkInsertGuildScoreHistories(ctx, tx, work.histories); err != nil {
			return fmt.Errorf("mysql bulk insert guild score histories: %w", err)
		}
		// Redis（キャッシュ）を最後に適用する。失敗すれば tx がロールバックされ MySQL も巻き戻る。
		if err := w.rankingStore.ApplyScoreDeltas(ctx, work.userDeltas, work.guildDeltas); err != nil {
			return fmt.Errorf("redis apply score deltas: %w", err)
		}
		if err := w.repo.MarkProcessedByIDs(ctx, tx, work.ids); err != nil {
			return fmt.Errorf("mark processed (count=%d): %w", len(work.ids), err)
		}
		return nil
	}, shared.WithIsolation(shared.IsolationReadCommitted)); err != nil {
		return 0, err
	}

	// デコード不能なイベントはバッチのコミット後に個別で retry 記録する
	// （バッチ tx に混ぜると巻き添えでロールバックされるため）。
	for i, ev := range undecoded {
		w.recordRetry(ctx, ev.ID, ev.Type, ev.RetryCount, decodeErrs[i])
	}
	return count, nil
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

// applyPerEvent はイベント単位トランザクションで候補を処理し、処理した候補数を返す。
// バッチ適用が失敗したときのフォールバック経路。1件ずつ独立して commit/rollback するため、
// 恒久的に失敗するイベントがあっても他のイベントは前進する。
func (w *Worker) applyPerEvent(ctx context.Context) (int, error) {
	candidates, err := w.listCandidates(ctx)
	if err != nil {
		return 0, err
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	if err := w.processBatch(ctx, candidates); err != nil {
		return 0, err
	}
	return len(candidates), nil
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

// processBatch は候補を concurrency 本の goroutine で並列に処理する。
// processOne が返すのは claim/mark 自体の失敗（インフラ起因）のみで、handleEvent の失敗は
// processOne 内で retry 記録に変換される。したがってここでエラーを受けたらティックを打ち切る。
// 最初のエラーで派生 context を cancel し、残りの goroutine と投入ループを速やかに終わらせる。
func (w *Worker) processBatch(ctx context.Context, candidates []outboxdomain.Event) error {
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ids := make(chan uint64)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)

	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				if err := w.processOne(batchCtx, id); err != nil {
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
				return firstErr
			}
			return batchCtx.Err()
		}
	}
	close(ids)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	return firstErr
}

// processOne は指定 ID のイベントを1件、個別トランザクションで claim して処理する。
// 既に処理済み / 他 worker がロック中で claim できない場合は何もしない。
// handleEvent が失敗した場合は claim tx をロールバックし（MySQL 副作用も巻き戻る）、
// 別 tx で IncrementRetry を記録する。retry 記録自体の失敗はログのみ（次ティックで再処理）。
func (w *Worker) processOne(ctx context.Context, id uint64) error {
	var (
		handleErr  error
		failedType outboxdomain.EventType
		retryCount uint32
	)

	if err := w.tx.DoInTx(ctx, func(tx shared.Tx) error {
		ev, found, err := w.repo.ClaimByID(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("claim id=%d: %w", id, err)
		}
		if !found {
			return nil
		}

		if herr := w.handleEvent(ctx, tx, ev); herr != nil {
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
		return nil
	}, shared.WithIsolation(shared.IsolationReadCommitted)); err != nil {
		if handleErr == nil {
			// claim / mark 自体の失敗（handleEvent 以外）。ティックを止めて次回リトライ。
			return err
		}
		// handleEvent 失敗: 別 tx で retry を記録する（ベストエフォート）。
		w.recordRetry(ctx, id, failedType, retryCount, handleErr)
	}
	return nil
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

// handleEvent は event_type に応じて副作用を実行する。tx は claim と同一トランザクション。
// MySQL 副作用（source of truth）を先に行い、Redis 反映（キャッシュ、非トランザクショナル）を
// 最後に置く。Redis 失敗時は tx がロールバックされ MySQL も巻き戻るため exactly-once を保つ。
// 新しい event_type を増やす際はここに case を追加する。
func (w *Worker) handleEvent(ctx context.Context, tx shared.Tx, ev outboxdomain.Event) error {
	switch ev.Type {
	case outboxdomain.EventTypeRankingScoreAdded:
		p, err := outboxdomain.UnmarshalRankingScoreAddedPayload(ev.Payload)
		if err != nil {
			return err
		}
		// MySQL: ギルドスコア累計加算と履歴挿入（同期リクエストから移設した集計処理）。
		if err := w.rankingRepo.IncrementGuildScore(ctx, tx, p.GuildID, p.Points); err != nil {
			return fmt.Errorf("mysql increment guild score: %w", err)
		}
		if err := w.rankingRepo.InsertGuildScoreHistory(ctx, tx, p.GuildID, p.UserID, p.Points); err != nil {
			return fmt.Errorf("mysql insert guild score history: %w", err)
		}
		// Redis: ランキング ZSet 反映（最後に実行）。
		if err := w.rankingStore.IncrementUserPoints(ctx, p.UserID, p.Points); err != nil {
			return fmt.Errorf("redis increment user points: %w", err)
		}
		if err := w.rankingStore.IncrementGuildScore(ctx, p.GuildID, p.Points); err != nil {
			return fmt.Errorf("redis increment guild score: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("%w: %s", outboxdomain.ErrUnknownEventType, ev.Type)
	}
}
