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
	// defaultOutboxBatchSize は1トランザクションでまとめて適用するイベント数。
	// worker はバッチ単位でコミットするため、この値がそのまま COMMIT(fsync) の削減率になる。
	// 大きいほどスループットは上がるが、1トランザクションのロック保持時間と
	// バッチ失敗時のフォールバック（イベント単位での再処理）の範囲も広がる。
	defaultOutboxBatchSize = 500
	envOutboxConcurrency   = "OUTBOX_CONCURRENCY"
	// defaultOutboxConcurrency はイベント単位経路（バッチ失敗時のフォールバック）で
	// 並列処理する goroutine 数。通常時のバッチ経路は単一トランザクションのため影響しない。
	// 並列度ぶんの同時トランザクションを張るため、DBMaxOpenConns 以下である必要がある
	// （Load() で検証する）。
	defaultOutboxConcurrency = 8
	envOutboxRetention       = "OUTBOX_RETENTION"
	// defaultOutboxRetention は処理済み outbox イベントを保持する期間。
	// これを過ぎた行は GC バッチ（cmd/batch -gc-outbox）が削除する。
	// 障害調査で数日ぶんの処理済み履歴を辿れれば足りる一方、負荷試験規模
	// （実測 約400 events/sec）では1日あたり約3,400万行が積み上がるため、
	// 長くしすぎるとテーブルが肥大する。
	defaultOutboxRetention = 72 * time.Hour
	// minOutboxRetention は保持期間の下限。
	// GC の SQL は INTERVAL ? SECOND で比較するため、repository が保持期間を秒へ落とす際に
	// 1秒未満はゼロ方向に切り捨てられる。`500ms` を許すと INTERVAL 0 SECOND となり、
	// 経過時間に関係なく処理済みイベントを全削除してしまう（復旧不能）。
	minOutboxRetention   = time.Second
	envOutboxTickTimeout = "OUTBOX_TICK_TIMEOUT"
	// defaultOutboxTickTimeout は1ティック（runOnce）の処理時間上限。
	// DB/Redis のブロッキングで worker のループがハングするのを防ぐ。
	defaultOutboxTickTimeout = 30 * time.Second
	envOutboxMaxRetry        = "OUTBOX_MAX_RETRY"
	// defaultOutboxMaxRetry は1イベントの失敗を許容する回数。これに達したイベントは
	// worker の取得対象から外れる（DLQ 相当。docs/testing/outbox-worker.md §0-4）。
	// retry_count は失敗の種類を区別しないため、MySQL/Redis の障害が続くと健全なイベントも
	// 回数を消費する。通知駆動のドレインは高頻度で回るので、消費は速い。
	// 打ち切られたイベントの復帰は手動 UPDATE の運用対応になるため、既定は大きめに取る。
	defaultOutboxMaxRetry = 10

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
	OutboxConcurrency  int
	OutboxTickTimeout  time.Duration
	OutboxRetention    time.Duration
	OutboxMaxRetry     int
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

	concurrency := defaultOutboxConcurrency
	if v := os.Getenv(envOutboxConcurrency); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", envOutboxConcurrency, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("invalid %s: must be positive", envOutboxConcurrency)
		}
		concurrency = parsed
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

	maxRetry := defaultOutboxMaxRetry
	if v := os.Getenv(envOutboxMaxRetry); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", envOutboxMaxRetry, err)
		}
		// 0 を「無制限」にはしない。SQL 側に分岐が増えて index の使われ方が読みにくくなるうえ、
		// retry_count < 0 は全件除外になり worker が沈黙する事故と区別できないため。
		if parsed <= 0 {
			return nil, fmt.Errorf("invalid %s: must be positive", envOutboxMaxRetry)
		}
		maxRetry = parsed
	}

	retention := defaultOutboxRetention
	if v := os.Getenv(envOutboxRetention); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", envOutboxRetention, v, err)
		}
		if parsed < minOutboxRetention {
			return nil, fmt.Errorf("invalid %s: must be >= %s", envOutboxRetention, minOutboxRetention)
		}
		retention = parsed
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

	// 並列度は同時に張るトランザクション数と等しい。プール上限を超えると goroutine が
	// コネクション待ちで滞留し、並列化の効果が出ないまま tickTimeout を消費する。
	if concurrency > maxOpenConns {
		return nil, fmt.Errorf(
			"invalid %s: %d exceeds %s (%d); concurrency must not exceed the connection pool size",
			envOutboxConcurrency, concurrency, envDBMaxOpenConns, maxOpenConns,
		)
	}

	return &Config{
		Port:               port,
		LogLevel:           level,
		MySQLDSN:           dsn,
		RedisAddr:          redisAddr,
		OutboxPollInterval: pollInterval,
		OutboxBatchSize:    batchSize,
		OutboxConcurrency:  concurrency,
		OutboxTickTimeout:  tickTimeout,
		OutboxRetention:    retention,
		OutboxMaxRetry:     maxRetry,
		DBPingTimeout:      pingTimeout,
		DBMaxOpenConns:     maxOpenConns,
		DBMaxIdleConns:     maxIdleConns,
		DBConnMaxLifetime:  connMaxLifetime,
		DBConnMaxIdleTime:  connMaxIdleTime,
	}, nil
}
