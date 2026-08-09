// Package configs_test は configs パッケージの外部テストパッケージ。
package configs_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uchidas-rogue/game-api-sample/configs"
)

// clearConfigEnv は Load が参照する全ての環境変数を空にする。
//
// 各テストが個別に列挙すると、設定値を追加したときに一部のテストだけ更新漏れが起き、
// 実行環境の環境変数に結果が左右される（= flaky）。列挙はここ1箇所に集約する。
// AGENTS.md §2 の「新規の設定値追加時は Config 構造体・既定値・Load() の3箇所を更新する」
// に該当する変更をしたら、このリストにも追加すること。
//
// t.Setenv を使うため、呼び出し元のテストは t.Parallel() を付けられない。
func clearConfigEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"PORT",
		"LOG_LEVEL",
		"MYSQL_DSN",
		"REDIS_ADDR",
		"OUTBOX_POLL_INTERVAL",
		"OUTBOX_BATCH_SIZE",
		"OUTBOX_CONCURRENCY",
		"OUTBOX_TICK_TIMEOUT",
		"OUTBOX_RETENTION",
		"DB_PING_TIMEOUT",
		"DB_MAX_OPEN_CONNS",
		"DB_MAX_IDLE_CONNS",
		"DB_CONN_MAX_LIFETIME",
		"DB_CONN_MAX_IDLE_TIME",
	} {
		t.Setenv(key, "")
	}
}

// TestLoad_Defaults は全環境変数未設定時に既定値が返ることを検証する。
func TestLoad_Defaults(t *testing.T) {
	clearConfigEnv(t)

	cfg, err := configs.Load()
	require.NoError(t, err)

	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, slog.LevelInfo, cfg.LogLevel)
	assert.Equal(t, "game:game@tcp(127.0.0.1:3306)/game_db?parseTime=true&loc=Local", cfg.MySQLDSN)
	assert.Equal(t, "127.0.0.1:6379", cfg.RedisAddr)
	assert.Equal(t, 10*time.Minute, cfg.OutboxPollInterval)
	assert.Equal(t, 500, cfg.OutboxBatchSize)
	assert.Equal(t, 8, cfg.OutboxConcurrency)
	assert.Equal(t, 30*time.Second, cfg.OutboxTickTimeout)
	assert.Equal(t, 72*time.Hour, cfg.OutboxRetention)
	assert.Equal(t, 5*time.Second, cfg.DBPingTimeout)
	assert.Equal(t, 25, cfg.DBMaxOpenConns)
	assert.Equal(t, 25, cfg.DBMaxIdleConns)
	assert.Equal(t, 5*time.Minute, cfg.DBConnMaxLifetime)
	assert.Equal(t, 5*time.Minute, cfg.DBConnMaxIdleTime)
}

func TestLoad_PORT(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    int
	}{
		{name: "有効: 数値", value: "9090", want: 9090},
		{name: "無効: 数値でない", value: "abc", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("PORT", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Port)
		})
	}
}

func TestLoad_LogLevel(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    slog.Level
	}{
		{name: "debug", value: "debug", want: slog.LevelDebug},
		{name: "warn", value: "warn", want: slog.LevelWarn},
		{name: "error", value: "error", want: slog.LevelError},
		{name: "大文字DEBUG", value: "DEBUG", want: slog.LevelDebug},
		{name: "無効: verbose", value: "verbose", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("LOG_LEVEL", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.LogLevel)
		})
	}
}

func TestLoad_MySQLDSN(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("MYSQL_DSN", "user:pass@tcp(db:3306)/mydb")

	cfg, err := configs.Load()
	require.NoError(t, err)
	assert.Equal(t, "user:pass@tcp(db:3306)/mydb", cfg.MySQLDSN)
}

func TestLoad_RedisAddr(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("REDIS_ADDR", "redis:6379")

	cfg, err := configs.Load()
	require.NoError(t, err)
	assert.Equal(t, "redis:6379", cfg.RedisAddr)
}

func TestLoad_OutboxPollInterval(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    time.Duration
	}{
		{name: "有効: 2s", value: "2s", want: 2 * time.Second},
		{name: "有効: 500ms", value: "500ms", want: 500 * time.Millisecond},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: 0s（0以下）", value: "0s", wantErr: true},
		{name: "無効: -1s（負値）", value: "-1s", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("OUTBOX_POLL_INTERVAL", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.OutboxPollInterval)
		})
	}
}

// TestLoad_OutboxConcurrency は並列度のパースと、接続プール上限との整合検証を確認する。
// 並列度は同時トランザクション数と等しいため、DB_MAX_OPEN_CONNS を超える設定は
// goroutine が接続待ちで滞留するだけなので起動時にエラーにする。
func TestLoad_OutboxConcurrency(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		maxOpenConn string
		wantErr     bool
		want        int
	}{
		{name: "有効: 16", value: "16", want: 16},
		{name: "有効: プール上限ちょうど", value: "25", want: 25},
		{name: "有効: プールを広げれば超過も可", value: "50", maxOpenConn: "50", want: 50},
		{name: "無効: プール上限を超過", value: "26", wantErr: true},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: 0（0以下）", value: "0", wantErr: true},
		{name: "無効: -1（負値）", value: "-1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("DB_MAX_OPEN_CONNS", tt.maxOpenConn)
			t.Setenv("OUTBOX_CONCURRENCY", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.OutboxConcurrency)
		})
	}
}

func TestLoad_OutboxBatchSize(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    int
	}{
		{name: "有効: 50", value: "50", want: 50},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: 0（0以下）", value: "0", wantErr: true},
		{name: "無効: -1（負値）", value: "-1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("OUTBOX_BATCH_SIZE", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.OutboxBatchSize)
		})
	}
}

// TestLoad_OutboxTickTimeout / TestLoad_OutboxRetention / TestLoad_DBPingTimeout は
// OUTBOX_POLL_INTERVAL と同じ検証構造を持つ独立したコードブロックを対象にする。
// これらは同じ形だが条件式はそれぞれ別のソース行にあるため、
// 1 ブロックのテストで他を代表させることはできない（AGENTS.md §3 の対称性チェック）。
//
// 0s と -1s はどちらも `parsed <= 0` の同一分岐に入るが、別ケースとして残す。
// 0s は `<=` を `<` と書き誤った場合、-1s は `== 0` と書き誤った場合を検出するため
// （境界値を独立ケースにする3基準のうち「片方だけが検出できる実装ミスがある」に該当）。
func TestLoad_OutboxTickTimeout(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    time.Duration
	}{
		{name: "有効: 10s", value: "10s", want: 10 * time.Second},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: 0s（0以下）", value: "0s", wantErr: true},
		{name: "無効: -1s（負値）", value: "-1s", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("OUTBOX_TICK_TIMEOUT", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.OutboxTickTimeout)
		})
	}
}

func TestLoad_OutboxRetention(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    time.Duration
	}{
		{name: "有効: 24h", value: "24h", want: 24 * time.Hour},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: 0s（0以下）", value: "0s", wantErr: true},
		{name: "無効: -1s（負値）", value: "-1s", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("OUTBOX_RETENTION", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.OutboxRetention)
		})
	}
}

func TestLoad_DBPingTimeout(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    time.Duration
	}{
		{name: "有効: 3s", value: "3s", want: 3 * time.Second},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: 0s（0以下）", value: "0s", wantErr: true},
		{name: "無効: -1s（負値）", value: "-1s", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("DB_PING_TIMEOUT", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.DBPingTimeout)
		})
	}
}

// DB プール設定4件は同型の検証ブロックが並ぶ（AGENTS.md §3 の対称性チェック）。
// MaxOpenConns だけが `<= 0` を拒否し、他の3つは 0 を許容する（`< 0` を拒否）ため、
// 各ブロックの境界値ケースは意図的に非対称にしてある。
func TestLoad_DBMaxOpenConns(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    int
	}{
		{name: "有効: 100", value: "100", want: 100},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: 0（0以下）", value: "0", wantErr: true},
		{name: "無効: -1（負値）", value: "-1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("DB_MAX_OPEN_CONNS", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.DBMaxOpenConns)
		})
	}
}

func TestLoad_DBMaxIdleConns(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    int
	}{
		{name: "有効: 50", value: "50", want: 50},
		{name: "有効: 0（アイドル接続を保持しない）", value: "0", want: 0},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: -1（負値）", value: "-1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("DB_MAX_IDLE_CONNS", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.DBMaxIdleConns)
		})
	}
}

func TestLoad_DBConnMaxLifetime(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    time.Duration
	}{
		{name: "有効: 1m", value: "1m", want: time.Minute},
		{name: "有効: 0s（無制限）", value: "0s", want: 0},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: -1s（負値）", value: "-1s", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("DB_CONN_MAX_LIFETIME", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.DBConnMaxLifetime)
		})
	}
}

func TestLoad_DBConnMaxIdleTime(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
		want    time.Duration
	}{
		{name: "有効: 30s", value: "30s", want: 30 * time.Second},
		{name: "有効: 0s（無制限）", value: "0s", want: 0},
		{name: "無効: 不正フォーマット", value: "abc", wantErr: true},
		{name: "無効: -1s（負値）", value: "-1s", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("DB_CONN_MAX_IDLE_TIME", tt.value)

			cfg, err := configs.Load()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.DBConnMaxIdleTime)
		})
	}
}
