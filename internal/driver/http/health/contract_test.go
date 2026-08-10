package health_test

import (
	"errors"
	"testing"

	"go.uber.org/mock/gomock"

	healthdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/health"
	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/health"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/apicontract"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockhealth "github.com/uchidas-rogue/game-api-sample/internal/usecase/health/mock"
)

// TestResponseContract はレスポンスの **構造** が契約ファイルと一致することを検証する。
// 値の妥当性は TestHandler_Check の責務なので、ここでは見ない。
//
// 正常系と異常系で構造が同一（status 1 キー）のため、契約ファイルは 1 つで足りる。
func TestResponseContract(t *testing.T) {
	t.Parallel()

	const contractHealth = "../testdata/contracts/health.json"

	tests := []struct {
		name       string
		mockStatus healthdomain.HealthStatus
		mockErr    error
	}{
		// #3
		{name: "正常系のレスポンス構造", mockStatus: healthdomain.StatusOK},
		{name: "異常系のレスポンス構造", mockErr: errors.New("boom")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc := mockhealth.NewMockUsecase(ctrl)
			uc.EXPECT().Check(gomock.Any()).Return(tt.mockStatus, tt.mockErr)

			h := healthhandler.NewHandler(uc, slogtest.NewLogger(t, nil))
			rec := recordCheck(t, h)

			apicontract.AssertStructure(t, contractHealth, rec.Body.Bytes())
		})
	}
}
