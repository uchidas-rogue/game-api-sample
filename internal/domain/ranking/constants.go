package ranking

import "time"

// ランキング取得の制限値。
const (
	DefaultRankingLimit = 10
	MaxRankingLimit     = 100
	MinRankingLimit     = 1
)

// スコア・ポイントの制約。
const (
	MinScore = 0
	MaxScore = 999999999999
)

// Redis キー。
const (
	GuildRankingKey = "ranking:guilds"
	UserRankingKey  = "ranking:users"
	// RankingInitializedKey はランキング ZSet が構築済みであることを示すセンチネルキー。
	// 揮発（ノード障害・フェイルオーバ・人為ミス）すると ZSet と一緒に消えるため、
	// 「空だが正常」と「揮発して空」を区別する唯一の手掛かりになる。
	// TTL を付けてはならない（期限切れが偽陽性になる）。
	RankingInitializedKey = "ranking:meta:initialized"
)

// Redis Pub/Sub チャネル。
const (
	// RankingUpdatedChannel はランキング ZSet への反映が完了したことを配信するチャネル。
	// outbox-worker が ApplyScoreDeltas に成功した直後にだけ publish する。
	//
	// outbox の起床通知（infrastructure 層の outboxEventsChannel）を転用してはならない。
	// あれは ZSet 反映の「前」に飛ぶ通知なので、購読して読むと反映前の古い値を配ることになる
	// （docs/testing/ranking-watch.md §0-1）。
	RankingUpdatedChannel = "ranking:updated"
)

// エラー応答に使う再試行間隔。
const (
	// RankingUnavailableRetryAfter はランキング未構築（Redis揮発を含む）時にクライアントへ
	// 提示する再試行までの待ち時間。復旧は再構築バッチの手動実行を伴うため、即時の再試行が
	// 実る値にはしない。HTTP の Retry-After ヘッダと gRPC の RetryInfo で同じ値を使うため
	// ここに一本化する。
	RankingUnavailableRetryAfter = 30 * time.Second
)

// IsValidScore はスコア・ポイントが有効範囲内かを判定する。
func IsValidScore(score int64) bool {
	return score >= MinScore && score <= MaxScore
}

// NormalizeLimit はlimitを有効範囲に正規化する。
func NormalizeLimit(limit int) int {
	if limit < MinRankingLimit {
		return DefaultRankingLimit
	}
	if limit > MaxRankingLimit {
		return MaxRankingLimit
	}
	return limit
}

// NormalizeOffset はoffsetを有効範囲に正規化する。負数は0に丸める。
func NormalizeOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}
