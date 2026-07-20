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

// execerFactory は shared.Tx から生の sqlc.DBTX（ExecContext）を返すファクトリ。
// sqlc が生成できない可変行数の複数行 INSERT（bulk upsert 等）を発行するために用いる。
type execerFactory func(tx shared.Tx) (sqlc.DBTX, error)

// newExecerFactory は本番用の execerFactory を生成する。
// tx が nil の場合は DB 接続を直接返し、tx 指定時はトランザクションの *sql.Tx を返す。
func newExecerFactory(db sqlc.DBTX) execerFactory {
	return func(tx shared.Tx) (sqlc.DBTX, error) {
		if tx == nil {
			return db, nil
		}
		sqlTx, ok := tx.(*infraMysql.SQLTx)
		if !ok {
			return nil, fmt.Errorf("unexpected tx type: %T", tx)
		}
		return sqlTx.Raw(), nil
	}
}
