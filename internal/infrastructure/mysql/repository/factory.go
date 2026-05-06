package repository

import (
	"fmt"

	infraMysql "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// querierFactory は shared.Tx から sqlc.Querier を返すファクトリ。
// 本番では Queries / Queries.WithTx を返し、テストではモック Querier を返すために差し替え可能とする。
type querierFactory func(tx shared.Tx) (sqlc.Querier, error)

// newQuerierFactory は本番用の querierFactory を生成する。
func newQuerierFactory(db sqlc.DBTX) querierFactory {
	base := sqlc.New(db)
	return func(tx shared.Tx) (sqlc.Querier, error) {
		if tx == nil {
			return base, nil
		}
		sqlTx, ok := tx.(*infraMysql.SQLTx)
		if !ok {
			return nil, fmt.Errorf("unexpected tx type: %T", tx)
		}
		return base.WithTx(sqlTx.Raw()), nil
	}
}
