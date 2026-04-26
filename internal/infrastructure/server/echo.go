// Package server はEchoサーバの生成・起動を担う。
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// New はミドルウェア設定済みのEchoインスタンスを生成する。
// アクセスログはslogへ流す。
func New(logger *slog.Logger) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

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
		LogValuesFunc: func(_ echo.Context, v middleware.RequestLoggerValues) error {
			attrs := []slog.Attr{
				slog.String("request_id", v.RequestID),
				slog.String("method", v.Method),
				slog.String("uri", v.URI),
				slog.Int("status", v.Status),
				slog.Duration("latency", v.Latency),
			}
			if v.Error != nil {
				attrs = append(attrs, slog.String("error", v.Error.Error()))
				logger.LogAttrs(context.Background(), slog.LevelError, "request", attrs...)
				return nil
			}
			logger.LogAttrs(context.Background(), slog.LevelInfo, "request", attrs...)
			return nil
		},
	}))

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
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("failed to start server: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
