package ranking

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
)

// retryAfter はランキング未構築時にクライアントへ提示する再試行までの待ち時間。
// 復旧は再構築バッチの手動実行を伴うため、即時の再試行が実る値にはしない。
// HTTP delivery の Retry-After ヘッダ（internal/driver/http/ranking）と同じ値にする。
const retryAfter = 30 * time.Second

// handleError は usecase のエラーを gRPC の status へ変換する。
// 分岐と対応表は docs/testing/grpc-ranking.md §4 が正本。
//
// ctx キャンセルはここで扱わない。gRPC ランタイムが codes.Canceled /
// codes.DeadlineExceeded を付けるため、ハンドラが重ねて判定すると二重管理になる。
func (h *Handler) handleError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, rankingdomain.ErrGuildNotFound):
		return status.Error(codes.NotFound, "guild not found")
	case errors.Is(err, rankingdomain.ErrUserNotFound):
		return status.Error(codes.NotFound, "user not found")
	case errors.Is(err, rankingdomain.ErrUserNotInGuild):
		return status.Error(codes.PermissionDenied, "user is not a member of the guild")
	case errors.Is(err, rankingdomain.ErrInvalidScore):
		return status.Error(codes.InvalidArgument, "invalid score")
	case errors.Is(err, rankingdomain.ErrInvalidPoints):
		return status.Error(codes.InvalidArgument, "invalid points")
	case errors.Is(err, rankingdomain.ErrScoreNotFound):
		return status.Error(codes.NotFound, "score not found")
	case errors.Is(err, rankingdomain.ErrPointsNotFound):
		return status.Error(codes.NotFound, "points not found")
	case errors.Is(err, rankingdomain.ErrRankingUnavailable):
		// ランキングが未構築（Redis 揮発を含む）。再構築すれば解消する一時的な状態なので、
		// 「対象が未登録」を表す NotFound ではなく Unavailable を返し、再試行が有効で
		// あることを RetryInfo で伝える（HTTP の Retry-After ヘッダに相当）。
		// 原因の詳細はメッセージに載せない（Internal と同じ方針）。
		//
		// Internal と違い、ここではログを出さない。この状態は再構築するまで継続するため、
		// リクエストごとに記録すると 1 件の障害が毎秒数千行の同一ログになり、
		// 他のエラーを埋めてしまう。Unavailable を返した事実はアクセスログに残る。
		return unavailableError()
	default:
		h.logger.ErrorContext(ctx, "ranking operation failed", slog.Any("error", err))
		return status.Error(codes.Internal, "internal server error")
	}
}

// unavailableError は RetryInfo を details に載せた Unavailable の status を返す。
//
// WithDetails は detail の proto marshal に失敗したときだけエラーを返すため、
// 固定値の RetryInfo を渡す本実装では到達しない。それでもエラーを握りつぶさず
// details 無しの Unavailable へフォールバックするのは、details の欠落を理由に
// ステータス自体を落とすと「揮発中に Internal が返る」というより悪い挙動になるため。
func unavailableError() error {
	st := status.New(codes.Unavailable, "ranking is unavailable")

	detailed, err := st.WithDetails(&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)})
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
}
