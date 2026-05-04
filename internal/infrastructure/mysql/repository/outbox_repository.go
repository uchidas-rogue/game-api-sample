package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

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
func (r *OutboxRepository) ListPending(ctx context.Context, tx shared.Tx, limit int32) ([]outboxdomain.Event, error) {
	q, err := r.querier(tx)
	if err != nil {
		return nil, fmt.Errorf("ListPending: %w", err)
	}
	rows, err := q.ListPendingOutboxEvents(ctx, limit)
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

// GetMaxID は outbox_events の現在の最大 ID を返す（空テーブル時は 0）。
func (r *OutboxRepository) GetMaxID(ctx context.Context, tx shared.Tx) (uint64, error) {
	q, err := r.querier(tx)
	if err != nil {
		return 0, fmt.Errorf("GetMaxID: %w", err)
	}
	maxID, err := q.GetMaxOutboxEventID(ctx)
	if err != nil {
		return 0, fmt.Errorf("get max outbox event id: %w", err)
	}
	if maxID < 0 {
		return 0, fmt.Errorf("unexpected negative max id: %d", maxID)
	}
	return uint64(maxID), nil
}

// MarkProcessedUpTo は指定 ID 以下の pending イベントを一括で処理済みにマークする。
func (r *OutboxRepository) MarkProcessedUpTo(ctx context.Context, tx shared.Tx, maxID uint64) (int64, error) {
	q, err := r.querier(tx)
	if err != nil {
		return 0, fmt.Errorf("MarkProcessedUpTo: %w", err)
	}
	rows, err := q.MarkOutboxEventsProcessedUpTo(ctx, maxID)
	if err != nil {
		return 0, fmt.Errorf("mark outbox events processed up to: %w", err)
	}
	return rows, nil
}

// IncrementRetry は retry_count をインクリメントし last_error を記録する。
func (r *OutboxRepository) IncrementRetry(ctx context.Context, tx shared.Tx, id uint64, lastError string) error {
	q, err := r.querier(tx)
	if err != nil {
		return fmt.Errorf("IncrementRetry: %w", err)
	}
	if err := q.IncrementOutboxEventRetry(ctx, sqlc.IncrementOutboxEventRetryParams{
		ID:        id,
		LastError: sql.NullString{String: lastError, Valid: lastError != ""},
	}); err != nil {
		return fmt.Errorf("increment outbox event retry: %w", err)
	}
	return nil
}
