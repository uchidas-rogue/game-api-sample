// Package outbox は Outbox パターンのユースケース抽象を提供する。
package outbox

//go:generate mockgen -source=repository.go -destination=mock/mock_repository.go -package=mock_outbox

import (
	"context"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// Repository は outbox_events テーブルへの操作を抽象化する。
//
// InsertEvent はリクエスト経路の業務トランザクション内で呼び出される。
// その他のメソッドは worker から呼び出される。
type Repository interface {
	// InsertEvent は outbox にイベントを登録する。tx は業務トランザクションを引き回す。
	InsertEvent(ctx context.Context, tx shared.Tx, eventType outboxdomain.EventType, payload []byte) (id uint64, err error)

	// ListPending は未処理イベントを古い順に取得する。worker は tx 内で行をロックしつつ処理するため tx は必須。
	ListPending(ctx context.Context, tx shared.Tx, limit int32) ([]outboxdomain.Event, error)

	// MarkProcessed は指定 ID のイベントを処理済みにマークする。
	MarkProcessed(ctx context.Context, tx shared.Tx, id uint64) error

	// IncrementRetry は失敗時のリトライ回数を加算し、エラーメッセージを記録する。
	IncrementRetry(ctx context.Context, tx shared.Tx, id uint64, lastError string) error

	// GetMaxID は outbox_events テーブルの現在の最大 ID を返す（空テーブル時は 0）。
	// ranking 同期バッチがスナップショット境界として使用する。
	GetMaxID(ctx context.Context, tx shared.Tx) (uint64, error)

	// MarkProcessedUpTo は指定 ID 以下、かつ eventType が一致する pending イベントを一括で
	// 処理済みにマークし、マークされた件数を返す。バッチが「DB を読んだ時点までに COMMIT 済みの
	// 対象イベント」を processed として確定させるために使用する。
	MarkProcessedUpTo(ctx context.Context, tx shared.Tx, maxID uint64, eventType outboxdomain.EventType) (int64, error)
}
