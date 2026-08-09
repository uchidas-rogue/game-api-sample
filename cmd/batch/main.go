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
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/seed"
)

func main() {
	syncRankings := flag.Bool("sync-rankings", false, "sync rankings from DB to Redis")
	doSeed := flag.Bool("seed", false, "seed dev/load-test data into MySQL")
	seedUsers := flag.Int("users", seed.DefaultUsers, "number of users to seed (with -seed)")
	seedGuilds := flag.Int("guilds", seed.DefaultGuilds, "number of guilds to seed (with -seed)")
	flag.Parse()

	if !*syncRankings && !*doSeed {
		slog.Error("no batch specified. use -seed or -sync-rankings")
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
	infraMysql.ConfigurePool(db, infraMysql.PoolConfig{
		MaxOpenConns:    cfg.DBMaxOpenConns,
		MaxIdleConns:    cfg.DBMaxIdleConns,
		ConnMaxLifetime: cfg.DBConnMaxLifetime,
		ConnMaxIdleTime: cfg.DBConnMaxIdleTime,
	})

	pingCtx, cancelPing := context.WithTimeout(ctx, cfg.DBPingTimeout)
	defer cancelPing()
	if err := infraMysql.Ping(pingCtx, db); err != nil {
		log.Error("failed to ping mysql", slog.Any("error", err))
		os.Exit(1)
	}

	if *doSeed {
		seeder := seed.NewSeeder(db, log)
		if err := seeder.Seed(ctx, seed.Params{Users: *seedUsers, Guilds: *seedGuilds}); err != nil {
			log.Error("seeding failed", slog.Any("error", err))
			os.Exit(1)
		}
		return
	}

	// *syncRankings
	redisClient, err := infraRedis.NewClient(cfg.RedisAddr)
	if err != nil {
		log.Error("failed to connect redis", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()

	rankingRepo := repository.NewRankingRepository(db)
	rankingStore := infraRedis.NewRankingStore(redisClient.Raw())
	transactor := infraMysql.NewTransactor(db, log)
	syncer := batch.NewRankingSyncer(rankingRepo, rankingStore, transactor, log)

	log.Info("starting ranking sync batch")
	if err := syncer.SyncAll(ctx); err != nil {
		log.Error("ranking sync failed", slog.Any("error", err))
		os.Exit(1)
	}
	log.Info("ranking sync completed successfully")
}
