package outbox

//go:generate mockgen -source=notifier.go -destination=mock/mock_notifier.go -package=mock_outbox

import "context"

// Notifier は outbox にイベントが追加されたことを worker に通知する。
// リクエスト経路でトランザクションコミット直後に呼び出される。
// 失敗してもリクエストは継続させる（worker のポーリングがフォールバック）。
type Notifier interface {
	Notify(ctx context.Context) error
}

// Subscriber は worker 側で通知を購読する。
// Subscribe は ctx キャンセルで終了し、戻り値のチャネルはその時点でクローズされる。
// 通知の取りこぼしは想定内（ポーリングがフォールバック）。
type Subscriber interface {
	Subscribe(ctx context.Context) (<-chan struct{}, error)
}
