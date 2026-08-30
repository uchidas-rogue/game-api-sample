// Package main は gRPC サーバのエントリポイント。
// Unity クライアント向けに ranking の参照・加算（unary）と、
// ランキング更新の push（server streaming）を提供する常駐プロセス。
//
// HTTP（cmd/api）とは別プロセスに分けてある。同じ usecase を共有しつつ、
// 配信形式ごとに独立してスケールできるようにするため。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/uchidas-rogue/game-api-sample/configs"
	"github.com/uchidas-rogue/game-api-sample/internal/di"
	grpcserver "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/server"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/logger"
	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
	infraRedis "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/redis"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/server"
)

func main() {
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

	redisClient, err := infraRedis.NewClient(ctx, cfg.RedisAddr)
	if err != nil {
		log.Error("failed to connect redis", slog.Any("error", err))
		os.Exit(1)
	}
	defer func() { _ = redisClient.Close() }()

	container := di.Build(db, redisClient, log)

	// 配信ハブとサーバで停止のトリガを分けている。
	//
	// watchCtx: ストリームの配信を止める。GracefulStop はストリームが終わるまで
	//   ブロックするため、これを先に止めないとシャットダウンが完了しない。
	// srvCtx:   gRPC サーバのシャットダウンを始める。シグナルに加えて、
	//   ハブが異常終了したときにもここから落とす。
	watchCtx, stopWatcher := context.WithCancel(ctx)
	defer stopWatcher()
	srvCtx, stopServer := context.WithCancel(ctx)
	defer stopServer()

	go func() {
		// ctx キャンセルによる正常終了では nil が返る。
		// それ以外（購読断・購読開始の失敗）はランキングの push が永久に止まった状態なので、
		// 縮退運転せずプロセスごと落としてオーケストレータの再起動に任せる。
		if err := container.RankingWatcher.Run(watchCtx); err != nil {
			log.Error("ranking watcher stopped unexpectedly, shutting down", slog.Any("error", err))
			stopServer()
		}
	}()

	srv := server.NewGRPC(log)
	if err := grpcserver.Register(srv, container.GRPCServices); err != nil {
		log.Error("failed to register grpc services", slog.Any("error", err))
		os.Exit(1)
	}

	// stopWatcher を onShutdown に渡すことで、GracefulStop の前に全ストリームが閉じる。
	if err := server.RunGRPC(srvCtx, srv, cfg.GRPCPort, log, stopWatcher); err != nil {
		log.Error("grpc server terminated with error", slog.Any("error", err))
		os.Exit(1)
	}
	log.Info("grpc server stopped gracefully")
}
