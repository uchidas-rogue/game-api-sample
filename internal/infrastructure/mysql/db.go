package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

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

// PoolConfig は *sql.DB のコネクションプール設定。
// configs から値を受け取り ConfigurePool で適用する（mysql 層は configs に依存しない）。
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// ConfigurePool は *sql.DB にプール設定を適用する。
// database/sql の既定は MaxOpenConns 無制限・MaxIdleConns 2 のため、
// 高負荷時にコネクションが青天井に増えて MySQL の max_connections を超過する。
// 上限を張ることで超過分を接続待ちに変え、"Too many connections" を防ぐ。
func ConfigurePool(db *sql.DB, p PoolConfig) {
	db.SetMaxOpenConns(p.MaxOpenConns)
	db.SetMaxIdleConns(p.MaxIdleConns)
	db.SetConnMaxLifetime(p.ConnMaxLifetime)
	db.SetConnMaxIdleTime(p.ConnMaxIdleTime)
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
