// Package router_test は router パッケージの外部テストパッケージ。
package router_test

import (
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
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

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := slogtest.NewLogger(t, nil)

	h := router.Handlers{
		Health:  healthhandler.NewHandler(mock_health.NewMockUsecase(ctrl), logger),
		Gacha:   gachahandler.NewHandler(mock_gacha.NewMockUsecase(ctrl), logger),
		Ranking: rankinghandler.NewHandler(mock_ranking.NewMockUsecase(ctrl), logger),
	}

	e := echo.New()
	router.Register(e, h)
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
