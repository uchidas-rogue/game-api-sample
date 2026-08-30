package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	infraRedis "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/redis"
)

// ---------------------------------------------------------------------------
// ranking:updated の Pub/Sub アダプタのテスト。
//
// 本ファイルが検証するのは「publish する」「購読してシグナルを流す」「ctx 終了で
// チャネルを閉じる」だけ。購読者の管理・差分判定・遅い購読者の扱いといった判断は
// usecase 層の RankingWatcher にあり、そちらはモックで検証している
// （docs/testing/ranking-watch.md §0-2）。
//
// miniredis の Pub/Sub は実 Redis と完全互換ではないため、ここでは
// 「1メッセージを publish して1シグナルを受ける」以上の挙動（購読の再確立、
// 大量メッセージ時の取りこぼし方など）には依存しない。
// ---------------------------------------------------------------------------

// pubsubTimeout は「起きるはずのこと」を待つ上限。CI の高負荷を見込んで広めに取る。
const pubsubTimeout = 2 * time.Second

// newRankingUpdatePubSub は miniredis 上に Notifier / Subscriber の組を作る。
func newRankingUpdatePubSub(t *testing.T) (*infraRedis.RankingUpdateNotifier, *infraRedis.RankingUpdateSubscriber) {
	t.Helper()
	s := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: s.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return infraRedis.NewRankingUpdateNotifier(client), infraRedis.NewRankingUpdateSubscriber(client)
}

// newClosedClient は接続先を失った（Close 済みの）クライアントを返す。
// Redis 側の失敗を決定的に起こすために使う。
func newClosedClient(t *testing.T) *goredis.Client {
	t.Helper()
	s := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: s.Addr()})
	require.NoError(t, client.Close())
	return client
}

// NotifyUpdated が publish 成功時にエラーを返さないこと、
// および購読者へ届くことを確認する。購読者ゼロでも失敗しない。
func TestRankingUpdateNotifier_NotifyUpdated_購読者へシグナルが届く(t *testing.T) {
	t.Parallel()

	notifier, subscriber := newRankingUpdatePubSub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 購読者が居ない状態でも publish 自体は成功する。
	require.NoError(t, notifier.NotifyUpdated(ctx))

	ch, err := subscriber.Subscribe(ctx)
	require.NoError(t, err)

	require.NoError(t, notifier.NotifyUpdated(ctx))

	select {
	case _, ok := <-ch:
		assert.True(t, ok, "シグナルを受け取る前にチャネルが閉じられた")
	case <-time.After(pubsubTimeout):
		t.Fatal("publish したシグナルが購読側に届かなかった")
	}
}

// 接続が壊れている場合、NotifyUpdated はチャネル名を含むエラーを返す。
func TestRankingUpdateNotifier_NotifyUpdated_接続断_エラーを返す(t *testing.T) {
	t.Parallel()

	notifier := infraRedis.NewRankingUpdateNotifier(newClosedClient(t))

	err := notifier.NotifyUpdated(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), rankingdomain.RankingUpdatedChannel,
		"どのチャネルへの publish が失敗したかをエラーに残す")
}

// 接続が壊れている場合、Subscribe はチャネルを返さずエラーを返す。
// go-redis の Subscribe は遅延確立のため、Receive を明示的に呼んでいないと
// 「購読できていないのにチャネルだけ返る」状態になる。
func TestRankingUpdateSubscriber_Subscribe_接続断_チャネルを返さない(t *testing.T) {
	t.Parallel()

	subscriber := infraRedis.NewRankingUpdateSubscriber(newClosedClient(t))

	ch, err := subscriber.Subscribe(context.Background())

	require.Error(t, err)
	assert.Nil(t, ch)
	assert.Contains(t, err.Error(), rankingdomain.RankingUpdatedChannel)
}

// ctx をキャンセルすると購読が終了し、返したチャネルがクローズされる。
// 閉じ忘れると購読側（RankingWatcher.Run）が終了を検知できず常駐し続ける。
func TestRankingUpdateSubscriber_Subscribe_ctxキャンセル_チャネルが閉じる(t *testing.T) {
	t.Parallel()

	_, subscriber := newRankingUpdatePubSub(t)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := subscriber.Subscribe(ctx)
	require.NoError(t, err)

	cancel()

	deadline := time.After(pubsubTimeout)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("ctx キャンセル後もチャネルが閉じられなかった（goroutine リーク）")
		}
	}
}
