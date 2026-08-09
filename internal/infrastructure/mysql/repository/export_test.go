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

// NewGachaRepositoryWithExecer はテスト専用のコンストラクタ。
// execer ファクトリを本番実装（newExecerFactory）で組み立てつつ、db に sqlmock 等の
// sqlc.DBTX 実装を注入できるようにする。querier は未使用（nil のまま）のため、
// bulk 系（execer 経由）メソッドの検証専用。tx=nil で呼び出すことで execer が db を直接使う。
func NewGachaRepositoryWithExecer(db sqlc.DBTX) *GachaRepository {
	return &GachaRepository{
		execer: newExecerFactory(db),
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

// NewRankingRepositoryWithExecer はテスト専用のコンストラクタ。
// execer ファクトリを本番実装（newExecerFactory）で組み立てつつ、db に sqlmock 等の
// sqlc.DBTX 実装を注入できるようにする。querier は未使用（nil のまま）のため、
// bulk 系（execer 経由）メソッドの検証専用。tx=nil で呼び出すことで execer が db を直接使う。
func NewRankingRepositoryWithExecer(db sqlc.DBTX) *RankingRepository {
	return &RankingRepository{
		execer: newExecerFactory(db),
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
