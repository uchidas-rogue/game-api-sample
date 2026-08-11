package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	outboxusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

var _ outboxusecase.Repository = (*OutboxRepository)(nil)

// OutboxRepository は outboxusecase.Repository の sqlc/MySQL 実装。
type OutboxRepository struct {
	querier querierFactory
}

// NewOutboxRepository は OutboxRepository を生成する。
func NewOutboxRepository(db sqlc.DBTX) *OutboxRepository {
	return &OutboxRepository{
		querier: newQuerierFactory(db),
	}
}

// InsertEvent は outbox にイベントを登録する。
func (r *OutboxRepository) InsertEvent(ctx context.Context, tx shared.Tx, eventType outboxdomain.EventType, payload []byte) (uint64, error) {
	q, err := r.querier(tx)
	if err != nil {
		return 0, fmt.Errorf("InsertEvent: %w", err)
	}
	id, err := q.InsertOutboxEvent(ctx, sqlc.InsertOutboxEventParams{
		EventType: string(eventType),
		Payload:   json.RawMessage(payload),
	})
	if err != nil {
		return 0, fmt.Errorf("insert outbox event: %w", err)
	}
	if id < 0 {
		return 0, fmt.Errorf("unexpected negative last insert id: %d", id)
	}
	return uint64(id), nil
}

// ListPending は未処理イベントを古い順に取得する（FOR UPDATE SKIP LOCKED）。
// retry_count が maxRetry に達したイベントは候補に含めない。
func (r *OutboxRepository) ListPending(ctx context.Context, tx shared.Tx, limit int32, maxRetry uint32) ([]outboxdomain.Event, error) {
	q, err := r.querier(tx)
	if err != nil {
		return nil, fmt.Errorf("ListPending: %w", err)
	}
	rows, err := q.ListPendingOutboxEvents(ctx, sqlc.ListPendingOutboxEventsParams{
		MaxRetry: maxRetry,
		Limit:    limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list pending outbox events: %w", err)
	}
	events := make([]outboxdomain.Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, outboxdomain.Event{
			ID:         row.ID,
			Type:       outboxdomain.EventType(row.EventType),
			Payload:    []byte(row.Payload),
			RetryCount: row.RetryCount,
		})
	}
	return events, nil
}

// ClaimByID は指定 ID の未処理イベントを FOR UPDATE SKIP LOCKED で確保する。
// 該当なし（処理済み / 他 worker がロック中 / retry 上限到達 = sql.ErrNoRows）は
// found=false を返し、エラーにはしない。
func (r *OutboxRepository) ClaimByID(ctx context.Context, tx shared.Tx, id uint64, maxRetry uint32) (outboxdomain.Event, bool, error) {
	q, err := r.querier(tx)
	if err != nil {
		return outboxdomain.Event{}, false, fmt.Errorf("ClaimByID: %w", err)
	}
	row, err := q.ClaimPendingOutboxEventByID(ctx, sqlc.ClaimPendingOutboxEventByIDParams{
		ID:       id,
		MaxRetry: maxRetry,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return outboxdomain.Event{}, false, nil
		}
		return outboxdomain.Event{}, false, fmt.Errorf("claim pending outbox event by id=%d: %w", id, err)
	}
	return outboxdomain.Event{
		ID:         row.ID,
		Type:       outboxdomain.EventType(row.EventType),
		Payload:    []byte(row.Payload),
		RetryCount: row.RetryCount,
	}, true, nil
}

// MarkProcessed は処理済みフラグを立てる。
func (r *OutboxRepository) MarkProcessed(ctx context.Context, tx shared.Tx, id uint64) error {
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("MarkProcessed: %w", err)
	}
	if err := q.MarkOutboxEventProcessed(ctx, id); err != nil {
		return fmt.Errorf("mark outbox event processed: %w", err)
	}
	return nil
}

// MarkProcessedByIDs は複数イベントを1文で処理済みにマークする。
// 可変長 IN 句は sqlc.slice() で生成しているため、生 SQL の組み立ては不要。
func (r *OutboxRepository) MarkProcessedByIDs(ctx context.Context, tx shared.Tx, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("MarkProcessedByIDs: %w", err)
	}
	if err := q.MarkOutboxEventsProcessedByIDs(ctx, ids); err != nil {
		return fmt.Errorf("mark outbox events processed (count=%d): %w", len(ids), err)
	}
	return nil
}

// DeleteProcessedBefore は処理済みイベントのうち retention より古いものを最大 limit 件削除する。
// 基準時刻は SQL 側の NOW(6) で取るため、ここでは保持期間を秒に落として渡すだけ。
func (r *OutboxRepository) DeleteProcessedBefore(
	ctx context.Context, tx shared.Tx, retention time.Duration, limit int32,
) (int64, error) {
	q, err := r.querier(tx)
	if err != nil {
		return 0, fmt.Errorf("DeleteProcessedBefore: %w", err)
	}
	deleted, err := q.DeleteProcessedOutboxEventsBefore(ctx, sqlc.DeleteProcessedOutboxEventsBeforeParams{
		RetentionSeconds: int64(retention.Seconds()),
		Limit:            limit,
	})
	if err != nil {
		return 0, fmt.Errorf("delete processed outbox events (retention=%s, limit=%d): %w", retention, limit, err)
	}
	return deleted, nil
}

// IncrementRetry は retry_count をインクリメントし last_error を記録する。
// 加算後の retry_count は同一トランザクション内で読み直して返す。MySQL には
// UPDATE ... RETURNING が無いため2文になるが、UPDATE が取った排他ロックの下で読むので
// 他 worker の加算が割り込むことはなく、返す値は自分の加算結果そのものになる。
func (r *OutboxRepository) IncrementRetry(ctx context.Context, tx shared.Tx, id uint64, lastError string) (uint32, error) {
	q, err := r.querier(tx)
	if err != nil {
		return 0, fmt.Errorf("IncrementRetry: %w", err)
	}
	if err := q.IncrementOutboxEventRetry(ctx, sqlc.IncrementOutboxEventRetryParams{
		ID:        id,
		LastError: sql.NullString{String: lastError, Valid: lastError != ""},
	}); err != nil {
		return 0, fmt.Errorf("increment outbox event retry: %w", err)
	}
	retryCount, err := q.GetOutboxEventRetryCount(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("get outbox event retry count id=%d: %w", id, err)
	}
	return retryCount, nil
}
