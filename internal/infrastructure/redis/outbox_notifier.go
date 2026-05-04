package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// outboxEventsChannel は outbox イベント追加通知の Redis Pub/Sub チャネル名。
const outboxEventsChannel = "outbox:events"

// OutboxNotifier は Redis Pub/Sub で worker に通知する Publisher 実装。
type OutboxNotifier struct {
	client *redis.Client
}

// NewOutboxNotifier は OutboxNotifier を生成する。
func NewOutboxNotifier(client *redis.Client) *OutboxNotifier {
	return &OutboxNotifier{client: client}
}

// Notify は outbox にイベントが追加されたことを通知する。
// メッセージ内容は使用せず（worker は DB から実データを取得する）、空文字列を送る。
func (n *OutboxNotifier) Notify(ctx context.Context) error {
	if err := n.client.Publish(ctx, outboxEventsChannel, "").Err(); err != nil {
		return fmt.Errorf("redis publish %s: %w", outboxEventsChannel, err)
	}
	return nil
}

// OutboxSubscriber は Redis Pub/Sub で通知を購読する Subscriber 実装。
type OutboxSubscriber struct {
	client *redis.Client
}

// NewOutboxSubscriber は OutboxSubscriber を生成する。
func NewOutboxSubscriber(client *redis.Client) *OutboxSubscriber {
	return &OutboxSubscriber{client: client}
}

// Subscribe は Redis Pub/Sub の購読を開始し、通知シグナル用チャネルを返す。
// ctx 終了時に PubSub を Close し、戻り値チャネルもクローズする。
// 受信側のドレインが追いつかない場合は通知を捨てる（取りこぼしはポーリングがフォールバック）。
func (s *OutboxSubscriber) Subscribe(ctx context.Context) (<-chan struct{}, error) {
	pubsub := s.client.Subscribe(ctx, outboxEventsChannel)
	// Subscribe 自体は遅延確立のため、初回受信を保証するためここで Receive する。
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("redis subscribe %s: %w", outboxEventsChannel, err)
	}

	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		defer func() { _ = pubsub.Close() }()
		ch := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				// バッファが埋まっている場合は捨てる（既に通知済み相当）。
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out, nil
}
