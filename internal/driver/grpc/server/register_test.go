// Package server_test は gRPC サービス登録の外部テストパッケージ。
//
// テスト設計は docs/testing/grpc-ranking.md §7。
// 登録されたかどうかは grpc.Server の GetServiceInfo で確認する
// （HTTP 側が echo.Echo.Routes() を使うのと同じ役割）。
package server_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/server"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
)

// rankingServiceName は proto の package + service 名。
// 生成物の定数ではなくリテラルで書くのは、これがクライアントとの契約そのものであり、
// proto の package 名を変えたら「気づかず通る」のではなく落ちてほしいため。
const rankingServiceName = "game.ranking.v1.RankingService"

// TestRegister_MissingService は組み立て漏れが起動時に露見することを検証する。
//
// 検査を外すと、型付き nil の *Handler が RegisterRankingServiceServer へ渡り、
// 生成コードの埋め込み検査で nil 参照の panic になる（このテストで実測済み）。
// スタックトレースからは組み立て漏れだと読み取れず、生成コードの形が変われば
// panic が最初の RPC まで遅延する方へ倒れる。欠けたフィールド名を挙げるエラーで
// 起動時に止めることだけが、運用側から原因を見える形にする手段になる。
func TestRegister_MissingService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// drop は組み立て済みの Services から1つを nil に落とす。
		drop        func(*server.Services)
		wantMissing string
	}{
		{
			// #1 S1→S2→SE1
			name:        "Ranking が nil",
			drop:        func(s *server.Services) { s.Ranking = nil },
			wantMissing: "Ranking",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := newServices(t)
			tt.drop(&svc)

			s := grpc.NewServer()
			t.Cleanup(s.Stop)

			err := server.Register(s, svc)

			require.Error(t, err)
			assert.ErrorIs(t, err, server.ErrMissingService)
			assert.Contains(t, err.Error(), tt.wantMissing, "欠けたフィールド名がエラーに含まれること")
			assert.Empty(t, s.GetServiceInfo(), "検査に落ちたらサービスを1つも登録しないこと")
		})
	}
}

// TestRegister_AllServices は全ハンドラが揃っているときに RPC が登録されることを検証する。
func TestRegister_AllServices(t *testing.T) {
	// #2 S1→S2→S3→SZ
	t.Parallel()

	s := grpc.NewServer()
	t.Cleanup(s.Stop)

	require.NoError(t, server.Register(s, newServices(t)))

	info := s.GetServiceInfo()
	svcInfo, ok := info[rankingServiceName]
	require.True(t, ok, "%s が登録されていない: %v", rankingServiceName, info)

	registered := make(map[string]bool, len(svcInfo.Methods))
	for _, m := range svcInfo.Methods {
		registered[m.Name] = true
	}

	// proto の rpc 定義（proto/game/ranking/v1/ranking.proto）と対応する。
	// WatchUserRankings は本ハンドラでは未実装だが、UnimplementedRankingServiceServer の
	// 埋め込みにより登録はされる（呼ぶと codes.Unimplemented）。
	expected := []string{
		"GetUserRankings",
		"GetGuildRankings",
		"GetUserRank",
		"GetGuildRank",
		"AddUserPoints",
		"WatchUserRankings",
	}
	for _, name := range expected {
		assert.Contains(t, registered, name, "期待する RPC が登録されていない: %s", name)
	}
}

// newServices は全フィールドが埋まった Services を組み立てる。
func newServices(t *testing.T) server.Services {
	t.Helper()

	ctrl := gomock.NewController(t)
	logger := slogtest.NewLogger(t, nil)

	return server.Services{
		Ranking: rankinghandler.NewHandler(mockranking.NewMockUsecase(ctrl), logger),
	}
}
