// Package health はヘルスチェックのHTTPハンドラを提供する。
package health

import (
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	healthdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/health"
	healthusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/health"
)

// Handler はヘルスチェックHTTPハンドラ。
type Handler struct {
	usecase healthusecase.Usecase
	logger  *slog.Logger
}

// NewHandler はHandlerの新しいインスタンスを生成する。logger は必須。
func NewHandler(u healthusecase.Usecase, logger *slog.Logger) *Handler {
	return &Handler{usecase: u, logger: logger}
}

// response はヘルスチェックAPIのレスポンスボディ。
type response struct {
	Status string `json:"status"`
}

// Check はGET /healthzのハンドラ。
// usecaseを呼び出して稼働状態をJSONで返却する。
func (h *Handler) Check(c echo.Context) error {
	ctx := c.Request().Context()

	status, err := h.usecase.Check(ctx)
	if err != nil {
		// エラーの詳細はレスポンスに載せずログにだけ出す（情報漏えい防止）。
		// /healthz は認証なしで外部公開されるため、依存リソースの疎通確認を追加した際に
		// 接続先やドライバのエラー文言がそのまま外へ出るのを防ぐ。
		h.logger.ErrorContext(ctx, "health check failed", slog.Any("error", err))
		return c.JSON(http.StatusServiceUnavailable, response{Status: healthdomain.StatusDown.String()})
	}
	return c.JSON(http.StatusOK, response{Status: status.String()})
}
