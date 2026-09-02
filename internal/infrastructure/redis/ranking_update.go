package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
)

// RankingUpdateNotifier が usecase 層の RankingUpdateNotifier を満たすことをコンパイル時に検証する。
var _ rankingusecase.RankingUpdateNotifier = (*RankingUpdateNotifier)(nil)

// RankingUpdateNotifier は ranking ZSet への反映完了を Redis Pub/Sub で配信する Publisher 実装。
//
// 意図的に「publish するだけ」の薄いアダプタにしてある。購読者の管理・差分判定・
// 遅い購読者の扱いといった判断はすべて usecase 層の RankingWatcher に置き、
// この層には Pub/Sub の呼び出し以外のロジックを持たせない
// （miniredis の Pub/Sub 互換性に検証を依存させないため。docs/testing/ranking-watch.md §0-2）。
type RankingUpdateNotifier struct {
	client *redis.Client
}

// NewRankingUpdateNotifier は RankingUpdateNotifier を生成する。
func NewRankingUpdateNotifier(client *redis.Client) *RankingUpdateNotifier {
	return &RankingUpdateNotifier{client: client}
}

// NotifyUpdated はランキング ZSet の更新を通知する。
// メッセージ内容は使わない（購読側は通知を受けてから改めてランキングを読む）ため空文字列を送る。
func (n *RankingUpdateNotifier) NotifyUpdated(ctx context.Context) error {
	if err := n.client.Publish(ctx, rankingdomain.RankingUpdatedChannel, "").Err(); err != nil {
		return fmt.Errorf("redis publish %s: %w", rankingdomain.RankingUpdatedChannel, err)
	}
	return nil
}

// RankingUpdateSubscriber が usecase 層の RankingUpdateSubscriber を満たすことをコンパイル時に検証する。
var _ rankingusecase.RankingUpdateSubscriber = (*RankingUpdateSubscriber)(nil)

// RankingUpdateSubscriber は ranking 更新通知を購読する Subscriber 実装。
// Notifier と同じく、シグナルを流す以外の判断を持たない薄いアダプタ。
type RankingUpdateSubscriber struct {
	client *redis.Client
}

// NewRankingUpdateSubscriber は RankingUpdateSubscriber を生成する。
func NewRankingUpdateSubscriber(client *redis.Client) *RankingUpdateSubscriber {
	return &RankingUpdateSubscriber{client: client}
}

// Subscribe は Redis Pub/Sub の購読を開始し、通知シグナル用チャネルを返す。
// ctx 終了時に PubSub を Close し、戻り値チャネルもクローズする。
//
// 受信側のドレインが追いつかない場合は通知を捨てる。ここで捨ててよいのは、
// このチャネルが**値を運ばないシグナル**で新旧の区別が無いため（購読者へ配る
// ランキングの値そのものは RankingWatcher が最新値で上書きして持つ）。
func (s *RankingUpdateSubscriber) Subscribe(ctx context.Context) (<-chan struct{}, error) {
	pubsub := s.client.Subscribe(ctx, rankingdomain.RankingUpdatedChannel)
	// Subscribe 自体は遅延確立のため、初回受信を保証するためここで Receive する。
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, fmt.Errorf("redis subscribe %s: %w", rankingdomain.RankingUpdatedChannel, err)
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
