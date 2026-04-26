package health

import (
	"context"
	"testing"

	healthdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/health"
)

// TestUsecase_Check はヘルスチェックユースケースの振る舞いを検証する。
func TestUsecase_Check(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wantStatus healthdomain.HealthStatus
		wantErr    bool
	}{
		{
			name:       "正常系: StatusOKが返ること",
			wantStatus: healthdomain.StatusOK,
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u := NewUsecase()
			got, err := u.Check(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Check() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.wantStatus {
				t.Errorf("Check() got = %v, want %v", got, tt.wantStatus)
			}
		})
	}
}
