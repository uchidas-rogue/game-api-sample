package ranking

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
