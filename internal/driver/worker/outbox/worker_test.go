package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockoutbox "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox/mock"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
	mockshared "github.com/uchidas-rogue/game-api-sample/internal/usecase/shared/mock"
	workeroutbox "github.com/uchidas-rogue/game-api-sample/internal/driver/worker/outbox"
)

// invokeDoInTx は MockTransactor.DoInTx を fn(nil) で実行するヘルパー。
func invokeDoInTx(tx *mockshared.MockTransactor, times int) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
			return fn(nil)
		}).
		Times(times)
}

func mustMarshalRankingScoreAdded(t *testing.T, p outboxdomain.RankingScoreAddedPayload) []byte {
	t.Helper()
	return outboxdomain.MarshalRankingScoreAddedPayload(p)
}

// TestWorker_runOnce_dispatch は1回のティック内でイベントが正しく dispatch されることを確認する。
func TestWorker_runOnce_dispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker
	}{
		{
			name: "正常系: ranking_score_added を Redis に反映して MarkProcessed",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 10, GuildID: 1, Points: 500,
				})
				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{
						{ID: 1, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload},
					}, nil)
				store.EXPECT().IncrementUserPoints(gomock.Any(), int64(10), int64(500)).Return(nil)
				store.EXPECT().IncrementGuildScore(gomock.Any(), int64(1), int64(500)).Return(nil)
				repo.EXPECT().MarkProcessed(gomock.Any(), gomock.Any(), uint64(1)).Return(nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "異常系: Redis 反映失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 10, GuildID: 1, Points: 500,
				})
				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{
						{ID: 7, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload},
					}, nil)
				store.EXPECT().IncrementUserPoints(gomock.Any(), int64(10), int64(500)).
					Return(errors.New("redis down"))
				repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(7), gomock.Any()).Return(nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "異常系: 未知の event_type は IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{
						{ID: 99, Type: "unknown_event", Payload: []byte("{}")},
					}, nil)
				repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(99), gomock.Any()).Return(nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "異常系: IncrementUserPoints 成功 + IncrementGuildScore 失敗で IncrementRetry",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				payload := mustMarshalRankingScoreAdded(t, outboxdomain.RankingScoreAddedPayload{
					UserID: 11, GuildID: 2, Points: 300,
				})
				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{
						{ID: 8, Type: outboxdomain.EventTypeRankingScoreAdded, Payload: payload},
					}, nil)
				store.EXPECT().IncrementUserPoints(gomock.Any(), int64(11), int64(300)).Return(nil)
				store.EXPECT().IncrementGuildScore(gomock.Any(), int64(2), int64(300)).
					Return(errors.New("redis guild down"))
				repo.EXPECT().IncrementRetry(gomock.Any(), gomock.Any(), uint64(8), gomock.Any()).Return(nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "異常系: ListPending エラーは IncrementRetry/MarkProcessed を呼ばずに戻る",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, errors.New("db down"))

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
		{
			name: "正常系: ListPending が空なら何も呼ばない",
			setup: func(t *testing.T, ctrl *gomock.Controller) *workeroutbox.Worker {
				t.Helper()
				repo := mockoutbox.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				invokeDoInTx(tx, 1)

				repo.EXPECT().ListPending(gomock.Any(), gomock.Any(), gomock.Any()).
					Return([]outboxdomain.Event{}, nil)

				return workeroutbox.New(workeroutbox.Config{
					Repo: repo, RankingStore: store, Tx: tx,
					Logger: slogtest.NewLogger(t, nil), PollInterval: 10 * time.Millisecond, BatchSize: 100,
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			w := tt.setup(t, ctrl)

			// Run を即時 ctx キャンセルで止め、初回 runOnce のみ実行されることを利用して検証する。
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := w.Run(ctx)
			assert.NoError(t, err)
		})
	}
}
