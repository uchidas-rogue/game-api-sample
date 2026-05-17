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
// 1ティックの処理は単一の DB トランザクション内で行う:
//   1. ListPending で未処理イベントを FOR UPDATE SKIP LOCKED で取得（複数 worker 並行安全）
//   2. event_type ごとに dispatch して Redis 反映等を実行
//   3. 成功したイベントは MarkProcessed、失敗したものは IncrementRetry + last_error 記録
//
// max retry 上限・DLQ・処理済みレコードの GC は本実装では未対応（後続課題）。
type Worker struct {
	repo         outboxusecase.Repository
	rankingStore rankingusecase.RankingStore
	tx           shared.Transactor
	subscriber   outboxusecase.Subscriber
	logger       *slog.Logger
	pollInterval time.Duration
	batchSize    int32
	tickTimeout  time.Duration
}

// Config は Worker のコンストラクタ引数。
type Config struct {
	Repo         outboxusecase.Repository
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
		rankingStore: cfg.RankingStore,
		tx:           cfg.Tx,
		subscriber:   cfg.Subscriber,
		logger:       cfg.Logger,
		pollInterval: cfg.PollInterval,
		batchSize:    int32(cfg.BatchSize),
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

// runOnce は1ティックぶんの処理を実行する。トランザクション境界はここに集約。
// tickTimeout > 0 のとき、DB/Redis のブロッキングでループがハングしないよう
// ティックごとに deadline 付き context を被せる。
func (w *Worker) runOnce(ctx context.Context) error {
	if w.tickTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.tickTimeout)
		defer cancel()
	}
	return w.tx.DoInTx(ctx, func(tx shared.Tx) error {
		events, err := w.repo.ListPending(ctx, tx, w.batchSize)
		if err != nil {
			return fmt.Errorf("list pending: %w", err)
		}
		for _, ev := range events {
			if err := w.handleEvent(ctx, tx, ev); err != nil {
				w.logger.WarnContext(ctx, "outbox event handling failed",
					slog.Uint64("event_id", ev.ID),
					slog.String("event_type", string(ev.Type)),
					slog.Uint64("retry_count", uint64(ev.RetryCount)),
					slog.Any("error", err),
				)
				if rerr := w.repo.IncrementRetry(ctx, tx, ev.ID, err.Error()); rerr != nil {
					return fmt.Errorf("increment retry id=%d: %w", ev.ID, rerr)
				}
				continue
			}
			if err := w.repo.MarkProcessed(ctx, tx, ev.ID); err != nil {
				return fmt.Errorf("mark processed id=%d: %w", ev.ID, err)
			}
		}
		return nil
	})
}

// handleEvent は event_type に応じて処理を分岐する。
// 新しい event_type を増やす際はここに case を追加する。
func (w *Worker) handleEvent(ctx context.Context, _ shared.Tx, ev outboxdomain.Event) error {
	switch ev.Type {
	case outboxdomain.EventTypeRankingScoreAdded:
		p, err := outboxdomain.UnmarshalRankingScoreAddedPayload(ev.Payload)
		if err != nil {
			return err
		}
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
