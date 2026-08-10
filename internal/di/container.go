// Package di はアプリケーションの依存解決を集約する（コンポジションルート）。
package di

import (
	"database/sql"
	"log/slog"

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
)

// Container はアプリケーション全体のコンポーネントを保持する。
//
// 現状は HTTP ハンドラ束のみ。usecase を直接参照したい呼び出し元（別 driver 等）が
// 出てきたら、そのときにフィールドを足す。使われないまま持たせると、
// 依存の実際の広がりが Container の見た目と食い違う。
type Container struct {
	Handlers router.Handlers
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

	return Container{
		Handlers: router.Handlers{
			Health:  healthH,
			Gacha:   gachaH,
			Ranking: rankingH,
		},
	}
}
