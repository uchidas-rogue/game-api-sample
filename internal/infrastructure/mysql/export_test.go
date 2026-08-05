package mysql

import (
	"database/sql"

	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// ToSQLTxOptionsForTest は非公開の toSQLTxOptions を外部テストへ公開する seam。
// 分離レベルの取り違えは「動くが遅い」形でしか現れず、go-sqlmock では BeginTx に
// 渡る TxOptions を検証できないため、変換ロジック自体を直接テストする。
func ToSQLTxOptionsForTest(o shared.TxOptions) *sql.TxOptions {
	return toSQLTxOptions(o)
}
