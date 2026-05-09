package repository

import (
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// NewQuerierFactoryForTest は newQuerierFactory をテストパッケージに公開する。
// 多 DB 対応時に効く「unexpected tx type」境界チェックを検証するため。
func NewQuerierFactoryForTest(db sqlc.DBTX) func(tx shared.Tx) (sqlc.Querier, error) {
	return newQuerierFactory(db)
}

// NewGachaRepositoryWithQuerier はテスト専用のコンストラクタ。
// querier ファクトリを直接差し替えることで、sqlc.Querier のモックを注入できる。
// tx パラメータが渡されても、ファクトリ側でモック Querier を返すため tx の中身は参照されない。
func NewGachaRepositoryWithQuerier(q sqlc.Querier) *GachaRepository {
	return &GachaRepository{
		querier: func(_ shared.Tx) (sqlc.Querier, error) {
			return q, nil
		},
	}
}

// NewRankingRepositoryWithQuerier はテスト専用のコンストラクタ。
func NewRankingRepositoryWithQuerier(q sqlc.Querier) *RankingRepository {
	return &RankingRepository{
		querier: func(_ shared.Tx) (sqlc.Querier, error) {
			return q, nil
		},
	}
}

// NewOutboxRepositoryWithQuerier はテスト専用のコンストラクタ。
func NewOutboxRepositoryWithQuerier(q sqlc.Querier) *OutboxRepository {
	return &OutboxRepository{
		querier: func(_ shared.Tx) (sqlc.Querier, error) {
			return q, nil
		},
	}
}
