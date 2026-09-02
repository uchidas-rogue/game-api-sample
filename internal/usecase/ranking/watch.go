package ranking

//go:generate mockgen -source=watch.go -destination=mock/mock_watch.go -package=mock_ranking

import (
	"context"
	"errors"
)

// ErrWatcherStopped は停止済みのハブへ購読を要求したことを表す。
// Run の ctx が終わった後に WatchUserRankings を呼ぶと返る。
// ビジネスルールではなく常駐プロセスのライフサイクルの話なので domain には置かない。
var ErrWatcherStopped = errors.New("ranking watcher is stopped")

// ErrWatchSubscriptionClosed は更新通知の購読が切れたことを表す。
// この経路には outbox worker のようなポーリングのフォールバックが無く、黙って止まると
// 「繋がっているのに永久に更新が来ないストリーム」になるため、Run はエラーとして返す。
var ErrWatchSubscriptionClosed = errors.New("ranking update subscription closed")

// RankingUpdateNotifier はランキング ZSet への反映が完了したことを通知する。
// outbox-worker が ApplyScoreDeltas に成功した直後にだけ呼ぶ。
// 失敗してもワーカーは止めない（取りこぼしは次の更新で追いつく）。
type RankingUpdateNotifier interface {
	NotifyUpdated(ctx context.Context) error
}

// RankingUpdateSubscriber は「ランキング ZSet が更新された」通知の購読を抽象化する。
// 実装は infrastructure（Redis Pub/Sub）。ctx キャンセルでチャネルはクローズされる。
// 通知の取りこぼしは想定内（次の更新で追いつく）。
type RankingUpdateSubscriber interface {
	Subscribe(ctx context.Context) (<-chan struct{}, error)
}

// Watcher はランキングの更新を購読者へ push する。
// 戻り値のチャネルは ctx キャンセル時とハブ停止時に必ずクローズされる。
// 受信が追いつかない購読者には最新値だけを届ける（途中の値は捨てる）。
type Watcher interface {
	// WatchUserRankings はユーザーランキングの購読を開始する。
	// 購読登録の直後に現在値を1件送るため、呼び出し側は初期表示のために
	// 別途 GetUserRankings を呼ぶ必要がない。
	WatchUserRankings(ctx context.Context, limit int) (<-chan RankingsResult, error)
}
