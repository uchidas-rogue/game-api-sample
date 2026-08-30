// handleError は非公開の写像なので、内部テストパッケージから直接呼ぶ。
//
// 5 つのハンドラ経由で 9 分岐 × 5 をなぞっても同じコードパスを重ねるだけで検出力は
// 上がらない（docs/testing/grpc-ranking.md §4）。ハンドラ側が handleError へ
// 委譲していることは handler_test.go の代表ケースが確認する。
package ranking

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
)

// retryInfoFullName は google.rpc.RetryInfo の proto フルネーム。
// errdetails パッケージは driver のテストから import できない（.golangci.yml の depguard）ため、
// protoreflect が返すメッセージ名とフィールドで検証する。
const retryInfoFullName = "google.rpc.RetryInfo"

// logMessage は default 分岐が出す ERROR ログのメッセージ。
const logMessage = "ranking operation failed"

func TestHandleError(t *testing.T) {
	t.Parallel()

	// docs/testing/grpc-ranking.md「4. 共通のエラー分類」の仕様表と 1 対 1 で対応する。
	// sentinel を素で渡さずラップしているのは、判定が errors.Is（== ではない）で
	// 行われていることを固定するため。
	tests := []struct {
		name string

		// ---- 入力 ----
		err error

		// ---- 期待結果 ----
		wantCode      codes.Code
		wantMessage   string
		wantRetryInfo bool
		// wantLog は ERROR ログを出すべきか。障害中のログ氾濫を避けるため、
		// Unavailable では出さないことも同じ強さで固定する。
		wantLog bool
	}{
		{
			// #1 X→X1→R1
			name:        "ErrGuildNotFound: NotFound",
			err:         fmt.Errorf("get guild: %w", rankingdomain.ErrGuildNotFound),
			wantCode:    codes.NotFound,
			wantMessage: "guild not found",
		},
		{
			// #2 X→X1→R2
			name:        "ErrUserNotFound: NotFound",
			err:         fmt.Errorf("get user: %w", rankingdomain.ErrUserNotFound),
			wantCode:    codes.NotFound,
			wantMessage: "user not found",
		},
		{
			// #3 X→X1→R3
			name:        "ErrUserNotInGuild: PermissionDenied",
			err:         fmt.Errorf("get user guild: %w", rankingdomain.ErrUserNotInGuild),
			wantCode:    codes.PermissionDenied,
			wantMessage: "user is not a member of the guild",
		},
		{
			// #4 X→X1→R4
			// 現時点で ErrInvalidScore を返す実装は無く、ハンドラ経由では到達しない
			// （docs/testing/http-ranking.md §4 の【要対応】）。写像としては定義済みなので、
			// 不自然なモックを要さないこの層で分岐を固定しておく。
			name:        "ErrInvalidScore: InvalidArgument",
			err:         fmt.Errorf("update score: %w", rankingdomain.ErrInvalidScore),
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid score",
		},
		{
			// #5 X→X1→R5
			name:        "ErrInvalidPoints: InvalidArgument",
			err:         fmt.Errorf("add points: %w", rankingdomain.ErrInvalidPoints),
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid points",
		},
		{
			// #6 X→X1→R6
			name:        "ErrScoreNotFound: NotFound",
			err:         fmt.Errorf("guild 1: %w", rankingdomain.ErrScoreNotFound),
			wantCode:    codes.NotFound,
			wantMessage: "score not found",
		},
		{
			// #7 X→X1→R7
			name:        "ErrPointsNotFound: NotFound",
			err:         fmt.Errorf("user 1: %w", rankingdomain.ErrPointsNotFound),
			wantCode:    codes.NotFound,
			wantMessage: "points not found",
		},
		{
			// #8 X→X1→R9
			// 障害が続く間ずっと返るステータスなので、ログを出さないことも仕様。
			name:          "ErrRankingUnavailable: Unavailable と RetryInfo・ログ無し",
			err:           fmt.Errorf("check ranking initialized: %w", rankingdomain.ErrRankingUnavailable),
			wantCode:      codes.Unavailable,
			wantMessage:   "ranking is unavailable",
			wantRetryInfo: true,
		},
		{
			// #9 X→X1→R8
			name:        "未知のエラー: Internal・原因は返さずログに出す",
			err:         fmt.Errorf("get rank: %w", errors.New("redis connection refused")),
			wantCode:    codes.Internal,
			wantMessage: "internal server error",
			wantLog:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger, rec := slogtest.NewRecordingLogger(t, nil)
			h := &Handler{logger: logger}

			err := h.handleError(t.Context(), tt.err)

			st, ok := status.FromError(err)
			require.True(t, ok, "gRPC の status を持つエラーであること: %v", err)
			assert.Equal(t, tt.wantCode, st.Code())
			assert.Equal(t, tt.wantMessage, st.Message())
			assert.NotContains(t, st.Message(), tt.err.Error(), "原因の詳細をクライアントへ返さない")

			assertRetryDelay(t, st, tt.wantRetryInfo)

			if tt.wantLog {
				assert.Equal(t, 1, rec.Count(logMessage), "ERROR ログを 1 件出すこと")
				assert.Equal(t, 1, rec.Count(logMessage, "level=ERROR"), "ログレベルは ERROR")
				return
			}
			assert.Zero(t, rec.Count(logMessage), "分類できたエラーでログを出さないこと")
		})
	}
}

// assertRetryDelay は RetryInfo の有無と、載っている場合の待ち時間を検証する。
// クライアントへ「いつ再試行すべきか」を伝えるのが目的なので、正の秒数であることまで見る。
func assertRetryDelay(t *testing.T, st *status.Status, want bool) {
	t.Helper()

	var (
		found   bool
		seconds int64
	)
	for _, detail := range st.Details() {
		m, ok := detail.(proto.Message)
		if !ok || m.ProtoReflect().Descriptor().FullName() != retryInfoFullName {
			continue
		}
		found = true

		reflected := m.ProtoReflect()
		delayField := reflected.Descriptor().Fields().ByName("retry_delay")
		require.NotNil(t, delayField, "RetryInfo に retry_delay フィールドがあること")

		delay := reflected.Get(delayField).Message()
		secondsField := delay.Descriptor().Fields().ByName("seconds")
		require.NotNil(t, secondsField, "Duration に seconds フィールドがあること")
		seconds = delay.Get(secondsField).Int()
	}

	if !want {
		assert.False(t, found, "RetryInfo は Unavailable 以外では付けない")
		return
	}
	require.True(t, found, "RetryInfo が必要")
	assert.Positive(t, seconds, "RetryDelay は正の秒数")
	assert.Equal(t, int64(retryAfter.Seconds()), seconds, "RetryDelay は retryAfter と一致する")
}
