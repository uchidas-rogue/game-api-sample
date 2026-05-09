// Package server_test は server パッケージの外部テスト。
// New のミドルウェア動作と Run のグレースフルシャットダウン・起動エラーを検証する。
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/server"
)

// newTestLogger は bytes.Buffer へ JSON 形式でログを書き込む *slog.Logger を返す。
// テスト内でログ出力内容をアサーションするために使用する。
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h)
}

// parseLogLines は buf に書かれた改行区切りの JSON ログ行を map のスライスとして返す。
func parseLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var result []map[string]any
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		var m map[string]any
		if err := dec.Decode(&m); errors.Is(err, nil) {
			result = append(result, m)
		} else {
			break
		}
	}
	return result
}

// TestNew_RequestLoggingMiddleware は New が返す Echo インスタンスのミドルウェアを検証する。
func TestNew_RequestLoggingMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		handler        echo.HandlerFunc
		wantStatusCode int
		wantLogLevel   string
		wantError      bool
	}{
		{
			name: "正常系_200_Info ログが出力される",
			handler: func(c echo.Context) error {
				return c.String(http.StatusOK, "ok")
			},
			wantStatusCode: http.StatusOK,
			wantLogLevel:   "INFO",
			wantError:      false,
		},
		{
			name: "エラー系_HTTPError500_Error ログが出力される",
			handler: func(c echo.Context) error {
				return echo.NewHTTPError(http.StatusInternalServerError, "boom")
			},
			wantStatusCode: http.StatusInternalServerError,
			wantLogLevel:   "ERROR",
			wantError:      true,
		},
		{
			name: "パニック系_Recover が拾って 500 を返す",
			handler: func(c echo.Context) error {
				panic("unexpected panic")
			},
			wantStatusCode: http.StatusInternalServerError,
			wantLogLevel:   "", // Recover ミドルウェアが処理するためアクセスログの level は問わない
			wantError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logger := newTestLogger(&buf)
			e := server.New(logger)

			e.GET("/test", tt.handler)

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatusCode, rec.Code)

			if tt.wantLogLevel == "" {
				return
			}

			// アクセスログの出力を検証
			lines := parseLogLines(t, &buf)
			// request ログを探す
			var requestLog map[string]any
			for _, l := range lines {
				if l["msg"] == "request" {
					requestLog = l
					break
				}
			}
			require.NotNil(t, requestLog, "request ログが出力されていること")
			assert.Equal(t, tt.wantLogLevel, requestLog["level"])

			// 共通属性の検証
			assert.NotEmpty(t, requestLog["request_id"], "request_id が空でないこと")
			assert.Equal(t, http.MethodGet, requestLog["method"])
			assert.Equal(t, "/test", requestLog["uri"])
			assert.NotNil(t, requestLog["latency"])

			if tt.wantError {
				assert.NotNil(t, requestLog["error"], "エラー系では error 属性が付くこと")
			}
		})
	}
}

// TestRun_GracefulShutdown は ctx キャンセルでサーバが正常終了することを検証する。
func TestRun_GracefulShutdown(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newTestLogger(&buf)
	e := server.New(logger)
	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, e, 0, logger)
	}()

	// Echo がリッスンを開始するまでポーリング（最大 2 秒）。
	// e.ListenerAddr() は内部で mutex RLock を取るため race-safe。
	var started bool
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if e.ListenerAddr() != nil {
			started = true
			break
		}
	}
	require.True(t, started, "サーバが 2 秒以内に起動すること")

	// ctx をキャンセルしてシャットダウンを要求
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "グレースフルシャットダウンは nil を返すこと")
	case <-time.After(5 * time.Second):
		t.Fatal("Run が 5 秒以内に終了しなかった")
	}

	// ログ出力の検証
	logStr := buf.String()
	assert.True(t, strings.Contains(logStr, "starting http server"), "起動ログが出力されていること")
	assert.True(t, strings.Contains(logStr, "shutting down http server"), "シャットダウンログが出力されていること")
}

// TestRun_StartError は使用中のポートを渡した場合に起動エラーが返ることを検証する。
func TestRun_StartError(t *testing.T) {
	t.Parallel()

	// 先にポートを占有する。0.0.0.0 で listen することで ":port" 指定も衝突させる。
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	occupiedPort := addr.Port

	var buf bytes.Buffer
	logger := newTestLogger(&buf)
	e := server.New(logger)

	// キャンセルしない ctx を渡す。errCh 経路でエラーが返るはず。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, e, occupiedPort, logger)
	}()

	select {
	case runErr := <-done:
		require.Error(t, runErr, "ポート占有時はエラーを返すこと")
		assert.Contains(t, runErr.Error(), "failed to start server", "エラーメッセージに 'failed to start server' が含まれること")
	case <-time.After(5 * time.Second):
		t.Fatal("Run が 5 秒以内に終了しなかった")
	}
}
