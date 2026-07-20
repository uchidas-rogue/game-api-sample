// Package outbox は Outbox を購読して外部副作用を実行する worker を提供する。
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	outboxusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// Worker は outbox_events をポーリングし、event_type に応じて副作用を実行する。
//
// 1ティックで最大 batchSize 件を処理する。まず ListPending で未処理イベントの候補を取得し、
// 各候補を**イベント単位トランザクション**で処理する:
//   1. ClaimByID で候補を id 指定で FOR UPDATE SKIP LOCKED 確保（複数 worker 並行安全）。
//      既に処理済み / 他 worker がロック中なら skip。
//   2. handleEvent で MySQL 副作用（guild_scores 加算・guild_score_histories 挿入）と
//      Redis 反映を実行
//   3. 成功なら同一 tx で MarkProcessed → COMMIT（MySQL は exactly-once）
//   4. handleEvent 失敗なら tx を ROLLBACK し、別 tx で IncrementRetry + last_error 記録
//
// イベント単位 tx にする理由: handleEvent が MySQL guild_scores を加算した後に Redis 反映が
// 失敗した場合、その MySQL 加算はロールバックされ（未マークのまま）、再試行で1回だけ適用される。
// バッチ単位 tx だと、Redis 失敗イベントの MySQL 加算が同バッチの他イベントと共にコミットされ、
// 未マークゆえ再試行で二重加算になりうる。
//
// 候補を id 指定で claim する理由: 先頭イベントが恒久失敗しても後続の候補を処理でき、
// head-of-line blocking を避ける（最小 id 固定 claim だと poison イベントが後続を止める）。
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
	// TickTimeout は1ティック（runOnce）の処理時間上限。
	// DB/Redis のブロッキングでループがハングするのを防ぐ。0 の場合は無制限。
	TickTimeout time.Duration
}

// New は Worker を生成する。Config.Logger は呼び出し側で必ず初期化済みのものを渡す。
func New(cfg Config) *Worker {
	return &Worker{
		repo:         cfg.Repo,
		rankingRepo:  cfg.RankingRepo,
		rankingStore: cfg.RankingStore,
		tx:           cfg.Tx,
		subscriber:   cfg.Subscriber,
		logger:       cfg.Logger,
		pollInterval: cfg.PollInterval,
		batchSize:    cfg.BatchSize,
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
	if err := w.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		w.logger.ErrorContext(ctx, "outbox tick failed", slog.Any("error", err))
	}

	for {
		select {
		case <-ctx.Done():
			w.logger.InfoContext(ctx, "outbox worker stopped")
			return nil
		case <-ticker.C:
			if err := w.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.ErrorContext(ctx, "outbox tick failed", slog.Any("error", err))
			}
		case _, ok := <-notifyCh:
			if !ok {
				// 購読チャネルがクローズされた場合は以降ポーリングのみで継続。
				notifyCh = nil
				continue
			}
			if err := w.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				w.logger.ErrorContext(ctx, "outbox notify-triggered run failed", slog.Any("error", err))
			}
		}
	}
}

// runOnce は1ティックぶんの処理を実行する。まず ListPending で未処理イベントの候補を最大
// batchSize 件取得し、各候補を id 指定で1件ずつ個別トランザクションで処理する。
// 候補を id 指定で claim することで、先頭イベントが恒久失敗しても後続を処理でき
// （head-of-line blocking 回避）、per-event tx の原子性と複数 worker 安全性を両立する。
// tickTimeout > 0 のとき、ティック全体に deadline 付き context を被せる。
func (w *Worker) runOnce(ctx context.Context) error {
	if w.tickTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.tickTimeout)
		defer cancel()
	}

	var candidates []outboxdomain.Event
	if err := w.tx.DoInTx(ctx, func(tx shared.Tx) error {
		events, err := w.repo.ListPending(ctx, tx, int32(w.batchSize))
		if err != nil {
			return fmt.Errorf("list pending: %w", err)
		}
		candidates = events
		return nil
	}); err != nil {
		return err
	}

	for _, ev := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.processOne(ctx, ev.ID); err != nil {
			return err
		}
	}
	return nil
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
	}); err != nil {
		if handleErr == nil {
			// claim / mark 自体の失敗（handleEvent 以外）。ティックを止めて次回リトライ。
			return err
		}
		// handleEvent 失敗: 別 tx で retry を記録する（ベストエフォート）。
		w.logger.WarnContext(ctx, "outbox event handling failed",
			slog.Uint64("event_id", id),
			slog.String("event_type", string(failedType)),
			slog.Uint64("retry_count", uint64(retryCount)),
			slog.Any("error", handleErr),
		)
		if rerr := w.tx.DoInTx(ctx, func(tx shared.Tx) error {
			return w.repo.IncrementRetry(ctx, tx, id, handleErr.Error())
		}); rerr != nil {
			w.logger.ErrorContext(ctx, "outbox increment retry failed",
				slog.Uint64("event_id", id),
				slog.Any("error", rerr),
			)
		}
	}
	return nil
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
