// Package repository_test は OutboxRepository の外部テスト。
package repository_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/repository"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	mocksqlc "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc/mock"
)

func TestOutboxRepository_InsertEvent(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"user_id":1,"guild_id":2,"points":100}`)
	errDB := errors.New("insert failed")

	tests := []struct {
		name      string
		payload   []byte
		stubID    int64
		stubErr   error
		wantID    uint64
		wantErr   bool
		checkJSON bool
	}{
		{
			name:      "正常系: イベント挿入成功",
			payload:   payload,
			stubID:    1,
			wantID:    1,
			checkJSON: true,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			payload: payload,
			stubErr: errDB,
			wantErr: true,
		},
		{
			name:    "異常系: 負の LastInsertId はエラーになる",
			payload: payload,
			stubID:  -1,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().InsertOutboxEvent(gomock.Any(), sqlc.InsertOutboxEventParams{
				EventType: string(outboxdomain.EventTypeRankingScoreAdded),
				Payload:   json.RawMessage(tt.payload),
			}).Return(tt.stubID, tt.stubErr)

			repo := repository.NewOutboxRepositoryWithQuerier(mockQ)
			gotID, err := repo.InsertEvent(context.Background(), dummyTx{}, outboxdomain.EventTypeRankingScoreAdded, tt.payload)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, gotID)
		})
	}
}

func TestOutboxRepository_ListPending(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{"user_id":1,"guild_id":2,"points":100}`)
	errDB := errors.New("query failed")

	tests := []struct {
		name      string
		limit     int32
		stubRows  []sqlc.ListPendingOutboxEventsRow
		stubErr   error
		wantCount int
		wantErr   bool
	}{
		{
			name:  "正常系: 複数件取得・payload 変換確認",
			limit: 10,
			stubRows: []sqlc.ListPendingOutboxEventsRow{
				{
					ID:         1,
					EventType:  string(outboxdomain.EventTypeRankingScoreAdded),
					Payload:    payload,
					RetryCount: 0,
				},
				{
					ID:         2,
					EventType:  string(outboxdomain.EventTypeRankingScoreAdded),
					Payload:    payload,
					RetryCount: 1,
				},
			},
			wantCount: 2,
		},
		{
			name:      "正常系: 空リスト",
			limit:     10,
			stubRows:  []sqlc.ListPendingOutboxEventsRow{},
			wantCount: 0,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			limit:   10,
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().ListPendingOutboxEvents(gomock.Any(), tt.limit).Return(tt.stubRows, tt.stubErr)

			repo := repository.NewOutboxRepositoryWithQuerier(mockQ)
			got, err := repo.ListPending(context.Background(), dummyTx{}, tt.limit)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
			for i, ev := range got {
				assert.Equal(t, tt.stubRows[i].ID, ev.ID)
				assert.Equal(t, outboxdomain.EventType(tt.stubRows[i].EventType), ev.Type)
				assert.Equal(t, []byte(tt.stubRows[i].Payload), ev.Payload)
				assert.Equal(t, tt.stubRows[i].RetryCount, ev.RetryCount)
			}
		})
	}
}

func TestOutboxRepository_ListPending_PayloadRoundtrip(t *testing.T) {
	t.Parallel()

	orig := outboxdomain.RankingScoreAddedPayload{UserID: 10, GuildID: 20, Points: 300}
	b, err := outboxdomain.MarshalRankingScoreAddedPayload(orig)
	require.NoError(t, err)

	ctrl := gomock.NewController(t)
	mockQ := mocksqlc.NewMockQuerier(ctrl)
	mockQ.EXPECT().ListPendingOutboxEvents(gomock.Any(), int32(1)).Return([]sqlc.ListPendingOutboxEventsRow{
		{
			ID:        1,
			EventType: string(outboxdomain.EventTypeRankingScoreAdded),
			Payload:   json.RawMessage(b),
		},
	}, nil)

	repo := repository.NewOutboxRepositoryWithQuerier(mockQ)
	got, err := repo.ListPending(context.Background(), dummyTx{}, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)

	decoded, err := outboxdomain.UnmarshalRankingScoreAddedPayload(got[0].Payload)
	require.NoError(t, err)
	assert.Equal(t, orig, decoded)
}

func TestOutboxRepository_MarkProcessed(t *testing.T) {
	t.Parallel()

	errDB := errors.New("update failed")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{name: "正常系: 処理済みマーク成功"},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().MarkOutboxEventProcessed(gomock.Any(), uint64(1)).Return(tt.stubErr)

			repo := repository.NewOutboxRepositoryWithQuerier(mockQ)
			err := repo.MarkProcessed(context.Background(), dummyTx{}, 1)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestOutboxRepository_GetMaxID(t *testing.T) {
	t.Parallel()

	errDB := errors.New("query failed")

	tests := []struct {
		name    string
		stubID  int64
		stubErr error
		wantID  uint64
		wantErr bool
	}{
		{name: "正常系: 最大ID取得成功", stubID: 100, wantID: 100},
		{name: "正常系: 空テーブルは0を返す", stubID: 0, wantID: 0},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
		{name: "異常系: 負の値はエラーになる", stubID: -1, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetMaxOutboxEventID(gomock.Any()).Return(tt.stubID, tt.stubErr)

			repo := repository.NewOutboxRepositoryWithQuerier(mockQ)
			got, err := repo.GetMaxID(context.Background(), dummyTx{})

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, got)
		})
	}
}

func TestOutboxRepository_MarkProcessedUpTo(t *testing.T) {
	t.Parallel()

	errDB := errors.New("update failed")

	tests := []struct {
		name      string
		maxID     uint64
		eventType outboxdomain.EventType
		stubRows  int64
		stubErr   error
		wantRows  int64
		wantErr   bool
	}{
		{
			name:      "正常系: 複数件処理済みマーク",
			maxID:     10,
			eventType: outboxdomain.EventTypeRankingScoreAdded,
			stubRows:  3,
			wantRows:  3,
		},
		{
			name:      "異常系: DB エラーはラップして返す",
			maxID:     10,
			eventType: outboxdomain.EventTypeRankingScoreAdded,
			stubErr:   errDB,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().MarkOutboxEventsProcessedUpTo(gomock.Any(), sqlc.MarkOutboxEventsProcessedUpToParams{
				MaxID:     tt.maxID,
				EventType: string(tt.eventType),
			}).Return(tt.stubRows, tt.stubErr)

			repo := repository.NewOutboxRepositoryWithQuerier(mockQ)
			got, err := repo.MarkProcessedUpTo(context.Background(), dummyTx{}, tt.maxID, tt.eventType)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantRows, got)
		})
	}
}

func TestOutboxRepository_IncrementRetry(t *testing.T) {
	t.Parallel()

	errDB := errors.New("update failed")

	tests := []struct {
		name      string
		id        uint64
		lastError string
		stubErr   error
		wantErr   bool
	}{
		{
			name:      "正常系: リトライカウント増加成功",
			id:        1,
			lastError: "timeout",
		},
		{
			name:      "正常系: 空文字 lastError は NullString.Valid=false になる",
			id:        1,
			lastError: "",
		},
		{
			name:      "異常系: DB エラーはラップして返す",
			id:        1,
			lastError: "err",
			stubErr:   errDB,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().IncrementOutboxEventRetry(gomock.Any(), sqlc.IncrementOutboxEventRetryParams{
				ID:        tt.id,
				LastError: sql.NullString{String: tt.lastError, Valid: tt.lastError != ""},
			}).Return(tt.stubErr)

			repo := repository.NewOutboxRepositoryWithQuerier(mockQ)
			err := repo.IncrementRetry(context.Background(), dummyTx{}, tt.id, tt.lastError)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
