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
}
