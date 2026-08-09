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

	// ListPending は未処理イベントを古い順に候補として取得する。worker はこの候補を1件ずつ
	// ClaimByID で確保して処理する。tx 内で行をロックしつつ取得するため tx は必須。
	ListPending(ctx context.Context, tx shared.Tx, limit int32) ([]outboxdomain.Event, error)

	// ClaimByID は指定 ID の未処理イベントを FOR UPDATE SKIP LOCKED で確保する。
	// worker はイベント単位トランザクションで処理するため本メソッドで1件ずつロックする。
	// 該当なし（処理済み or 他 worker がロック中）は found=false（エラーにしない）。
	// ID 指定にすることで先頭イベントの恒久失敗が後続を止めない（head-of-line blocking 回避）。
	ClaimByID(ctx context.Context, tx shared.Tx, id uint64) (event outboxdomain.Event, found bool, err error)

	// MarkProcessed は指定 ID のイベントを処理済みにマークする。
	MarkProcessed(ctx context.Context, tx shared.Tx, id uint64) error

	// IncrementRetry は失敗時のリトライ回数を加算し、エラーメッセージを記録する。
	IncrementRetry(ctx context.Context, tx shared.Tx, id uint64, lastError string) error
}
