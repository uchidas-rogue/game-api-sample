package ranking

import (
	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankingv1 "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/gen/rankingv1"
)

// rankEntriesToProto は domain のランキング行を pb のメッセージへ変換する。
// 空スライスでも nil ではなく長さ 0 のスライスを返す（proto3 では同じ表現になるが、
// 変換側で分岐を作らないため）。
func rankEntriesToProto(entries []rankingdomain.RankEntry) []*rankingv1.RankEntry {
	converted := make([]*rankingv1.RankEntry, 0, len(entries))
	for _, e := range entries {
		converted = append(converted, &rankingv1.RankEntry{
			Rank:  e.Rank,
			Id:    e.ID,
			Name:  e.Name,
			Score: e.Score,
		})
	}
	return converted
}

// userRankToProto は domain のユーザー順位を pb のレスポンスへ変換する。
func userRankToProto(r rankingdomain.UserRankResult) *rankingv1.GetUserRankResponse {
	return &rankingv1.GetUserRankResponse{
		UserId:     r.UserID,
		UserName:   r.UserName,
		Points:     r.Points,
		Rank:       r.Rank,
		TotalUsers: r.TotalUsers,
	}
}

// guildRankToProto は domain のギルド順位を pb のレスポンスへ変換する。
func guildRankToProto(r rankingdomain.GuildRankResult) *rankingv1.GetGuildRankResponse {
	return &rankingv1.GetGuildRankResponse{
		GuildId:     r.GuildID,
		GuildName:   r.GuildName,
		Score:       r.Score,
		Rank:        r.Rank,
		TotalGuilds: r.TotalGuilds,
	}
}

// userPointAddToProto は domain のポイント加算結果を pb のレスポンスへ変換する。
//
// 順位（rank / guild_rank）は worker による Redis 反映後にしか確定しないため、
// proto のメッセージ自体が持っていない。フィールドを足す前に
// docs/testing/ranking.md を読むこと。
func userPointAddToProto(r rankingdomain.UserPointAddResult) *rankingv1.AddUserPointsResponse {
	return &rankingv1.AddUserPointsResponse{
		UserId:        r.UserID,
		Points:        r.Points,
		PreviousTotal: r.PreviousTotal,
		NewTotal:      r.NewTotal,
		GuildId:       r.GuildID,
	}
}
