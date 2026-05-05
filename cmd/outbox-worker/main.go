// Package main は outbox-worker のエントリポイント。
// outbox_events を購読して Redis 反映等の副作用を実行する常駐プロセス。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/uchidas-rogue/game-api-sample/configs"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/logger"
	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/repository"
	infraRedis "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/redis"
	workeroutbox "github.com/uchidas-rogue/game-api-sample/internal/driver/worker/outbox"
)

func main() {
	cfg, err := configs.Load()
	if err != nil {
		slog.Error("failed to load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := infraMysql.Open(cfg.MySQLDSN)
	if err != nil {
		log.Error("failed to open mysql", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	redisClient, err := infraRedis.NewClient(cfg.RedisAddr)
	if err != nil {
		log.Error("failed to connect redis", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()

	tx := infraMysql.NewTransactor(db, log)
	outboxRepo := repository.NewOutboxRepository(db)
	rankingStore := infraRedis.NewRankingStore(redisClient.Raw())
	outboxSubscriber := infraRedis.NewOutboxSubscriber(redisClient.Raw())

	w := workeroutbox.New(workeroutbox.Config{
		Repo:         outboxRepo,
		RankingStore: rankingStore,
		Tx:           tx,
		Subscriber:   outboxSubscriber,
		Logger:       log,
		PollInterval: cfg.OutboxPollInterval,
		BatchSize:    cfg.OutboxBatchSize,
	})

	if err := w.Run(ctx); err != nil {
		log.Error("outbox worker terminated with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
	log.Info("outbox worker stopped gracefully")
}
