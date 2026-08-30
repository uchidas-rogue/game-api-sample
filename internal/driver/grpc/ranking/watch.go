package ranking

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rankingv1 "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/gen/rankingv1"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
)

// WatchUserRankings はユーザーランキングの更新をクライアントへ push し続ける。
// フロー図とテスト仕様表は docs/testing/grpc-ranking.md §5 が正本。
//
// 更新の検知・差分判定・遅い購読者の扱いは usecase 側のハブ（RankingWatcher）の責務で、
// この層は購読を開始し、流れてきた結果を pb メッセージへ変換して送るだけ。
// ハブは購読の直後に現在値を1件流すため、初期表示のために GetUserRankings を
// 別途呼ぶ必要はない。
//
// 送信は本メソッドの goroutine からのみ行う。grpc.ServerStream の SendMsg は
// 並行呼び出しが許されていないため、受信と送信を別 goroutine へ分けないこと。
func (h *Handler) WatchUserRankings(
	req *rankingv1.WatchUserRankingsRequest, stream rankingv1.RankingService_WatchUserRankingsServer,
) error {
	// ハブはこの ctx を見て購読者を解除する（クライアント切断 = ストリームの ctx 終了）。
	// 別の ctx を渡すと購読者がハブに残り続ける。
	ctx := stream.Context()

	limit := req.GetLimit()
	if limit < 0 {
		return status.Error(codes.InvalidArgument, "limit must not be negative")
	}

	// 既定値の適用と上限の丸めはハブ（domain の NormalizeLimit）に委ねる。unary の
	// 一覧系（rankingsInput）と同じ設計意図で、未設定と区別できない 0 もそのまま渡す。
	updates, err := h.watcher.WatchUserRankings(ctx, int(limit))
	if err != nil {
		// ハブの停止は「常駐プロセスが終了処理に入った」というストリーム固有の事由で、
		// unary 5 メソッドが共有する handleError（§4）には現れない分岐。到達しない行を
		// あちらへ足さないよう、ここで写す。
		//
		// RetryInfo を載せないのは、§4 の R9（ZSet 揮発）が 30 秒待ちを提示するのと
		// 事情が違うため。こちらは即座に再接続すれば別インスタンスで繋がりうる。
		if errors.Is(err, rankingusecase.ErrWatcherStopped) {
			return status.Error(codes.Unavailable, "ranking watch is unavailable")
		}
		return h.handleError(ctx, err)
	}

	for res := range updates {
		if err := stream.Send(&rankingv1.WatchUserRankingsResponse{
			Rankings:   rankEntriesToProto(res.Rankings),
			TotalCount: res.TotalCount,
		}); err != nil {
			// クライアント切断は日常的に起きるサーバ外の事由。status に包み直すと
			// code が二重に決まるため、gRPC ランタイムへそのまま返す（ログも出さない）。
			return err
		}
	}

	// チャネルのクローズが唯一の正常終了。ctx キャンセル（クライアント切断）でも
	// ハブ停止でも起きるが、この層からは区別できず、区別する必要も無い。
	return nil
}
