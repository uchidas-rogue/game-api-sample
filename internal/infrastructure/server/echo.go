// Package server はEchoサーバの生成・起動を担う。
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// HTTP サーバの運用値。いずれもこの層固有なので server パッケージで定義する（AGENTS.md §2）。
// 環境ごとに変える必要が出たら configs へ移す。
const (
	// shutdownTimeout はグレースフルシャットダウンで処理中リクエストの完了を待つ上限。
	shutdownTimeout = 10 * time.Second

	// readHeaderTimeout はリクエストヘッダを読み切るまでの上限。
	// net/http の既定は無制限で、ヘッダを少しずつ送り続けるだけの接続に
	// ソケットとゴルーチンを占有され続ける（slowloris）。
	// DBMaxOpenConns で DB 接続に上限を張っても、その手前がここで詰まると意味がない。
	readHeaderTimeout = 5 * time.Second

	// readTimeout はヘッダとボディを含むリクエスト全体の読み取り上限。
	readTimeout = 10 * time.Second

	// writeTimeout はレスポンスを書き終えるまでの上限。
	// 本 API のハンドラは DB/Redis への短い往復のみで、ストリーミング応答を持たない。
	writeTimeout = 10 * time.Second

	// idleTimeout は keep-alive 接続を維持する上限。
	// 未設定だと net/http が readTimeout を流用するため、負荷試験で接続の
	// 張り直しが増える。リクエスト読み取りより十分長くとる。
	idleTimeout = 120 * time.Second

	// bodyLimit はリクエストボディの上限。
	// 現行エンドポイントのボディは最大でも数十バイト（{"points":N,"reason":"..."}）なので
	// 桁で余裕があり、かつ無制限にしないための値。
	bodyLimit = "64K"
)

// New はミドルウェア設定済みのEchoインスタンスを生成する。
// アクセスログはslogへ流す。
func New(logger *slog.Logger) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	// net/http の既定はいずれも無制限。Run が e.Server をそのまま起動するため、
	// ここで設定しておけば起動経路によらず有効になる。
	e.Server.ReadHeaderTimeout = readHeaderTimeout
	e.Server.ReadTimeout = readTimeout
	e.Server.WriteTimeout = writeTimeout
	e.Server.IdleTimeout = idleTimeout

	// リクエストID付与
	e.Use(middleware.RequestID())

	// アクセスログをslogに統合
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogStatus:    true,
		LogURI:       true,
		LogMethod:    true,
		LogLatency:   true,
		LogRequestID: true,
		LogError:     true,
		HandleError:  true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			// アクセスログにリクエストctxを渡すことで、slog.Handlerがctx経由のtrace_id等を拾えるようにする（OTel/Datadog連携を見据えた設計）
			// LogValuesFuncはハンドラ完了後に呼ばれるためctxはcanceled済みの可能性があるが、
			// 標準slog.Handlerはctxのキャンセル状態を参照しないためログ欠落は発生しない。
			ctx := c.Request().Context()
			attrs := []slog.Attr{
				slog.String("request_id", v.RequestID),
				slog.String("method", v.Method),
				slog.String("uri", v.URI),
				slog.Int("status", v.Status),
				slog.Duration("latency", v.Latency),
			}
			if v.Error != nil {
				attrs = append(attrs, slog.Any("error", v.Error))
				logger.LogAttrs(ctx, slog.LevelError, "request", attrs...)
				return nil
			}
			logger.LogAttrs(ctx, slog.LevelInfo, "request", attrs...)
			return nil
		},
	}))

	// リクエストボディの上限（未設定だと無制限）。
	// アクセスログより **後** に登録して内側に置く。先に置くと、弾いた 413 が
	// ログミドルウェアの外側で完結してしまい、request_id もアクセスログも残らない。
	e.Use(middleware.BodyLimit(bodyLimit))

	// パニック時のリカバリ
	e.Use(middleware.Recover())

	return e
}

// Run は指定ポートでEchoサーバを起動し、ctxのキャンセルでグレースフルシャットダウンする。
func Run(ctx context.Context, e *echo.Echo, port int, logger *slog.Logger) error {
	addr := fmt.Sprintf(":%d", port)

	// 起動を非同期で行い、ctxキャンセルでシャットダウン
	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting http server", slog.String("addr", addr))
		if err := e.Start(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("failed to start server: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		logger.Info("shutting down http server")
		if err := e.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("failed to shutdown server: %w", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}
