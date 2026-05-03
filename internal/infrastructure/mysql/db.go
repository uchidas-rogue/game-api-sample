package mysql

import (
	"database/sql"
	"fmt"

	// importで発火するinitだけ必要なので、MySQLドライバを匿名インポート。
	_ "github.com/go-sql-driver/mysql"
)

// Open は DSN から *sql.DB を開く。Ping は呼び出し側の責務。
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open mysql: %w", err)
	}
	return db, nil
}
