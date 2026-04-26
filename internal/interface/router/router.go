// Package router はEchoのルーティング定義を集約する。
package router

import (
	"github.com/labstack/echo/v4"

	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/interface/handler/health"
)

// Handlers はルーティングに必要な全ハンドラを束ねる構造体。
// 機能追加時はこの構造体にハンドラを追加していく。
type Handlers struct {
	Health *healthhandler.Handler
}

// Register はEchoインスタンスにルーティングを登録する。
func Register(e *echo.Echo, h Handlers) {
	// ヘルスチェック
	e.GET("/healthz", h.Health.Check)
}
