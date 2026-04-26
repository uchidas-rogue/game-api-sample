// Package health はヘルスチェックのHTTPハンドラを提供する。
package health

import (
	"net/http"

	"github.com/labstack/echo/v4"

	healthusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/health"
)

// Handler はヘルスチェックHTTPハンドラ。
type Handler struct {
	usecase healthusecase.Usecase
}

// NewHandler はHandlerの新しいインスタンスを生成する。
func NewHandler(u healthusecase.Usecase) *Handler {
	return &Handler{usecase: u}
}

// response はヘルスチェックAPIのレスポンスボディ。
type response struct {
	Status string `json:"status"`
}

// Check はGET /healthzのハンドラ。
// usecaseを呼び出して稼働状態をJSONで返却する。
func (h *Handler) Check(c echo.Context) error {
	status, err := h.usecase.Check(c.Request().Context())
	if err != nil {
		// usecase側でエラーが発生した場合は503を返す。
		return c.JSON(http.StatusServiceUnavailable, response{Status: err.Error()})
	}
	return c.JSON(http.StatusOK, response{Status: status.String()})
}
