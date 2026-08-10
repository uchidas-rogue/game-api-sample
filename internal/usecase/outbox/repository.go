// Package outbox は Outbox パターンのユースケース抽象を提供する。
package outbox

//go:generate mockgen -source=repository.go -destination=mock/mock_repository.go -package=mock_outbox

import (
	"context"
	"time"

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

	// MarkProcessedByIDs は複数イベントを1文で処理済みにマークする。
	// worker がバッチ単位トランザクションで適用する際に、イベント件数ぶんの
	// UPDATE 往復を1回に集約するために使う。ids が空の場合は何もしない。
	MarkProcessedByIDs(ctx context.Context, tx shared.Tx, ids []uint64) error

	// IncrementRetry は失敗時のリトライ回数を加算し、エラーメッセージを記録する。
	IncrementRetry(ctx context.Context, tx shared.Tx, id uint64, lastError string) error

	// DeleteProcessedBefore は処理済みイベントのうち retention より古いものを
	// 最大 limit 件削除し、削除件数を返す。未処理イベントは対象にしない。
	//
	// 基準時刻ではなく保持期間を受け取るのは、実装が SQL 側の NOW() を使い
	// アプリ側で現在時刻を取得しないようにするため（AGENTS.md §2 の Clock 規約）。
	// limit で分割するのは、1文で全削除すると undo ログとロック保持が肥大し、
	// リクエスト経路の InsertEvent を阻害するため。呼び出し側は
	// 「削除件数 < limit」になるまで繰り返す。
	DeleteProcessedBefore(ctx context.Context, tx shared.Tx, retention time.Duration, limit int32) (deleted int64, err error)
}
