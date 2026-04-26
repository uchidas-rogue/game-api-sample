package health_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"go.uber.org/mock/gomock"

	healthdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/health"
	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/interface/handler/health"
	mock_health "github.com/uchidas-rogue/game-api-sample/internal/usecase/health/mock"
)

// TestHandler_Check はヘルスチェックハンドラの振る舞いを検証する。
func TestHandler_Check(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mockStatus     healthdomain.HealthStatus
		mockErr        error
		wantStatusCode int
		wantBody       string
	}{
		{
			name:           "正常系: usecaseがStatusOKを返した場合は200/okを返す",
			mockStatus:     healthdomain.StatusOK,
			mockErr:        nil,
			wantStatusCode: http.StatusOK,
			wantBody:       `{"status":"ok"}`,
		},
		{
			name:           "異常系: usecaseがエラーを返した場合は503を返す",
			mockStatus:     "",
			mockErr:        errors.New("dependency check failed"),
			wantStatusCode: http.StatusServiceUnavailable,
			wantBody:       `{"status":"dependency check failed"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockUsecase := mock_health.NewMockUsecase(ctrl)
			mockUsecase.EXPECT().Check(gomock.Any()).Return(tt.mockStatus, tt.mockErr).Times(1)

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			h := healthhandler.NewHandler(mockUsecase)
			if err := h.Check(c); err != nil {
				t.Fatalf("Check() unexpected error = %v", err)
			}

			if rec.Code != tt.wantStatusCode {
				t.Errorf("status code got = %d, want %d", rec.Code, tt.wantStatusCode)
			}
			gotBody := strings.TrimSpace(rec.Body.String())
			if gotBody != tt.wantBody {
				t.Errorf("body got = %q, want %q", gotBody, tt.wantBody)
			}
		})
	}
}
