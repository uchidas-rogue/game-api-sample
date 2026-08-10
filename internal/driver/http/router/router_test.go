// Package router_test は router パッケージの外部テストパッケージ。
package router_test

import (
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	gachahandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/gacha"
	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/health"
	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/driver/http/router"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mock_gacha "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha/mock"
	mock_health "github.com/uchidas-rogue/game-api-sample/internal/usecase/health/mock"
	mock_ranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
)

// routeKey はルート検索に使うキー。
type routeKey struct {
	method string
	path   string
}

// collectRoutes は echo.Echo.Routes() から method+path のセットを返す。
func collectRoutes(e *echo.Echo) map[routeKey]struct{} {
	m := make(map[routeKey]struct{})
	for _, r := range e.Routes() {
		m[routeKey{method: r.Method, path: r.Path}] = struct{}{}
	}
	return m
}

func TestRegister_AllHandlers(t *testing.T) {
	t.Parallel()

	h := newHandlers(t)

	e := echo.New()
	require.NoError(t, router.Register(e, h))
	routes := collectRoutes(e)

	expected := []routeKey{
		{method: "GET", path: "/healthz"},
		{method: "POST", path: "/users/:userID/gacha/multi"},
		{method: "GET", path: "/rankings/guilds"},
		{method: "GET", path: "/guilds/:guildID/ranking"},
		{method: "POST", path: "/users/:userID/points"},
		{method: "GET", path: "/rankings/users"},
		{method: "GET", path: "/users/:userID/ranking"},
	}

	for _, rk := range expected {
		assert.Contains(t, routes, rk, "期待ルートが登録されていない: %s %s", rk.method, rk.path)
	}
}

// TestRegister_MissingHandler は組み立て漏れが起動時に露見することを検証する。
//
// nil のまま登録しても echo への登録自体は成功してしまう（`h.Gacha.Multi` は
// メソッド値の生成で、nil レシーバをデリファレンスしない）。panic は最初のリクエストまで
// 遅延し middleware.Recover() が 500 に丸めるため、明示的な検査が唯一の防波堤になる。
// フィールドごとにケースを分けているのは、検査から1つ漏れても落ちるようにするため。
func TestRegister_MissingHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// drop は組み立て済みの Handlers から1つを nil に落とす。
		drop        func(*router.Handlers)
		wantMissing string
	}{
		{
			name:        "Health が nil",
			drop:        func(h *router.Handlers) { h.Health = nil },
			wantMissing: "Health",
		},
		{
			name:        "Gacha が nil",
			drop:        func(h *router.Handlers) { h.Gacha = nil },
			wantMissing: "Gacha",
		},
		{
			name:        "Ranking が nil",
			drop:        func(h *router.Handlers) { h.Ranking = nil },
			wantMissing: "Ranking",
		},
		{
			name:        "全て nil: 欠けているものを全て列挙する",
			drop:        func(h *router.Handlers) { *h = router.Handlers{} },
			wantMissing: "Health, Gacha, Ranking",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newHandlers(t)
			tt.drop(&h)

			e := echo.New()
			err := router.Register(e, h)

			require.Error(t, err)
			assert.ErrorIs(t, err, router.ErrMissingHandler)
			assert.Contains(t, err.Error(), tt.wantMissing, "欠けたフィールド名がエラーに含まれること")
			assert.Empty(t, collectRoutes(e), "検査に落ちたらルートを1つも登録しないこと")
		})
	}
}

// newHandlers は全フィールドが埋まった Handlers を組み立てる。
func newHandlers(t *testing.T) router.Handlers {
	t.Helper()

	ctrl := gomock.NewController(t)
	logger := slogtest.NewLogger(t, nil)

	return router.Handlers{
		Health:  healthhandler.NewHandler(mock_health.NewMockUsecase(ctrl), logger),
		Gacha:   gachahandler.NewHandler(mock_gacha.NewMockUsecase(ctrl), logger),
		Ranking: rankinghandler.NewHandler(mock_ranking.NewMockUsecase(ctrl), logger),
	}
}
