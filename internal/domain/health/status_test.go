package health_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/uchidas-rogue/game-api-sample/internal/domain/health"
)

func TestHealthStatus_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   health.HealthStatus
		want string
	}{
		{name: "ok", in: health.StatusOK, want: "ok"},
		{name: "degraded", in: health.StatusDegraded, want: "degraded"},
		{name: "down", in: health.StatusDown, want: "down"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.in.String())
		})
	}
}
