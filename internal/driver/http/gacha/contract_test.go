package gacha_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	gachahandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/gacha"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/apicontract"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	gachausecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
	mockuc "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha/mock"
)

// 契約ファイル（レスポンス構造の正本）へのパス。
const (
	contractMulti = "../testdata/contracts/gacha_multi.json"
	contractError = "../testdata/contracts/error.json"
)

// TestResponseContract はレスポンスの **構造** が契約ファイルと一致することを検証する。
// 値の妥当性は TestHandler_Multi の責務なので、ここでは見ない。
//
// docs/testing/http-gacha.md「2. レスポンス契約」の表と 1 対 1 で対応する。
func TestResponseContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		contractPath string
		// invalidBody が true なら入力バリデーションで弾かれる経路を通す。
		invalidBody bool
		wantStatus  int
	}{
		// #12
		{name: "マルチガチャ成功", contractPath: contractMulti, wantStatus: http.StatusOK},
		// #13
		{name: "エラーレスポンス", contractPath: contractError, invalidBody: true, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc := mockuc.NewMockUsecase(ctrl)
			body := `{"pull_count":10}`
			if tt.invalidBody {
				body = `{"pull_count":"not a number"}`
			} else {
				uc.EXPECT().Multi(gomock.Any(), int64(1), 10).Return(gachausecase.Result{
					UserID: 1,
					DrawnItems: []gachadomain.Item{
						{ID: 1, Name: "ノーマルソード", Rarity: gachadomain.RarityN},
					},
					RemainingGems: 500,
				}, nil)
			}

			h := gachahandler.NewHandler(uc, slogtest.NewLogger(t, nil))
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/users/1/gacha/multi", strings.NewReader(body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetParamNames("userID")
			c.SetParamValues("1")

			require.NoError(t, h.Multi(c))
			require.Equal(t, tt.wantStatus, rec.Code, "契約検証の前提: 想定したステータスであること body=%s", rec.Body.String())
			apicontract.AssertStructure(t, tt.contractPath, rec.Body.Bytes())
		})
	}
}
