// Package di はアプリケーションの依存解決を集約する（コンポジションルート）。
package di

import (
	"database/sql"
	"log/slog"

	grpcranking "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/ranking"
	grpcserver "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/server"
	gachahandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/gacha"
	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/health"
	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/driver/http/router"
	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/repository"
	infraRedis "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/redis"
	gachausecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
	healthusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/health"
	outboxusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// インフラ層の実装が usecase 層インターフェースを満たすことをコンパイル時に検証する。
// クリーンアーキテクチャの依存方向（infrastructure は usecase を import しない）を守るため、両方を import している本コンポジションルートで検証を行う。
// コンポジションルートが期待する全インターフェースをここで確認するようにする。
var (
	_ shared.Transactor           = (*infraMysql.Transactor)(nil)
	_ gachausecase.Repository     = (*repository.GachaRepository)(nil)
	_ rankingusecase.Repository   = (*repository.RankingRepository)(nil)
	_ rankingusecase.RankingStore = (*infraRedis.RankingStore)(nil)
	_ outboxusecase.Repository    = (*repository.OutboxRepository)(nil)
	_ outboxusecase.Notifier      = (*infraRedis.OutboxNotifier)(nil)
	_ outboxusecase.Subscriber    = (*infraRedis.OutboxSubscriber)(nil)

	// ランキング更新通知。Subscriber は本コンテナ（gRPC の配信ハブ）が、
	// Notifier は cmd/outbox-worker が使う。使う場所は分かれているが、
	// インフラ実装が usecase の interface を満たすことの検証はここへ集約する。
	_ rankingusecase.RankingUpdateSubscriber = (*infraRedis.RankingUpdateSubscriber)(nil)
	_ rankingusecase.RankingUpdateNotifier   = (*infraRedis.RankingUpdateNotifier)(nil)
)

// Container はアプリケーション全体のコンポーネントを保持する。
//
// 配信形式ごとに束を持つ。cmd/api は Handlers を、cmd/grpc は GRPCServices と
// RankingWatcher を使う。どちらの経路も同じ usecase インスタンスを共有しており、
// 「1 つのユースケースを複数の delivery が配る」という層構成がここで確定する。
//
// 使われないまま持たせると依存の実際の広がりが見た目と食い違うので、
// 呼び出し元が現れてからフィールドを足す。
type Container struct {
	Handlers router.Handlers

	// GRPCServices は gRPC delivery が登録するサービス束。
	GRPCServices grpcserver.Services

	// RankingWatcher はランキング更新の配信ハブ。cmd/grpc が Run を常駐させる。
	//
	// ここだけ interface ではなく具象型を返している。Run はコンポジションルート専用の
	// ライフサイクルメソッドで、これを Watcher interface に含めると driver 層と
	// 全モックが使わないメソッドを持ち回ることになるため。
	RankingWatcher *rankingusecase.RankingWatcher
}

// Build は DB, Redis, logger を受け取り、各層の依存を組み立てる。
// 機能追加時はここで各層のコンストラクタを呼び出して注入する。
func Build(db *sql.DB, redisClient *infraRedis.Client, logger *slog.Logger) Container {
	healthUC := healthusecase.NewUsecase()
	healthH := healthhandler.NewHandler(healthUC, logger)

	tx := infraMysql.NewTransactor(db, logger)

	// ガチャ
	gachaRepo := repository.NewGachaRepository(db)
	gachaUC := gachausecase.NewUsecase(tx, gachaRepo, nil, logger)
	gachaH := gachahandler.NewHandler(gachaUC, logger)

	// ランキング（outbox 経由で Redis 反映を非同期化）
	rankingRepo := repository.NewRankingRepository(db)
	rankingStore := infraRedis.NewRankingStore(redisClient.Raw())
	outboxRepo := repository.NewOutboxRepository(db)
	outboxNotifier := infraRedis.NewOutboxNotifier(redisClient.Raw())
	rankingUC := rankingusecase.NewUsecase(tx, rankingRepo, rankingStore, outboxRepo, outboxNotifier, logger)
	rankingH := rankinghandler.NewHandler(rankingUC, logger)

	// ランキング更新の配信（gRPC の server streaming 用）。
	// Redis の購読は接続ごとではなくハブで 1 本にまとめる。
	rankingUpdateSubscriber := infraRedis.NewRankingUpdateSubscriber(redisClient.Raw())
	rankingWatcher := rankingusecase.NewRankingWatcher(rankingUC, rankingUpdateSubscriber, logger)

	// gRPC ハンドラは HTTP ハンドラと同じ rankingUC を共有する。
	rankingGRPCH := grpcranking.NewHandler(rankingUC, rankingWatcher, logger)

	return Container{
		Handlers: router.Handlers{
			Health:  healthH,
			Gacha:   gachaH,
			Ranking: rankingH,
		},
		GRPCServices: grpcserver.Services{
			Ranking: rankingGRPCH,
		},
		RankingWatcher: rankingWatcher,
	}
}
