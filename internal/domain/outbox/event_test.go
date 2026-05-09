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

func TestMarshalRankingScoreAddedPayload_KeyNames(t *testing.T) {
	t.Parallel()

	payload := outbox.RankingScoreAddedPayload{UserID: 10, GuildID: 20, Points: 300}
	b := outbox.MarshalRankingScoreAddedPayload(payload)

	jsonStr := string(b)
	// JSON キー名の検証（スネークケース）。
	assert.Contains(t, jsonStr, `"user_id"`)
	assert.Contains(t, jsonStr, `"guild_id"`)
	assert.Contains(t, jsonStr, `"points"`)
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
			name:    "異常系: 空バイト列",
			input:   []byte(``),
			wantErr: true,
			errMsg:  "unmarshal ranking score added payload",
		},
		{
			name:  "部分欠落: user_idのみ指定、他はゼロ値",
			input: []byte(`{"user_id":99}`),
			want:  outbox.RankingScoreAddedPayload{UserID: 99, GuildID: 0, Points: 0},
		},
		{
			name:  "部分欠落: 全フィールド欠落はゼロ値",
			input: []byte(`{}`),
			want:  outbox.RankingScoreAddedPayload{},
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

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	t.Parallel()

	original := outbox.RankingScoreAddedPayload{UserID: 42, GuildID: 7, Points: 9999}

	b := outbox.MarshalRankingScoreAddedPayload(original)

	got, err := outbox.UnmarshalRankingScoreAddedPayload(b)
	require.NoError(t, err)

	assert.Equal(t, original, got)
}
