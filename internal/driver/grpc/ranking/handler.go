// Package ranking はランキング機能の gRPC ハンドラを提供する。
//
// HTTP delivery（internal/driver/http/ranking）と同じ usecase を共有し、
// 応答の意味づけも揃えてある。テスト設計（フロー図・仕様表）は
// docs/testing/grpc-ranking.md が正本。
package ranking

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rankingv1 "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/gen/rankingv1"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
)

// Handler が RankingServiceServer を満たすことをコンパイル時に検証する。
var _ rankingv1.RankingServiceServer = (*Handler)(nil)

// Handler はランキング機能の gRPC ハンドラ。
//
// UnimplementedRankingServiceServer を値で埋め込んでいるため、本ハンドラが実装して
// いない RPC（WatchUserRankings）は codes.Unimplemented を返す。埋め込みを外すと
// proto にメソッドを追加した瞬間にコンパイルが通らなくなるので、外さないこと。
type Handler struct {
	rankingv1.UnimplementedRankingServiceServer

	usecase rankingusecase.Usecase
	logger  *slog.Logger
}

// NewHandler は Handler を生成する。
func NewHandler(u rankingusecase.Usecase, logger *slog.Logger) *Handler {
	return &Handler{usecase: u, logger: logger}
}

// GetUserRankings はユーザーランキングを返す。
func (h *Handler) GetUserRankings(
	ctx context.Context, req *rankingv1.GetUserRankingsRequest,
) (*rankingv1.GetUserRankingsResponse, error) {
	input, err := rankingsInput(req.GetLimit(), req.GetOffset())
	if err != nil {
		return nil, err
	}

	result, err := h.usecase.GetUserRankings(ctx, input)
	if err != nil {
		return nil, h.handleError(ctx, err)
	}

	return &rankingv1.GetUserRankingsResponse{
		Rankings:   rankEntriesToProto(result.Rankings),
		TotalCount: result.TotalCount,
	}, nil
}

// GetGuildRankings はギルドランキングを返す。
func (h *Handler) GetGuildRankings(
	ctx context.Context, req *rankingv1.GetGuildRankingsRequest,
) (*rankingv1.GetGuildRankingsResponse, error) {
	input, err := rankingsInput(req.GetLimit(), req.GetOffset())
	if err != nil {
		return nil, err
	}

	result, err := h.usecase.GetGuildRankings(ctx, input)
	if err != nil {
		return nil, h.handleError(ctx, err)
	}

	return &rankingv1.GetGuildRankingsResponse{
		Rankings:   rankEntriesToProto(result.Rankings),
		TotalCount: result.TotalCount,
	}, nil
}

// GetUserRank は単一ユーザーの順位を返す。
func (h *Handler) GetUserRank(
	ctx context.Context, req *rankingv1.GetUserRankRequest,
) (*rankingv1.GetUserRankResponse, error) {
	userID := req.GetUserId()
	if userID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}

	result, err := h.usecase.GetUserRank(ctx, userID)
	if err != nil {
		return nil, h.handleError(ctx, err)
	}

	return userRankToProto(result), nil
}

// GetGuildRank は単一ギルドの順位を返す。
func (h *Handler) GetGuildRank(
	ctx context.Context, req *rankingv1.GetGuildRankRequest,
) (*rankingv1.GetGuildRankResponse, error) {
	guildID := req.GetGuildId()
	if guildID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid guild_id")
	}

	result, err := h.usecase.GetGuildRank(ctx, guildID)
	if err != nil {
		return nil, h.handleError(ctx, err)
	}

	return guildRankToProto(result), nil
}

// AddUserPoints はユーザーにポイントを加算する。
//
// points の値域は検証しない。domain の IsValidScore が正本で、この層の責務は
// usecase が返す ErrInvalidPoints を InvalidArgument へ写すことだけ。
func (h *Handler) AddUserPoints(
	ctx context.Context, req *rankingv1.AddUserPointsRequest,
) (*rankingv1.AddUserPointsResponse, error) {
	userID := req.GetUserId()
	if userID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}
	if req.GetReason() == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}

	result, err := h.usecase.AddUserPoints(ctx, rankingusecase.AddUserPointsInput{
		UserID: userID,
		Points: req.GetPoints(),
		Reason: req.GetReason(),
	})
	if err != nil {
		return nil, h.handleError(ctx, err)
	}

	return userPointAddToProto(result), nil
}

// rankingsInput は一覧系リクエストのページング値を usecase の入力へ変換する。
//
// 弾くのは負値だけで、既定値の適用と上限の丸めは usecase（domain の NormalizeLimit /
// NormalizeOffset）に委ねる。ハンドラが既定値を持つと二重管理になるため、
// proto3 で未設定と区別できない 0 もそのまま渡す。
func rankingsInput(limit, offset int32) (rankingusecase.GetRankingsInput, error) {
	if limit < 0 {
		return rankingusecase.GetRankingsInput{}, status.Error(codes.InvalidArgument, "limit must not be negative")
	}
	if offset < 0 {
		return rankingusecase.GetRankingsInput{}, status.Error(codes.InvalidArgument, "offset must not be negative")
	}
	return rankingusecase.GetRankingsInput{Limit: int(limit), Offset: int(offset)}, nil
}
