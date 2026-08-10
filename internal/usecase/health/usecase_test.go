// Package health_test はヘルスチェックユースケースの外部テストパッケージ。
// 公開 API のみを対象とする。
package health_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	healthdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/health"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/health"
)

// TestUsecase_Check はヘルスチェックユースケースの振る舞いを検証する。
//
// 現状は依存を持たず常に StatusOK を返すためケースは1件だが、
// 依存リソースの疎通確認（StatusDegraded 等）を足した時点で行が増えるよう
// テーブル形式を保つ（testing-principles.md §2）。
func TestUsecase_Check(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		wantStatus healthdomain.HealthStatus
	}{
		{name: "正常系: StatusOK が返る", wantStatus: healthdomain.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := health.NewUsecase().Check(context.Background())

			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, got)
		})
	}
}
