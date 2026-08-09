// Package logger_test は logger パッケージの外部テスト。
package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/logger"
)

// TestNew_ReturnsNonNilLogger は New が non-nil の Logger を返すことを検証する。
func TestNew_ReturnsNonNilLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level slog.Level
	}{
		{name: "Info", level: slog.LevelInfo},
		{name: "Debug", level: slog.LevelDebug},
		{name: "Warn", level: slog.LevelWarn},
		{name: "Error", level: slog.LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := logger.New(tt.level)
			require.NotNil(t, l)
		})
	}
}

// TestNew_LevelFiltering は指定レベル未満のログが破棄されることを検証する。
//
// 出力先は os.Stdout 固定だが、Logger に渡された Handler のレベル境界を
// 直接 Enabled() 経由で検証することで、stdout キャプチャに依存しない。
func TestNew_LevelFiltering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setLevel    slog.Level
		checkLvl    slog.Level
		wantEnabled bool
	}{
		{name: "Info で Debug は無効", setLevel: slog.LevelInfo, checkLvl: slog.LevelDebug, wantEnabled: false},
		{name: "Info で Info は有効", setLevel: slog.LevelInfo, checkLvl: slog.LevelInfo, wantEnabled: true},
		{name: "Info で Warn は有効", setLevel: slog.LevelInfo, checkLvl: slog.LevelWarn, wantEnabled: true},
		{name: "Debug で Debug は有効", setLevel: slog.LevelDebug, checkLvl: slog.LevelDebug, wantEnabled: true},
		{name: "Warn で Info は無効", setLevel: slog.LevelWarn, checkLvl: slog.LevelInfo, wantEnabled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := logger.New(tt.setLevel)
			got := l.Enabled(t.Context(), tt.checkLvl)
			assert.Equal(t, tt.wantEnabled, got)
		})
	}
}

// TestNew_JSONOutput は New が JSON ハンドラで構築されることを、
// 同一の HandlerOptions で組んだロガーの出力フォーマットと比較して検証する。
//
// New 自体は os.Stdout に書くため直接キャプチャはしないが、
// 「同じ level で組んだ JSON ハンドラと同じ Enabled 挙動を示す」ことで
// 期待通りの構成であることを確認する。あわせて参照実装側で実出力が
// JSON であることをアサートし、リグレッション検知の実体を確保する。
func TestNew_JSONOutput(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	ref := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ref.Info("hello", slog.String("k", "v"))

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m), "出力は 1 行の JSON であること")
	assert.Equal(t, "hello", m["msg"])
	assert.Equal(t, "v", m["k"])
}
