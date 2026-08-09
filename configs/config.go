// Package configs は環境変数からアプリケーション設定を読み込む。
package configs

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// 環境変数のキー名と既定値を定数として定義する（マジックナンバー禁止対応）。
const (
	envPort      = "PORT"
	defaultPort  = 8080
	envLogLevel  = "LOG_LEVEL"
	defaultLevel = slog.LevelInfo
	envMySQLDSN  = "MYSQL_DSN"
	// defaultMySQLDSN は docker-compose 既定値に揃えた接続文字列。
	defaultMySQLDSN = "game:game@tcp(127.0.0.1:3306)/game_db?parseTime=true&loc=Local"
	envRedisAddr    = "REDIS_ADDR"
	// defaultRedisAddr は docker-compose 既定値に揃えた接続先。
	defaultRedisAddr = "127.0.0.1:6379"

	envOutboxPollInterval = "OUTBOX_POLL_INTERVAL"
	// defaultOutboxPollInterval は通知取りこぼし時のフォールバック前提のため長めに設定。
	// 通常時は Redis Pub/Sub の通知駆動で即時処理される。
	defaultOutboxPollInterval = 10 * time.Minute
	envOutboxBatchSize        = "OUTBOX_BATCH_SIZE"
	defaultOutboxBatchSize    = 100
	envOutboxTickTimeout      = "OUTBOX_TICK_TIMEOUT"
	// defaultOutboxTickTimeout は1ティック（runOnce）の処理時間上限。
	// DB/Redis のブロッキングで worker のループがハングするのを防ぐ。
	defaultOutboxTickTimeout = 30 * time.Second

	envDBPingTimeout = "DB_PING_TIMEOUT"
	// defaultDBPingTimeout は起動時の疎通確認1回ぶんの上限。
	// DB 未到達を有限時間で確定エラーにし、ECS の起動失敗判定に乗せる（fail fast）。
	defaultDBPingTimeout = 5 * time.Second

	envDBMaxOpenConns = "DB_MAX_OPEN_CONNS"
	// defaultDBMaxOpenConns は同時に開くコネクション数の上限。
	// database/sql の既定は無制限のため、高負荷時に MySQL の max_connections(既定151)を
	// 超えて "Error 1040: Too many connections" を誘発する。これを防ぐため有限値で頭打ちにし、
	// 超過分は接続待ちキューに回す（エラーではなく待機に変える）。api/worker 等の合算で
	// max_connections 未満に収まるよう保守的に設定する。
	defaultDBMaxOpenConns = 25
	envDBMaxIdleConns     = "DB_MAX_IDLE_CONNS"
	// defaultDBMaxIdleConns はプールに保持するアイドルコネクション数。
	// 既定(2)のままだと高頻度に接続の開閉が発生しオーバーヘッドになるため、
	// MaxOpenConns と同値にして再利用性を高める。
	defaultDBMaxIdleConns = 25
	envDBConnMaxLifetime  = "DB_CONN_MAX_LIFETIME"
	// defaultDBConnMaxLifetime はコネクションの最大生存時間。
	// LB/Aurora フェイルオーバ後の古い接続の滞留を防ぐため有限にする。
	defaultDBConnMaxLifetime = 5 * time.Minute
	envDBConnMaxIdleTime     = "DB_CONN_MAX_IDLE_TIME"
	// defaultDBConnMaxIdleTime はアイドル接続を回収するまでの時間。
	defaultDBConnMaxIdleTime = 5 * time.Minute
)

// Config はアプリケーション全体の設定値を保持する。
type Config struct {
	Port               int
	LogLevel           slog.Level
	MySQLDSN           string
	RedisAddr          string
	OutboxPollInterval time.Duration
	OutboxBatchSize    int
	OutboxTickTimeout  time.Duration
	DBPingTimeout      time.Duration
	DBMaxOpenConns     int
	DBMaxIdleConns     int
	DBConnMaxLifetime  time.Duration
	DBConnMaxIdleTime  time.Duration
}

// Load は環境変数から設定値を読み込む。
// PORT未設定時は8080、LOG_LEVEL未設定時はinfoを使用する。
// LOG_LEVELに指定できる値: debug / info / warn / error（大文字小文字不問）
// OUTBOX_POLL_INTERVAL は time.ParseDuration 形式（例: 2s, 500ms）を受け付ける。
func Load() (*Config, error) {
	port := defaultPort
	if v := os.Getenv(envPort); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", envPort, err)
		}
		port = parsed
	}

	level := defaultLevel
	if v := os.Getenv(envLogLevel); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			return nil, fmt.Errorf("invalid %s %q (use debug/info/warn/error): %w", envLogLevel, v, err)
		}
	}

	dsn := defaultMySQLDSN
	if v := os.Getenv(envMySQLDSN); v != "" {
		dsn = v
	}

	redisAddr := defaultRedisAddr
	if v := os.Getenv(envRedisAddr); v != "" {
		redisAddr = v
	}

	pollInterval := defaultOutboxPollInterval
	if v := os.Getenv(envOutboxPollInterval); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", envOutboxPollInterval, v, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("invalid %s: must be positive", envOutboxPollInterval)
		}
		pollInterval = parsed
	}

	batchSize := defaultOutboxBatchSize
	if v := os.Getenv(envOutboxBatchSize); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", envOutboxBatchSize, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("invalid %s: must be positive", envOutboxBatchSize)
		}
		batchSize = parsed
	}

	tickTimeout := defaultOutboxTickTimeout
	if v := os.Getenv(envOutboxTickTimeout); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", envOutboxTickTimeout, v, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("invalid %s: must be positive", envOutboxTickTimeout)
		}
		tickTimeout = parsed
	}

	pingTimeout := defaultDBPingTimeout
	if v := os.Getenv(envDBPingTimeout); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", envDBPingTimeout, v, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("invalid %s: must be positive", envDBPingTimeout)
		}
		pingTimeout = parsed
	}

	maxOpenConns := defaultDBMaxOpenConns
	if v := os.Getenv(envDBMaxOpenConns); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", envDBMaxOpenConns, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("invalid %s: must be positive", envDBMaxOpenConns)
		}
		maxOpenConns = parsed
	}

	maxIdleConns := defaultDBMaxIdleConns
	if v := os.Getenv(envDBMaxIdleConns); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", envDBMaxIdleConns, err)
		}
		if parsed < 0 {
			return nil, fmt.Errorf("invalid %s: must be non-negative", envDBMaxIdleConns)
		}
		maxIdleConns = parsed
	}

	connMaxLifetime := defaultDBConnMaxLifetime
	if v := os.Getenv(envDBConnMaxLifetime); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", envDBConnMaxLifetime, v, err)
		}
		if parsed < 0 {
			return nil, fmt.Errorf("invalid %s: must be non-negative", envDBConnMaxLifetime)
		}
		connMaxLifetime = parsed
	}

	connMaxIdleTime := defaultDBConnMaxIdleTime
	if v := os.Getenv(envDBConnMaxIdleTime); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", envDBConnMaxIdleTime, v, err)
		}
		if parsed < 0 {
			return nil, fmt.Errorf("invalid %s: must be non-negative", envDBConnMaxIdleTime)
		}
		connMaxIdleTime = parsed
	}

	return &Config{
		Port:               port,
		LogLevel:           level,
		MySQLDSN:           dsn,
		RedisAddr:          redisAddr,
		OutboxPollInterval: pollInterval,
		OutboxBatchSize:    batchSize,
		OutboxTickTimeout:  tickTimeout,
		DBPingTimeout:      pingTimeout,
		DBMaxOpenConns:     maxOpenConns,
		DBMaxIdleConns:     maxIdleConns,
		DBConnMaxLifetime:  connMaxLifetime,
		DBConnMaxIdleTime:  connMaxIdleTime,
	}, nil
}
