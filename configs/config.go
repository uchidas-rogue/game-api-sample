// Package configs は環境変数からアプリケーション設定を読み込む。
package configs

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
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
)

// Config はアプリケーション全体の設定値を保持する。
type Config struct {
	Port      int
	LogLevel  slog.Level
	MySQLDSN  string
	RedisAddr string
}

// Load は環境変数から設定値を読み込む。
// PORT未設定時は8080、LOG_LEVEL未設定時はinfoを使用する。
// LOG_LEVELに指定できる値: debug / info / warn / error（大文字小文字不問）
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

	return &Config{
		Port:      port,
		LogLevel:  level,
		MySQLDSN:  dsn,
		RedisAddr: redisAddr,
	}, nil
}
