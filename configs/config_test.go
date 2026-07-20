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

// TestLoad_Defaults は全環境変数未設定時に既定値が返ることを検証する。
// env 干渉回避のため各テストで t.Setenv を使用し、t.Parallel() は付けない。
func TestLoad_Defaults(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("MYSQL_DSN", "")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("OUTBOX_POLL_INTERVAL", "")
	t.Setenv("OUTBOX_BATCH_SIZE", "")

	cfg, err := configs.Load()
	require.NoError(t, err)

	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, slog.LevelInfo, cfg.LogLevel)
	assert.Equal(t, "game:game@tcp(127.0.0.1:3306)/game_db?parseTime=true&loc=Local", cfg.MySQLDSN)
	assert.Equal(t, "127.0.0.1:6379", cfg.RedisAddr)
	assert.Equal(t, 10*time.Minute, cfg.OutboxPollInterval)
	assert.Equal(t, 100, cfg.OutboxBatchSize)
	assert.Equal(t, 25, cfg.DBMaxOpenConns)
	assert.Equal(t, 25, cfg.DBMaxIdleConns)
	assert.Equal(t, 5*time.Minute, cfg.DBConnMaxLifetime)
	assert.Equal(t, 5*time.Minute, cfg.DBConnMaxIdleTime)
}

func TestLoad_DBPool(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		t.Setenv("DB_MAX_OPEN_CONNS", "100")
		t.Setenv("DB_MAX_IDLE_CONNS", "0")
		t.Setenv("DB_CONN_MAX_LIFETIME", "1m")
		t.Setenv("DB_CONN_MAX_IDLE_TIME", "30s")

		cfg, err := configs.Load()
		require.NoError(t, err)
		assert.Equal(t, 100, cfg.DBMaxOpenConns)
		assert.Equal(t, 0, cfg.DBMaxIdleConns)
		assert.Equal(t, time.Minute, cfg.DBConnMaxLifetime)
		assert.Equal(t, 30*time.Second, cfg.DBConnMaxIdleTime)
	})

	t.Run("MaxOpenConns must be positive", func(t *testing.T) {
		t.Setenv("DB_MAX_OPEN_CONNS", "0")
		_, err := configs.Load()
		require.Error(t, err)
	})

	t.Run("MaxOpenConns rejects non-numeric", func(t *testing.T) {
		t.Setenv("DB_MAX_OPEN_CONNS", "abc")
		_, err := configs.Load()
		require.Error(t, err)
	})

	t.Run("ConnMaxLifetime rejects negative", func(t *testing.T) {
		t.Setenv("DB_CONN_MAX_LIFETIME", "-1s")
		_, err := configs.Load()
		require.Error(t, err)
	})
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
			t.Setenv("PORT", tt.value)
			t.Setenv("LOG_LEVEL", "")
			t.Setenv("MYSQL_DSN", "")
			t.Setenv("REDIS_ADDR", "")
			t.Setenv("OUTBOX_POLL_INTERVAL", "")
			t.Setenv("OUTBOX_BATCH_SIZE", "")

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
			t.Setenv("PORT", "")
			t.Setenv("LOG_LEVEL", tt.value)
			t.Setenv("MYSQL_DSN", "")
			t.Setenv("REDIS_ADDR", "")
			t.Setenv("OUTBOX_POLL_INTERVAL", "")
			t.Setenv("OUTBOX_BATCH_SIZE", "")

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
	t.Setenv("PORT", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("MYSQL_DSN", "user:pass@tcp(db:3306)/mydb")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("OUTBOX_POLL_INTERVAL", "")
	t.Setenv("OUTBOX_BATCH_SIZE", "")

	cfg, err := configs.Load()
	require.NoError(t, err)
	assert.Equal(t, "user:pass@tcp(db:3306)/mydb", cfg.MySQLDSN)
}

func TestLoad_RedisAddr(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("MYSQL_DSN", "")
	t.Setenv("REDIS_ADDR", "redis:6379")
	t.Setenv("OUTBOX_POLL_INTERVAL", "")
	t.Setenv("OUTBOX_BATCH_SIZE", "")

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
			t.Setenv("PORT", "")
			t.Setenv("LOG_LEVEL", "")
			t.Setenv("MYSQL_DSN", "")
			t.Setenv("REDIS_ADDR", "")
			t.Setenv("OUTBOX_POLL_INTERVAL", tt.value)
			t.Setenv("OUTBOX_BATCH_SIZE", "")

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
			t.Setenv("PORT", "")
			t.Setenv("LOG_LEVEL", "")
			t.Setenv("MYSQL_DSN", "")
			t.Setenv("REDIS_ADDR", "")
			t.Setenv("OUTBOX_POLL_INTERVAL", "")
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
