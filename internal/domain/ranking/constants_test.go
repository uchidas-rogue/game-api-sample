// Package ranking_test は ranking ドメイン層の外部テストパッケージ。
package ranking_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ranking "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
)

func TestIsValidScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		score int64
		want  bool
	}{
		{name: "有効: 0（最小値）", score: 0, want: true},
		{name: "有効: MaxScore（最大値）", score: ranking.MaxScore, want: true},
		{name: "有効: 中間値", score: 500000, want: true},
		{name: "無効: 負数", score: -1, want: false},
		{name: "無効: MaxScore+1", score: ranking.MaxScore + 1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ranking.IsValidScore(tt.score))
		})
	}
}

func TestIsValidLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit int
		want  bool
	}{
		{name: "有効: MinRankingLimit", limit: ranking.MinRankingLimit, want: true},
		{name: "有効: MaxRankingLimit", limit: ranking.MaxRankingLimit, want: true},
		{name: "有効: DefaultRankingLimit", limit: ranking.DefaultRankingLimit, want: true},
		{name: "無効: 0", limit: 0, want: false},
		{name: "無効: MaxRankingLimit+1", limit: ranking.MaxRankingLimit + 1, want: false},
		{name: "無効: 負数", limit: -1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ranking.IsValidLimit(tt.limit))
		})
	}
}

func TestNormalizeLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "有効範囲内はそのまま返す", limit: 50, want: 50},
		{name: "MinRankingLimit はそのまま", limit: ranking.MinRankingLimit, want: ranking.MinRankingLimit},
		{name: "MaxRankingLimit はそのまま", limit: ranking.MaxRankingLimit, want: ranking.MaxRankingLimit},
		{name: "0 は DefaultRankingLimit に正規化", limit: 0, want: ranking.DefaultRankingLimit},
		{name: "負数は DefaultRankingLimit に正規化", limit: -100, want: ranking.DefaultRankingLimit},
		{name: "MaxRankingLimit+1 は MaxRankingLimit に丸める", limit: ranking.MaxRankingLimit + 1, want: ranking.MaxRankingLimit},
		{name: "非常に大きな値は MaxRankingLimit に丸める", limit: 99999, want: ranking.MaxRankingLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ranking.NormalizeLimit(tt.limit))
		})
	}
}
