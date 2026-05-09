// Package gacha_test は gacha ドメイン層の外部テストパッケージ。
package gacha_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	gacha "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
)

func TestIsValidPullCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pullCount int
		want      bool
	}{
		{name: fmt.Sprintf("有効: MinPullCount（%d）", gacha.MinPullCount), pullCount: gacha.MinPullCount, want: true},
		{name: fmt.Sprintf("有効: MaxPullCount（%d）", gacha.MaxPullCount), pullCount: gacha.MaxPullCount, want: true},
		{name: "有効: 中間値（5）", pullCount: 5, want: true},
		{name: fmt.Sprintf("無効: %d（最小値未満）", gacha.MinPullCount-1), pullCount: gacha.MinPullCount - 1, want: false},
		{name: fmt.Sprintf("無効: MaxPullCount+1（%d）", gacha.MaxPullCount+1), pullCount: gacha.MaxPullCount + 1, want: false},
		{name: "無効: 負数", pullCount: -1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, gacha.IsValidPullCount(tt.pullCount))
		})
	}
}

func TestGemCostFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pullCount int
		want      int
	}{
		{name: "0回: 費用0", pullCount: 0, want: 0},
		{name: fmt.Sprintf("MinPullCount(%d)回: GemCostPerPull×%d", gacha.MinPullCount, gacha.MinPullCount), pullCount: gacha.MinPullCount, want: gacha.GemCostPerPull * gacha.MinPullCount},
		{name: fmt.Sprintf("MaxPullCount(%d)回: GemCostPerPull×%d", gacha.MaxPullCount, gacha.MaxPullCount), pullCount: gacha.MaxPullCount, want: gacha.GemCostPerPull * gacha.MaxPullCount},
		{name: "負値: 負の費用（仕様外入力の計算確認）", pullCount: -1, want: gacha.GemCostPerPull * -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, gacha.GemCostFor(tt.pullCount))
		})
	}
}

func TestUser_HasEnoughGemsFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		gemNum    int
		pullCount int
		want      bool
	}{
		{
			name:      fmt.Sprintf("石が丁度足りる（同値, MaxPullCount=%d）", gacha.MaxPullCount),
			gemNum:    gacha.GemCostPerPull * gacha.MaxPullCount,
			pullCount: gacha.MaxPullCount,
			want:      true,
		},
		{
			name:      fmt.Sprintf("石が余っている（過剰, MaxPullCount=%d）", gacha.MaxPullCount),
			gemNum:    gacha.GemCostPerPull*gacha.MaxPullCount + 1,
			pullCount: gacha.MaxPullCount,
			want:      true,
		},
		{
			name:      fmt.Sprintf("石が1足りない（不足, MaxPullCount=%d）", gacha.MaxPullCount),
			gemNum:    gacha.GemCostPerPull*gacha.MaxPullCount - 1,
			pullCount: gacha.MaxPullCount,
			want:      false,
		},
		{
			name:      fmt.Sprintf("石が0で MinPullCount(%d) 回引く", gacha.MinPullCount),
			gemNum:    0,
			pullCount: gacha.MinPullCount,
			want:      false,
		},
		{
			name:      "0回引く: 費用0なので常に真",
			gemNum:    0,
			pullCount: 0,
			want:      true,
		},
		{
			name:      fmt.Sprintf("負の石所持で MinPullCount(%d) 回引く", gacha.MinPullCount),
			gemNum:    -1,
			pullCount: gacha.MinPullCount,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			u := gacha.User{GemNum: tt.gemNum}
			assert.Equal(t, tt.want, u.HasEnoughGemsFor(tt.pullCount))
		})
	}
}
