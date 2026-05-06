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
	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
	infraRedis "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/redis"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/server"
	"github.com/uchidas-rogue/game-api-sample/internal/driver/http/router"
)

func main() {
	// 設定読み込み（LOG_LEVELもここで解決する）
	cfg, err := configs.Load()
	if err != nil {
		slog.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	// ロガー初期化（cfg.LogLevelを適用）
	log := logger.New(cfg.LogLevel)
	slog.SetDefault(log)

	// SIGINT/SIGTERMでシャットダウン用のctxをキャンセル
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// MySQL接続を確立（DSN は configs から）
	db, err := infraMysql.Open(cfg.MySQLDSN)
	if err != nil {
		log.Error("failed to open mysql", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	// Redis接続を確立
	redisClient, err := infraRedis.NewClient(cfg.RedisAddr)
	if err != nil {
		log.Error("failed to connect redis", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()

	// Echoインスタンス生成 + DI解決 + ルーティング登録
	e := server.New(log)
	container := di.Build(db, redisClient, log)
	router.Register(e, container.Handlers)

	// サーバ起動（ctxキャンセルでグレースフルシャットダウン）
	if err := server.Run(ctx, e, cfg.Port, log); err != nil {
		log.Error("server terminated with error", slog.Any("error", err))
		os.Exit(1)
	}
	log.Info("server stopped gracefully")
}
