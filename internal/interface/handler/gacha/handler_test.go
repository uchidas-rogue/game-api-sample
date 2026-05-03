package gacha_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	gachahandler "github.com/uchidas-rogue/game-api-sample/internal/interface/handler/gacha"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	gachausecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
	mockuc "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha/mock"
)

// TestHandler_Multi はマルチガチャHTTPハンドラの振る舞いを検証する。
func TestHandler_Multi(t *testing.T) {
	t.Parallel()

	const path = "/users/:userID/gacha/multi"

	tests := []struct {
		name           string
		paramUserID    string
		body           string
		setupMock      func(m *mockuc.MockUsecase)
		wantStatusCode int
		wantBodyPart   string
	}{
		{
			name:        "正常系: マルチ抽選成功",
			paramUserID: "1",
			body:        `{"pull_count":10}`,
			setupMock: func(m *mockuc.MockUsecase) {
				m.EXPECT().Multi(gomock.Any(), int64(1), 10).Return(gachausecase.Result{
					UserID:        1,
					RemainingGems: 500,
					DrawnItems: []gachadomain.Item{
						{ID: 10, Name: "potion", Rarity: gachadomain.RarityN},
					},
				}, nil).Times(1)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"user_id":1`,
		},
		{
			name:        "正常系: pull_count=1 でも成功",
			paramUserID: "1",
			body:        `{"pull_count":1}`,
			setupMock: func(m *mockuc.MockUsecase) {
				m.EXPECT().Multi(gomock.Any(), int64(1), 1).Return(gachausecase.Result{
					UserID:        1,
					RemainingGems: 900,
					DrawnItems:    []gachadomain.Item{{ID: 10, Name: "potion", Rarity: gachadomain.RarityN}},
				}, nil).Times(1)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"user_id":1`,
		},
		{
			name:           "異常系: userIDが数値でない",
			paramUserID:    "abc",
			body:           `{"pull_count":10}`,
			setupMock:      func(_ *mockuc.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid userID"`,
		},
		{
			name:           "異常系: userIDが0以下",
			paramUserID:    "0",
			body:           `{"pull_count":10}`,
			setupMock:      func(_ *mockuc.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid userID"`,
		},
		{
			name:           "異常系: pull_count が 0 はバリデーションエラー",
			paramUserID:    "1",
			body:           `{"pull_count":0}`,
			setupMock:      func(_ *mockuc.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"pull_count must be between 1 and 10"`,
		},
		{
			name:           "異常系: pull_count が 11 はバリデーションエラー",
			paramUserID:    "1",
			body:           `{"pull_count":11}`,
			setupMock:      func(_ *mockuc.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"pull_count must be between 1 and 10"`,
		},
		{
			name:           "異常系: pull_count が負数はバリデーションエラー",
			paramUserID:    "1",
			body:           `{"pull_count":-1}`,
			setupMock:      func(_ *mockuc.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"pull_count must be between 1 and 10"`,
		},
		{
			name:           "異常系: 不正なJSONボディ",
			paramUserID:    "1",
			body:           `{`,
			setupMock:      func(_ *mockuc.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid request body"`,
		},
		{
			name:        "異常系: ユーザー未存在は404を返す",
			paramUserID: "1",
			body:        `{"pull_count":10}`,
			setupMock: func(m *mockuc.MockUsecase) {
				m.EXPECT().Multi(gomock.Any(), int64(1), 10).
					Return(gachausecase.Result{}, gachadomain.ErrUserNotFound).Times(1)
			},
			wantStatusCode: http.StatusNotFound,
			wantBodyPart:   `"message":"user not found"`,
		},
		{
			name:        "異常系: 石不足は402を返す",
			paramUserID: "1",
			body:        `{"pull_count":10}`,
			setupMock: func(m *mockuc.MockUsecase) {
				m.EXPECT().Multi(gomock.Any(), int64(1), 10).
					Return(gachausecase.Result{}, gachadomain.ErrInsufficientGems).Times(1)
			},
			wantStatusCode: http.StatusPaymentRequired,
			wantBodyPart:   `"message":"insufficient gems"`,
		},
		{
			name:        "異常系: アイテム未登録は503を返す",
			paramUserID: "1",
			body:        `{"pull_count":10}`,
			setupMock: func(m *mockuc.MockUsecase) {
				m.EXPECT().Multi(gomock.Any(), int64(1), 10).
					Return(gachausecase.Result{}, gachadomain.ErrNoItemsAvailable).Times(1)
			},
			wantStatusCode: http.StatusServiceUnavailable,
			wantBodyPart:   `"message":"gacha is unavailable"`,
		},
		{
			name:        "異常系: 重み不正は503を返す",
			paramUserID: "1",
			body:        `{"pull_count":10}`,
			setupMock: func(m *mockuc.MockUsecase) {
				m.EXPECT().Multi(gomock.Any(), int64(1), 10).
					Return(gachausecase.Result{}, gachadomain.ErrInvalidItemWeights).Times(1)
			},
			wantStatusCode: http.StatusServiceUnavailable,
			wantBodyPart:   `"message":"gacha is unavailable"`,
		},
		{
			name:        "異常系: 予期せぬエラーは500を返し詳細を漏らさない",
			paramUserID: "1",
			body:        `{"pull_count":10}`,
			setupMock: func(m *mockuc.MockUsecase) {
				m.EXPECT().Multi(gomock.Any(), int64(1), 10).
					Return(gachausecase.Result{}, errors.New("db connection lost")).Times(1)
			},
			wantStatusCode: http.StatusInternalServerError,
			wantBodyPart:   `"message":"internal server error"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockUC := mockuc.NewMockUsecase(ctrl)
			tt.setupMock(mockUC)

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath(path)
			c.SetParamNames("userID")
			c.SetParamValues(tt.paramUserID)

			h := gachahandler.NewHandler(mockUC, slogtest.NewLogger(t, nil))
			require.NoError(t, h.Multi(c))

			assert.Equal(t, tt.wantStatusCode, rec.Code)
			assert.Contains(t, strings.TrimSpace(rec.Body.String()), tt.wantBodyPart)
		})
	}
}
