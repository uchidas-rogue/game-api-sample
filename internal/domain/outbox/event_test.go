// Package outbox_test は outbox ドメイン層の外部テストパッケージ。
package outbox_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
)

func TestMarshalRankingScoreAddedPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload outbox.RankingScoreAddedPayload
		// wantJSON は assert.JSONEq で比較するため、キー順不同でも可。
		wantJSON string
	}{
		{
			name:     "正常系: 全フィールドあり",
			payload:  outbox.RankingScoreAddedPayload{UserID: 1, GuildID: 2, Points: 100},
			wantJSON: `{"user_id":1,"guild_id":2,"points":100}`,
		},
		{
			name:     "正常系: ゼロ値フィールド",
			payload:  outbox.RankingScoreAddedPayload{},
			wantJSON: `{"user_id":0,"guild_id":0,"points":0}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := outbox.MarshalRankingScoreAddedPayload(tt.payload)
			assert.JSONEq(t, tt.wantJSON, string(b))
		})
	}
}

func TestUnmarshalRankingScoreAddedPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   []byte
		want    outbox.RankingScoreAddedPayload
		wantErr bool
		errMsg  string
	}{
		{
			name:  "正常系: 全フィールドあり",
			input: []byte(`{"user_id":1,"guild_id":2,"points":100}`),
			want:  outbox.RankingScoreAddedPayload{UserID: 1, GuildID: 2, Points: 100},
		},
		{
			name:    "異常系: 不正JSON",
			input:   []byte(`{invalid json`),
			wantErr: true,
			errMsg:  "unmarshal ranking score added payload",
		},
		{
			// 不正JSONと同じ分岐だが独立させている。payload は DB のカラム由来で
			// 空になりうるため、「空を黙ってゼロ値として受理する」ガードが
			// 足された場合にここだけが落ちる（§7 の基準3）。
			// ゼロ値で受理されると、worker が 0 点のイベントを正常適用してしまう。
			name:    "異常系: 空バイト列",
			input:   []byte(``),
			wantErr: true,
			errMsg:  "unmarshal ranking score added payload",
		},
		{
			// 未指定フィールドがゼロ値になることの確認。`{}`（全欠落）は
			// 同じ分岐・同じ出力で、このケースが検出できない実装ミスも無いため統合した（§7）。
			name:  "部分欠落: 未指定フィールドはゼロ値になる",
			input: []byte(`{"user_id":99}`),
			want:  outbox.RankingScoreAddedPayload{UserID: 99, GuildID: 0, Points: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := outbox.UnmarshalRankingScoreAddedPayload(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.errMsg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
