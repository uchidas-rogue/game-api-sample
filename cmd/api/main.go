// Package main はゲームAPIのエントリポイント。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/uchidas-rogue/game-api-sample/configs"
	"github.com/uchidas-rogue/game-api-sample/internal/di"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/logger"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/server"
	"github.com/uchidas-rogue/game-api-sample/internal/interface/router"
)

func main() {
	// 設定読み込み（LOG_LEVELもここで解決する）
	cfg, err := configs.Load()
	if err != nil {
		slog.Error("failed to load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// ロガー初期化（cfg.LogLevelを適用）
	log := logger.New(cfg.LogLevel)
	slog.SetDefault(log)

	// SIGINT/SIGTERMでシャットダウン用のctxをキャンセル
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Echoインスタンス生成 + DI解決 + ルーティング登録
	e := server.New(log)
	handlers := di.BuildHandlers()
	router.Register(e, handlers)

	// サーバ起動（ctxキャンセルでグレースフルシャットダウン）
	if err := server.Run(ctx, e, cfg.Port, log); err != nil {
		log.Error("server terminated with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
	log.Info("server stopped gracefully")
}
