package ranking_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankingv1 "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/gen/rankingv1"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
)

// TestProtoDomainFieldParity は pb メッセージと、対応する Go 構造体の
// **フィールド集合**が一致することを検証する。
//
// HTTP 側の testdata/contracts/*.json に相当する保証だが、gRPC では proto ファイル
// 自体が構造の正本なので JSON の写しは作らない。代わりに proto と Go の対応を突合し、
// 「domain / usecase にフィールドを足して proto へ写し忘れた」（およびその逆）を検出する。
//
// 見るのはフィールド名の集合だけで、型・意味の対応は見ない（値の妥当性は
// handler_test.go の責務）。意図的な非対称が出たら、ケースへ理由付きの除外セットを
// 足すこと。現時点で除外は 1 件も無い。
//
// docs/testing/grpc-ranking.md「6. proto ⇄ domain のフィールド対応」の表と 1 対 1 で対応する。
func TestProtoDomainFieldParity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// msg は pb 側の代表インスタンス（フィールド記述子を読むだけなのでゼロ値でよい）。
		msg proto.Message
		// goType は対応する Go 構造体。
		goType reflect.Type
	}{
		{
			// #1
			name:   "RankEntry ⇄ domain.RankEntry",
			msg:    &rankingv1.RankEntry{},
			goType: reflect.TypeOf(rankingdomain.RankEntry{}),
		},
		{
			// #2
			name:   "GetUserRankResponse ⇄ domain.UserRankResult",
			msg:    &rankingv1.GetUserRankResponse{},
			goType: reflect.TypeOf(rankingdomain.UserRankResult{}),
		},
		{
			// #3
			name:   "GetGuildRankResponse ⇄ domain.GuildRankResult",
			msg:    &rankingv1.GetGuildRankResponse{},
			goType: reflect.TypeOf(rankingdomain.GuildRankResult{}),
		},
		{
			// #4
			// 順位（rank / guild_rank）は worker の Redis 反映後にしか確定しないため、
			// domain の結果にも proto にも無い。両方に無いことがここで固定される。
			name:   "AddUserPointsResponse ⇄ domain.UserPointAddResult",
			msg:    &rankingv1.AddUserPointsResponse{},
			goType: reflect.TypeOf(rankingdomain.UserPointAddResult{}),
		},
		{
			// #5
			name:   "AddUserPointsRequest ⇄ usecase.AddUserPointsInput",
			msg:    &rankingv1.AddUserPointsRequest{},
			goType: reflect.TypeOf(rankingusecase.AddUserPointsInput{}),
		},
		{
			// #6
			// ユーザー版・ギルド版は同じ Go 型に対応するため、表の 1 行を 2 ケースへ展開する。
			name:   "GetUserRankingsRequest ⇄ usecase.GetRankingsInput",
			msg:    &rankingv1.GetUserRankingsRequest{},
			goType: reflect.TypeOf(rankingusecase.GetRankingsInput{}),
		},
		{
			name:   "GetGuildRankingsRequest ⇄ usecase.GetRankingsInput",
			msg:    &rankingv1.GetGuildRankingsRequest{},
			goType: reflect.TypeOf(rankingusecase.GetRankingsInput{}),
		},
		{
			// #7
			name:   "GetUserRankingsResponse ⇄ usecase.RankingsResult",
			msg:    &rankingv1.GetUserRankingsResponse{},
			goType: reflect.TypeOf(rankingusecase.RankingsResult{}),
		},
		{
			name:   "GetGuildRankingsResponse ⇄ usecase.RankingsResult",
			msg:    &rankingv1.GetGuildRankingsResponse{},
			goType: reflect.TypeOf(rankingusecase.RankingsResult{}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, goFieldNames(tt.goType), protoFieldNames(tt.msg),
				"proto と Go のフィールドが食い違っている。片方だけにフィールドを足していないか確認すること")
		})
	}
}

// protoFieldNames は pb メッセージのフィールド名（snake_case）をソートして返す。
func protoFieldNames(m proto.Message) []string {
	fields := m.ProtoReflect().Descriptor().Fields()
	names := make([]string, 0, fields.Len())
	for i := range fields.Len() {
		names = append(names, string(fields.Get(i).Name()))
	}
	sort.Strings(names)
	return names
}

// goFieldNames は Go 構造体のフィールド名を snake_case へ変換してソートして返す。
func goFieldNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())
	for i := range t.NumField() {
		names = append(names, toSnakeCase(t.Field(i).Name))
	}
	sort.Strings(names)
	return names
}

// toSnakeCase は Go の識別子を protobuf のフィールド名へ変換する。
//
// 連続する大文字（`ID` / `UserID`）を 1 語として扱う点が肝で、素朴な
// 「大文字の前に _ を入れる」実装では `user_i_d` になり、対応が取れているのに落ちる。
func toSnakeCase(name string) string {
	runes := []rune(name)
	var b strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 {
			prevIsLower := !unicode.IsUpper(runes[i-1])
			nextIsLower := i+1 < len(runes) && !unicode.IsUpper(runes[i+1])
			if prevIsLower || nextIsLower {
				b.WriteRune('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
