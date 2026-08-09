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
	b := outboxdomain.MarshalRankingScoreAddedPayload(orig)

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

func TestOutboxRepository_ClaimByID(t *testing.T) {
	t.Parallel()

	payload := json.RawMessage(`{"user_id":1,"guild_id":2,"points":100}`)
	errDB := errors.New("claim failed")

	tests := []struct {
		name        string
		id          uint64
		stubRow     sqlc.ClaimPendingOutboxEventByIDRow
		stubErr     error
		wantFound   bool
		wantErr     bool
		checkResult func(t *testing.T, ev outboxdomain.Event)
	}{
		{
			name: "正常系: 指定 ID の未処理イベントを確保する",
			id:   1,
			stubRow: sqlc.ClaimPendingOutboxEventByIDRow{
				ID:         1,
				EventType:  string(outboxdomain.EventTypeRankingScoreAdded),
				Payload:    payload,
				RetryCount: 2,
			},
			wantFound: true,
			checkResult: func(t *testing.T, ev outboxdomain.Event) {
				t.Helper()
				assert.Equal(t, uint64(1), ev.ID)
				assert.Equal(t, outboxdomain.EventTypeRankingScoreAdded, ev.Type)
				assert.Equal(t, []byte(payload), ev.Payload)
				assert.Equal(t, uint32(2), ev.RetryCount)
			},
		},
		{
			name:      "正常系: 該当なし（sql.ErrNoRows）は found=false かつ err=nil",
			id:        2,
			stubErr:   sql.ErrNoRows,
			wantFound: false,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			id:      3,
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().ClaimPendingOutboxEventByID(gomock.Any(), tt.id).Return(tt.stubRow, tt.stubErr)

			repo := repository.NewOutboxRepositoryWithQuerier(mockQ)
			ev, found, err := repo.ClaimByID(context.Background(), dummyTx{}, tt.id)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				assert.False(t, found)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantFound, found)
			if tt.checkResult != nil {
				tt.checkResult(t, ev)
			}
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
