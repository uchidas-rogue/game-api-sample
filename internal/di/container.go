// Package di はアプリケーションの依存解決を集約する（コンポジションルート）。
package di

import (
	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/interface/handler/health"
	"github.com/uchidas-rogue/game-api-sample/internal/interface/router"
	healthusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/health"
)

// BuildHandlers はrouter.Registerに渡すHandlersを組み立てる。
// 機能追加時はここで各層のコンストラクタを呼び出してハンドラを注入する。
func BuildHandlers() router.Handlers {
	healthUC := healthusecase.NewUsecase()
	healthH := healthhandler.NewHandler(healthUC)

	return router.Handlers{
		Health: healthH,
	}
}
