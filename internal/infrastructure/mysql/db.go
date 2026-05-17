package mysql

import (
	"context"
	"database/sql"
	"fmt"

	// importで発火するinitだけ必要なので、MySQLドライバを匿名インポート。
	_ "github.com/go-sql-driver/mysql"
)

// Open は DSN から *sql.DB を開く。
// sql.Open は遅延接続のため、ここでは I/O は発生しない。
// 起動時の疎通確認は Ping の責務。
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open mysql: %w", err)
	}
	return db, nil
}

// Ping は DB への疎通を確認する。
// 起動時に DB 未到達を検知して ECS の起動失敗判定に乗せるため、
// 呼び出し側は deadline 付きの ctx を渡すこと。
func Ping(ctx context.Context, db *sql.DB) error {
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("failed to ping mysql: %w", err)
	}
	return nil
}
