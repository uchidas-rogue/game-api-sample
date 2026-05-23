// Package main はバッチ処理のエントリポイント。
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/uchidas-rogue/game-api-sample/configs"
	"github.com/uchidas-rogue/game-api-sample/internal/driver/batch"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/logger"
	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/repository"
	infraRedis "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/redis"
)

func main() {
	syncRankings := flag.Bool("sync-rankings", false, "sync rankings from DB to Redis")
	flag.Parse()

	if !*syncRankings {
		slog.Error("no batch specified. use -sync-rankings")
		os.Exit(1)
	}

	cfg, err := configs.Load()
	if err != nil {
		slog.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := infraMysql.Open(cfg.MySQLDSN)
	if err != nil {
		log.Error("failed to open mysql", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() { _ = db.Close() }()

	pingCtx, cancelPing := context.WithTimeout(ctx, cfg.DBPingTimeout)
	defer cancelPing()
	if err := infraMysql.Ping(pingCtx, db); err != nil {
		log.Error("failed to ping mysql", slog.Any("error", err))
		os.Exit(1)
	}

	redisClient, err := infraRedis.NewClient(cfg.RedisAddr)
	if err != nil {
		log.Error("failed to connect redis", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()

	rankingRepo := repository.NewRankingRepository(db)
	outboxRepo := repository.NewOutboxRepository(db)
	rankingStore := infraRedis.NewRankingStore(redisClient.Raw())
	transactor := infraMysql.NewTransactor(db, log)
	syncer := batch.NewRankingSyncer(rankingRepo, outboxRepo, rankingStore, transactor, log)

	log.Info("starting ranking sync batch")
	if err := syncer.SyncAll(ctx); err != nil {
		log.Error("ranking sync failed", slog.Any("error", err))
		os.Exit(1)
	}
	log.Info("ranking sync completed successfully")
}
