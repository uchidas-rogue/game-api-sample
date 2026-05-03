// Package di はアプリケーションの依存解決を集約する（コンポジションルート）。
package di

import (
	"database/sql"
	"log/slog"

	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/repository"
	gachahandler "github.com/uchidas-rogue/game-api-sample/internal/interface/handler/gacha"
	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/interface/handler/health"
	"github.com/uchidas-rogue/game-api-sample/internal/interface/router"
	gachausecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
	healthusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/health"
)

// インフラ層の実装が usecase 層インターフェースを満たすことをコンパイル時に検証する。
// クリーンアーキテクチャの依存方向（infrastructure は usecase を import しない）を守るため、両方を import している本コンポジションルートで検証を行う。
// コンポジションルートが期待する全インターフェースをここで確認するようにする。
var (
	_ gachausecase.Transactor = (*infraMysql.Transactor)(nil)
	_ gachausecase.Repository = (*repository.GachaRepository)(nil)
)

// Container はアプリケーション全体のコンポーネントを保持する。
type Container struct {
	Handlers router.Handlers
	GachaUC  gachausecase.Usecase
}

// Build は DB と logger を受け取り、各層の依存を組み立てる。
// 機能追加時はここで各層のコンストラクタを呼び出して注入する。
func Build(db *sql.DB, logger *slog.Logger) Container {
	healthUC := healthusecase.NewUsecase()
	healthH := healthhandler.NewHandler(healthUC)

	tx := infraMysql.NewTransactor(db, logger)
	gachaRepo := repository.NewGachaRepository(db)
	gachaUC := gachausecase.NewUsecase(tx, gachaRepo, nil, logger)
	gachaH := gachahandler.NewHandler(gachaUC, logger)

	return Container{
		Handlers: router.Handlers{
			Health: healthH,
			Gacha:  gachaH,
		},
		GachaUC: gachaUC,
	}
}
