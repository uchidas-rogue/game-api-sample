// Package ranking はランキング機能の HTTP ハンドラを提供する。
package ranking

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
)

const (
	paramGuildID = "guildID"
	paramUserID  = "userID"
	queryLimit   = "limit"
	queryOffset  = "offset"
)

// retryAfterSeconds はランキング未構築時に返す Retry-After（秒）。
// 復旧は再構築バッチの手動実行を伴うため、即時の再試行が実る値にはしない。
const retryAfterSeconds = "30"

// Handler はランキング機能の HTTP ハンドラ。
type Handler struct {
	usecase rankingusecase.Usecase
	logger  *slog.Logger
}

// NewHandler は Handler を生成する。
func NewHandler(u rankingusecase.Usecase, logger *slog.Logger) *Handler {
	return &Handler{usecase: u, logger: logger}
}

type rankEntryResponse struct {
	Rank  int64  `json:"rank"`
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Score int64  `json:"score"`
}

type rankingsResponse struct {
	Rankings   []rankEntryResponse `json:"rankings"`
	TotalCount int64               `json:"total_count"`
}

// GetGuildRankings は GET /rankings/guilds のハンドラ。
func (h *Handler) GetGuildRankings(c echo.Context) error {
	ctx := c.Request().Context()

	limit, err := parseNonNegativeIntQuery(c, queryLimit)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid limit"})
	}
	offset, err := parseNonNegativeIntQuery(c, queryOffset)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid offset"})
	}

	result, err := h.usecase.GetGuildRankings(ctx, rankingusecase.GetRankingsInput{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return h.handleError(c, err)
	}

	rankings := make([]rankEntryResponse, 0, len(result.Rankings))
	for _, r := range result.Rankings {
		rankings = append(rankings, rankEntryResponse{
			Rank:  r.Rank,
			ID:    r.ID,
			Name:  r.Name,
			Score: r.Score,
		})
	}

	return c.JSON(http.StatusOK, rankingsResponse{
		Rankings:   rankings,
		TotalCount: result.TotalCount,
	})
}

type guildRankResponse struct {
	GuildID     int64  `json:"guild_id"`
	GuildName   string `json:"guild_name"`
	Score       int64  `json:"score"`
	Rank        int64  `json:"rank"`
	TotalGuilds int64  `json:"total_guilds"`
}

// GetGuildRank は GET /guilds/:guildID/ranking のハンドラ。
func (h *Handler) GetGuildRank(c echo.Context) error {
	ctx := c.Request().Context()

	guildID, err := strconv.ParseInt(c.Param(paramGuildID), 10, 64)
	if err != nil || guildID <= 0 {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid guildID"})
	}

	result, err := h.usecase.GetGuildRank(ctx, guildID)
	if err != nil {
		return h.handleError(c, err)
	}

	return c.JSON(http.StatusOK, guildRankResponse{
		GuildID:     result.GuildID,
		GuildName:   result.GuildName,
		Score:       result.Score,
		Rank:        result.Rank,
		TotalGuilds: result.TotalGuilds,
	})
}

type addUserPointsRequest struct {
	Points int64  `json:"points"`
	Reason string `json:"reason"`
}

// addUserPointsResponse は AddUserPoints の HTTP レスポンス。
// 順位 (rank / guild_rank) は worker による Redis 反映後にしか確定しないため、
// 本エンドポイントでは返さない。クライアントは別途 GET /users/:userID/ranking
// および GET /guilds/:guildID/ranking を呼び出して取得する。
type addUserPointsResponse struct {
	UserID        int64 `json:"user_id"`
	Points        int64 `json:"points"`
	PreviousTotal int64 `json:"previous_total"`
	NewTotal      int64 `json:"new_total"`
	// ギルドスコア加算は非同期化したため guild の previous/new total は返さない。
	GuildID int64 `json:"guild_id"`
}

// AddUserPoints は POST /users/:userID/points のハンドラ。
func (h *Handler) AddUserPoints(c echo.Context) error {
	ctx := c.Request().Context()

	userID, err := strconv.ParseInt(c.Param(paramUserID), 10, 64)
	if err != nil || userID <= 0 {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid userID"})
	}

	var req addUserPointsRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid request body"})
	}
	if req.Reason == "" {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "reason is required"})
	}

	result, err := h.usecase.AddUserPoints(ctx, rankingusecase.AddUserPointsInput{
		UserID: userID,
		Points: req.Points,
		Reason: req.Reason,
	})
	if err != nil {
		return h.handleError(c, err)
	}

	return c.JSON(http.StatusOK, addUserPointsResponse{
		UserID:        result.UserID,
		Points:        result.Points,
		PreviousTotal: result.PreviousTotal,
		NewTotal:      result.NewTotal,
		GuildID:       result.GuildID,
	})
}

// GetUserRankings は GET /rankings/users のハンドラ。
func (h *Handler) GetUserRankings(c echo.Context) error {
	ctx := c.Request().Context()

	limit, err := parseNonNegativeIntQuery(c, queryLimit)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid limit"})
	}
	offset, err := parseNonNegativeIntQuery(c, queryOffset)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid offset"})
	}

	result, err := h.usecase.GetUserRankings(ctx, rankingusecase.GetRankingsInput{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return h.handleError(c, err)
	}

	rankings := make([]rankEntryResponse, 0, len(result.Rankings))
	for _, r := range result.Rankings {
		rankings = append(rankings, rankEntryResponse{
			Rank:  r.Rank,
			ID:    r.ID,
			Name:  r.Name,
			Score: r.Score,
		})
	}

	return c.JSON(http.StatusOK, rankingsResponse{
		Rankings:   rankings,
		TotalCount: result.TotalCount,
	})
}

type userRankResponse struct {
	UserID     int64  `json:"user_id"`
	UserName   string `json:"user_name"`
	Points     int64  `json:"points"`
	Rank       int64  `json:"rank"`
	TotalUsers int64  `json:"total_users"`
}

// GetUserRank は GET /users/:userID/ranking のハンドラ。
func (h *Handler) GetUserRank(c echo.Context) error {
	ctx := c.Request().Context()

	userID, err := strconv.ParseInt(c.Param(paramUserID), 10, 64)
	if err != nil || userID <= 0 {
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid userID"})
	}

	result, err := h.usecase.GetUserRank(ctx, userID)
	if err != nil {
		return h.handleError(c, err)
	}

	return c.JSON(http.StatusOK, userRankResponse{
		UserID:     result.UserID,
		UserName:   result.UserName,
		Points:     result.Points,
		Rank:       result.Rank,
		TotalUsers: result.TotalUsers,
	})
}

// handleError は usecase のエラーを HTTP レスポンスへ変換する。
// ctx は echo.Context から導出するため引数に取らない（context.Context を
// 第2引数に置くと Go の慣習に反し、渡し違いの余地も残るため）。
func (h *Handler) handleError(c echo.Context, err error) error {
	ctx := c.Request().Context()
	switch {
	case errors.Is(err, rankingdomain.ErrGuildNotFound):
		return c.JSON(http.StatusNotFound, errorResponse{Message: "guild not found"})
	case errors.Is(err, rankingdomain.ErrUserNotFound):
		return c.JSON(http.StatusNotFound, errorResponse{Message: "user not found"})
	case errors.Is(err, rankingdomain.ErrUserNotInGuild):
		return c.JSON(http.StatusForbidden, errorResponse{Message: "user is not a member of the guild"})
	case errors.Is(err, rankingdomain.ErrInvalidScore):
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid score"})
	case errors.Is(err, rankingdomain.ErrInvalidPoints):
		return c.JSON(http.StatusBadRequest, errorResponse{Message: "invalid points"})
	case errors.Is(err, rankingdomain.ErrScoreNotFound):
		return c.JSON(http.StatusNotFound, errorResponse{Message: "score not found"})
	case errors.Is(err, rankingdomain.ErrPointsNotFound):
		return c.JSON(http.StatusNotFound, errorResponse{Message: "points not found"})
	case errors.Is(err, rankingdomain.ErrRankingUnavailable):
		// ランキングが未構築（Redis 揮発を含む）。再構築すれば解消する一時的な状態なので、
		// 「対象が未登録」を表す 404 ではなく 503 を返し、再試行が有効であることを
		// Retry-After で伝える。原因の詳細はレスポンスに載せない（500 と同じ方針）。
		//
		// 500 と違い、ここではログを出さない。この状態は再構築するまで継続するため、
		// リクエストごとに記録すると 1 件の障害が毎秒数千行の同一ログになり、
		// 他のエラーを埋めてしまう。503 を返した事実はアクセスログに残る。
		c.Response().Header().Set(echo.HeaderRetryAfter, retryAfterSeconds)
		return c.JSON(http.StatusServiceUnavailable, errorResponse{Message: "ranking is unavailable"})
	default:
		h.logger.ErrorContext(ctx, "ranking operation failed", slog.Any("error", err))
		return c.JSON(http.StatusInternalServerError, errorResponse{Message: "internal server error"})
	}
}

type errorResponse struct {
	Message string `json:"message"`
}

// parseNonNegativeIntQuery は非負整数のクエリパラメータをパースする。
// 未指定（空文字）の場合は 0 を返し、usecase 側のデフォルト適用に委ねる。
// 数値変換失敗または負数の場合はエラーを返す。
func parseNonNegativeIntQuery(c echo.Context, key string) (int, error) {
	raw := c.QueryParam(key)
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("%s must be non-negative", key)
	}
	return v, nil
}
