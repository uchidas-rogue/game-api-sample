// Package gacha はガチャ機能のHTTPハンドラを提供する。
package gacha

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	gachausecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
)

// パラメータ名・パスパラメータ名の定数化（マジックストリング禁止対応）。
const (
	paramUserID = "userID"
)

// pullCountRangeMessage は pull_count バリデーション違反時のメッセージを返す。
// 範囲はドメイン定数を参照することでハンドラ側にマジックナンバーを持たない。
func pullCountRangeMessage() string {
	return fmt.Sprintf("pull_count must be between %d and %d", gachadomain.MinPullCount, gachadomain.MaxPullCount)
}

// Handler はマルチガチャHTTPハンドラ。
type Handler struct {
	usecase gachausecase.Usecase
	logger  *slog.Logger
}

// NewHandler は Handler を生成する。logger は必須。
func NewHandler(u gachausecase.Usecase, logger *slog.Logger) *Handler {
	return &Handler{usecase: u, logger: logger}
}

// drawnItem は抽選結果1件分のレスポンス表現。
type drawnItem struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Rarity int    `json:"rarity"`
}

// multiRequest は POST /users/:userID/gacha/multi のリクエストボディ。
type multiRequest struct {
	// PullCount は抽選回数。1〜MaxPullCount の範囲でなければエラー。
	PullCount int `json:"pull_count"`
}

// multiResponse は POST /users/:userID/gacha/multi のレスポンス。
type multiResponse struct {
	UserID        int64       `json:"user_id"`
	DrawnItems    []drawnItem `json:"drawn_items"`
	RemainingGems int         `json:"remaining_gems"`
}

// errorResponse は共通エラーレスポンス。
type errorResponse struct {
	Message string `json:"message"`
}

// Multi は POST /users/:userID/gacha/multi のハンドラ。
// パスパラメータからユーザーIDを取り出し、usecase を呼び出して結果をJSONで返却する。
func (h *Handler) Multi(c echo.Context) error {
	ctx := c.Request().Context()

	userID, err := strconv.ParseInt(c.Param(paramUserID), 10, 64)
	if err != nil || userID <= 0 {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid userID"})
	}

	var req multiRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid request body"})
	}
	if !gachadomain.IsValidPullCount(req.PullCount) {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: pullCountRangeMessage()})
	}

	result, err := h.usecase.Multi(ctx, userID, req.PullCount)
	if err != nil {
		switch {
		case errors.Is(err, gachadomain.ErrInvalidPullCount):
			return c.JSON(http.StatusBadRequest, errorResponse{Message: pullCountRangeMessage()})
		case errors.Is(err, gachadomain.ErrUserNotFound):
			return c.JSON(http.StatusNotFound, errorResponse{Message: "user not found"})
		case errors.Is(err, gachadomain.ErrInsufficientGems):
			// クライアント側起因: 石不足。
			return c.JSON(http.StatusPaymentRequired, errorResponse{Message: "insufficient gems"})
		case errors.Is(err, gachadomain.ErrNoItemsAvailable),
			errors.Is(err, gachadomain.ErrInvalidItemWeights):
			// マスタデータ不備のためサービス利用不可。
			return c.JSON(http.StatusServiceUnavailable, errorResponse{Message: "gacha is unavailable"})
		default:
			// 予期せぬエラーは詳細を返さず500を返す（情報漏えい防止）。
			h.logger.ErrorContext(ctx, "multi pull failed",
				slog.Int64("user_id", userID),
				slog.Any("error", err),
			)
			return c.JSON(http.StatusInternalServerError, errorResponse{Message: "internal server error"})
		}
	}

	resp := multiResponse{
		UserID:        result.UserID,
		RemainingGems: result.RemainingGems,
		DrawnItems:    make([]drawnItem, 0, len(result.DrawnItems)),
	}
	for _, it := range result.DrawnItems {
		resp.DrawnItems = append(resp.DrawnItems, drawnItem{
			ID:     it.ID,
			Name:   it.Name,
			Rarity: it.Rarity,
		})
	}
	return c.JSON(http.StatusOK, resp)
}
