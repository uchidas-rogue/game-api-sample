// Package health_test はヘルスチェック HTTP ハンドラの外部テストパッケージ。
//
// テスト設計（フロー図・テスト仕様表）は docs/testing/http-health.md にある。
// 分岐を追加・変更したら、まず図と表を更新してからここを直す。
package health_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	healthdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/health"
	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/health"
	mockhealth "github.com/uchidas-rogue/game-api-sample/internal/usecase/health/mock"
)

// checkCase は Check のテストケース1件。データのみを持つ。
type checkCase struct {
	name string

	// ---- usecase の戻り値 ----
	mockStatus healthdomain.HealthStatus
	mockErr    error

	// ---- 期待結果 ----
	wantStatusCode int
	wantBody       string
}

// TestHandler_Check はヘルスチェックハンドラの振る舞いを検証する。
// docs/testing/http-health.md「1-2. テスト仕様表」と 1 対 1 で対応する。
func TestHandler_Check(t *testing.T) {
	t.Parallel()

	// leakedDetail は異常系でレスポンスに漏れてはならない文字列。
	// 実運用では接続文字列などがここに入りうる。
	const leakedDetail = "dial tcp 10.0.0.1:3306: connection refused"

	tests := []checkCase{
		{
			// #1 A→B→E1
			name:           "usecase がエラー: 503 を返し、エラー詳細をレスポンスに載せない",
			mockErr:        errors.New(leakedDetail),
			wantStatusCode: http.StatusServiceUnavailable,
			wantBody:       `{"status":"down"}`,
		},
		{
			// #2 A→B→Z
			name:           "正常系: 200 と稼働状態を返す",
			mockStatus:     healthdomain.StatusOK,
			wantStatusCode: http.StatusOK,
			wantBody:       `{"status":"ok"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc := mockhealth.NewMockUsecase(ctrl)
			uc.EXPECT().Check(gomock.Any()).Return(tt.mockStatus, tt.mockErr).Times(1)

			// エラー詳細が「レスポンスには出ないが、ログには出る」ことを見るため、
			// ログを捕捉できる logger を渡す。
			var logBuf bytes.Buffer
			h := healthhandler.NewHandler(uc,
				slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError})))

			rec := recordCheck(t, h)

			assert.Equal(t, tt.wantStatusCode, rec.Code)
			assert.JSONEq(t, tt.wantBody, rec.Body.String())

			if tt.mockErr == nil {
				return
			}
			// 情報漏えい防止がこの分岐の存在理由なので、そこを検証する。
			assert.NotContains(t, rec.Body.String(), leakedDetail,
				"エラー詳細をレスポンスに載せないこと")
			assert.Contains(t, logBuf.String(), leakedDetail,
				"エラー詳細はログには残すこと（握り潰さない）")
		})
	}
}

// recordCheck は GET /healthz を1回実行し、記録したレスポンスを返す。
// ハンドラが error を返さないこと（Echo の既定エラーハンドラに委ねないこと）も
// ここで一律に検証する。
func recordCheck(t *testing.T, h *healthhandler.Handler) *httptest.ResponseRecorder {
	t.Helper()

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.Check(c), "ハンドラは error を返さず、自身でレスポンスを組み立てる")
	return rec
}
