// Package ranking_test は ranking ユースケースの外部テストパッケージ。
// 公開 API のみを対象とし、モックを用いてデータベース・Redis 接続をバイパスする。
package ranking_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	mockoutbox "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
	mockshared "github.com/uchidas-rogue/game-api-sample/internal/usecase/shared/mock"
)

// newDoInTxCaller は MockTransactor.DoInTx を実際に fn(nil) を呼び出す DoAndReturn として設定するヘルパー。
func newDoInTxCaller(tx *mockshared.MockTransactor) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
			return fn(nil)
		})
}

// TestGetGuildRankings_正常系_異常系 はギルドランキング取得のユースケースを検証する。
func TestGetGuildRankings_正常系_異常系(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       ranking.GetRankingsInput
		setup       func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase
		wantErr     bool
		checkResult func(t *testing.T, r ranking.RankingsResult)
	}{
		{
			name:  "正常系: ランキング取得成功",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				entries := []rankingdomain.RankEntry{
					{Rank: 1, ID: 1, Score: 9000},
					{Rank: 2, ID: 2, Score: 7000},
				}
				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).Return(entries, nil)
				repo.EXPECT().ListGuildsByIDs(gomock.Any(), gomock.Any(), []int64{1, 2}).
					Return(map[int64]rankingdomain.Guild{
						1: {ID: 1, Name: "ギルドA"},
						2: {ID: 2, Name: "ギルドB"},
					}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(2), nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r ranking.RankingsResult) {
				t.Helper()
				assert.Equal(t, int64(2), r.TotalCount)
				require.Len(t, r.Rankings, 2)
				assert.Equal(t, "ギルドA", r.Rankings[0].Name)
				assert.Equal(t, "ギルドB", r.Rankings[1].Name)
			},
		},
		{
			name:  "正常系: エントリーが空の場合は ListGuildsByIDs を呼ばない",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r ranking.RankingsResult) {
				t.Helper()
				assert.Equal(t, int64(0), r.TotalCount)
				assert.Empty(t, r.Rankings)
			},
		},
		{
			name:  "エッジケース: limit=0 は DefaultRankingLimit に正規化される",
			input: ranking.GetRankingsInput{Limit: 0, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				// NormalizeLimit(0) = 10 (DefaultRankingLimit)
				store.EXPECT().GetGuildRankings(gomock.Any(), 0, rankingdomain.DefaultRankingLimit).
					Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
		{
			name:  "エッジケース: limit が MaxRankingLimit を超える場合は上限に丸められる",
			input: ranking.GetRankingsInput{Limit: 9999, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				store.EXPECT().GetGuildRankings(gomock.Any(), 0, rankingdomain.MaxRankingLimit).
					Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
		{
			name:  "エッジケース: offset が負数の場合は 0 に丸められる",
			input: ranking.GetRankingsInput{Limit: 10, Offset: -5},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).
					Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
		{
			name:  "異常系: GetGuildRankings(store) がエラー",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				errRedis := errors.New("redis error")
				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).Return(nil, errRedis)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: ListGuildsByIDs がエラー",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				errDB := errors.New("db error")
				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).
					Return([]rankingdomain.RankEntry{{Rank: 1, ID: 1}}, nil)
				repo.EXPECT().ListGuildsByIDs(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, errDB)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: GetGuildTotalCount がエラー",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				errRedis := errors.New("redis error")
				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc := tt.setup(t, ctrl)

			result, err := uc.GetGuildRankings(context.Background(), tt.input)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, ranking.RankingsResult{}, result)
			} else {
				require.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

// TestGetGuildRank_正常系_異常系 はギルド個別順位取得のユースケースを検証する。
func TestGetGuildRank_正常系_異常系(t *testing.T) {
	t.Parallel()

	const guildID = int64(1)

	tests := []struct {
		name        string
		guildID     int64
		setup       func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase
		wantErr     bool
		checkErr    func(t *testing.T, err error)
		checkResult func(t *testing.T, r rankingdomain.GuildRankResult)
	}{
		{
			name:    "正常系: ギルド順位取得成功",
			guildID: guildID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID, Name: "テストギルド"}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(9000), true, nil)
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(1), nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).
					Return(int64(10), nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.GuildRankResult) {
				t.Helper()
				assert.Equal(t, guildID, r.GuildID)
				assert.Equal(t, "テストギルド", r.GuildName)
				assert.Equal(t, int64(9000), r.Score)
				assert.Equal(t, int64(1), r.Rank)
				assert.Equal(t, int64(10), r.TotalGuilds)
			},
		},
		{
			name:    "異常系: GetGuild がエラー",
			guildID: guildID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{}, rankingdomain.ErrGuildNotFound)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrGuildNotFound)
			},
		},
		{
			name:    "異常系: スコア未登録で ErrScoreNotFound",
			guildID: guildID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(0), false, nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrScoreNotFound)
			},
		},
		{
			name:    "異常系: GetGuildScore(store) がエラー",
			guildID: guildID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				errRedis := errors.New("redis error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(0), false, errRedis)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:    "異常系: GetGuildRank(store) がエラー",
			guildID: guildID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				errRedis := errors.New("redis error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(9000), true, nil)
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:    "異常系: GetGuildTotalCount がエラー",
			guildID: guildID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				errRedis := errors.New("redis error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(9000), true, nil)
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(1), nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).
					Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc := tt.setup(t, ctrl)

			result, err := uc.GetGuildRank(context.Background(), tt.guildID)

			if tt.wantErr {
				require.Error(t, err)
				if tt.checkErr != nil {
					tt.checkErr(t, err)
				}
				assert.Equal(t, rankingdomain.GuildRankResult{}, result)
			} else {
				require.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

// expectOutboxRankingScoreAdded は AddUserPoints 成功経路で発行される outbox InsertEvent
// 呼び出しを検証する EXPECT を登録する。payload は JSON でシリアライズされた
// RankingScoreAddedPayload と一致することを確認する。
func expectOutboxRankingScoreAdded(t *testing.T, repo *mockoutbox.MockRepository, userID, guildID, points int64) {
	t.Helper()
	repo.EXPECT().
		InsertEvent(gomock.Any(), gomock.Any(), outboxdomain.EventTypeRankingScoreAdded, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ shared.Tx, _ outboxdomain.EventType, payload []byte) (uint64, error) {
			p, err := outboxdomain.UnmarshalRankingScoreAddedPayload(payload)
			require.NoError(t, err)
			assert.Equal(t, userID, p.UserID)
			assert.Equal(t, guildID, p.GuildID)
			assert.Equal(t, points, p.Points)
			return 1, nil
		})
}

// TestAddUserPoints_正常系_異常系 はユーザーポイント加算のユースケースを検証する。
// MySQL 更新と outbox 登録を同一トランザクション内で実行することを確認する。
// Redis 反映は worker が非同期に行うため、本テストでは rankingStore への書き込みは
// 一切呼び出されないことを暗黙的に検証する（モックに EXPECT を仕掛けないことで担保）。
func TestAddUserPoints_正常系_異常系(t *testing.T) {
	t.Parallel()

	const (
		userID  = int64(10)
		guildID = int64(1)
		points  = int64(500)
		reason  = "クエストクリア報酬"
	)

	tests := []struct {
		name        string
		input       ranking.AddUserPointsInput
		setup       func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase
		wantErr     bool
		checkErr    func(t *testing.T, err error)
		checkResult func(t *testing.T, r rankingdomain.UserPointAddResult)
	}{
		{
			name:  "正常系: 既存ポイント・ギルドスコアありで加算成功",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				const prevPoints = int64(1000)
				const prevGuildScore = int64(5000)
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{UserID: userID, Points: prevPoints}, nil)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{GuildID: guildID, Score: prevGuildScore}, nil)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, points).
					Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).
					Return(nil)
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, points).
					Return(nil)
				expectOutboxRankingScoreAdded(t, outboxRepo, userID, guildID, points)
				notifier.EXPECT().Notify(gomock.Any()).Return(nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.UserPointAddResult) {
				t.Helper()
				assert.Equal(t, userID, r.UserID)
				assert.Equal(t, points, r.Points)
				assert.Equal(t, int64(1000), r.PreviousTotal)
				assert.Equal(t, int64(1500), r.NewTotal)
				assert.Equal(t, guildID, r.GuildID)
				assert.Equal(t, int64(5000), r.GuildPreviousTotal)
				assert.Equal(t, int64(5500), r.GuildNewTotal)
			},
		},
		{
			name:  "正常系: 初回ユーザー（ErrPointsNotFound）かつ初回ギルド（ErrScoreNotFound）でも加算成功",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, points).
					Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).
					Return(nil)
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, points).
					Return(nil)
				expectOutboxRankingScoreAdded(t, outboxRepo, userID, guildID, points)
				notifier.EXPECT().Notify(gomock.Any()).Return(nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.UserPointAddResult) {
				t.Helper()
				assert.Equal(t, userID, r.UserID)
				assert.Equal(t, points, r.Points)
				assert.Equal(t, int64(0), r.PreviousTotal)
				assert.Equal(t, points, r.NewTotal)
				assert.Equal(t, guildID, r.GuildID)
				assert.Equal(t, int64(0), r.GuildPreviousTotal)
				assert.Equal(t, points, r.GuildNewTotal)
			},
		},
		{
			name:  "境界値: Points=MaxScore でも加算成功",
			input: ranking.AddUserPointsInput{UserID: userID, Points: rankingdomain.MaxScore, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				maxPts := int64(rankingdomain.MaxScore)
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, maxPts, reason).Return(nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, maxPts).Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, maxPts).Return(nil)
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, maxPts).Return(nil)
				expectOutboxRankingScoreAdded(t, outboxRepo, userID, guildID, maxPts)
				notifier.EXPECT().Notify(gomock.Any()).Return(nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.UserPointAddResult) {
				t.Helper()
				assert.Equal(t, int64(rankingdomain.MaxScore), r.Points)
				assert.Equal(t, int64(rankingdomain.MaxScore), r.NewTotal)
			},
		},
		{
			name:  "正常系: outbox 通知が失敗してもリクエストは成功する（ポーリングがフォールバック）",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{UserID: userID, Points: 0}, nil)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{GuildID: guildID, Score: 0}, nil)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).Return(nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, points).Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).Return(nil)
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, points).Return(nil)
				expectOutboxRankingScoreAdded(t, outboxRepo, userID, guildID, points)
				notifier.EXPECT().Notify(gomock.Any()).Return(errors.New("redis down"))

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.UserPointAddResult) {
				t.Helper()
				assert.Equal(t, userID, r.UserID)
				assert.Equal(t, points, r.NewTotal)
			},
		},
		{
			name:  "異常系: ポイントが負数で ErrInvalidPoints（IsValidScore=false）",
			input: ranking.AddUserPointsInput{UserID: userID, Points: -1, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrInvalidPoints)
			},
		},
		{
			name:  "異常系: ポイントが最大値超過で ErrInvalidPoints（IsValidScore=false）",
			input: ranking.AddUserPointsInput{UserID: userID, Points: int64(rankingdomain.MaxScore) + 1, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrInvalidPoints)
			},
		},
		{
			name:  "異常系: ユーザー不在（ErrUserNotFound）",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("", rankingdomain.ErrUserNotFound)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrUserNotFound)
			},
		},
		{
			name:  "異常系: ギルド未所属（ErrUserNotInGuild）",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(int64(0), rankingdomain.ErrUserNotInGuild)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrUserNotInGuild)
			},
		},
		{
			name:  "異常系: GetUserPoints が予期せぬエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, errDB)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: GetGuildScore が予期せぬエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, errDB)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: InsertUserPointHistory がエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(errDB)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: InsertGuildScoreHistory がエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, points).
					Return(errDB)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: IncrementUserPoints(repo) がエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, points).
					Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).
					Return(errDB)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: IncrementGuildScore(repo) がエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, points).
					Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).
					Return(nil)
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, points).
					Return(errDB)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: outbox InsertEvent がエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)
				newDoInTxCaller(tx)

				errOutbox := errors.New("outbox insert error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), userID).
					Return(guildID, nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, points).
					Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).
					Return(nil)
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, points).
					Return(nil)
				outboxRepo.EXPECT().
					InsertEvent(gomock.Any(), gomock.Any(), outboxdomain.EventTypeRankingScoreAdded, gomock.Any()).
					Return(uint64(0), errOutbox)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc := tt.setup(t, ctrl)

			result, err := uc.AddUserPoints(context.Background(), tt.input)

			if tt.wantErr {
				require.Error(t, err)
				if tt.checkErr != nil {
					tt.checkErr(t, err)
				}
				assert.Equal(t, rankingdomain.UserPointAddResult{}, result)
			} else {
				require.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

// TestGetUserRankings_正常系_異常系 はユーザーランキング取得のユースケースを検証する。
func TestGetUserRankings_正常系_異常系(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       ranking.GetRankingsInput
		setup       func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase
		wantErr     bool
		checkResult func(t *testing.T, r ranking.RankingsResult)
	}{
		{
			name:  "正常系: ユーザーランキング取得成功",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				entries := []rankingdomain.RankEntry{
					{Rank: 1, ID: 1, Score: 8000},
					{Rank: 2, ID: 2, Score: 6000},
				}
				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).Return(entries, nil)
				repo.EXPECT().ListUsersByIDs(gomock.Any(), gomock.Any(), []int64{1, 2}).
					Return(map[int64]string{1: "ユーザーA", 2: "ユーザーB"}, nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).Return(int64(2), nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r ranking.RankingsResult) {
				t.Helper()
				assert.Equal(t, int64(2), r.TotalCount)
				require.Len(t, r.Rankings, 2)
				assert.Equal(t, "ユーザーA", r.Rankings[0].Name)
				assert.Equal(t, "ユーザーB", r.Rankings[1].Name)
			},
		},
		{
			name:  "正常系: エントリーが空の場合は ListUsersByIDs を呼ばない",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
		},
		{
			name:  "異常系: GetUserRankings(store) がエラー",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).Return(nil, errors.New("redis error"))

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: ListUsersByIDs がエラー",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).
					Return([]rankingdomain.RankEntry{{Rank: 1, ID: 1}}, nil)
				repo.EXPECT().ListUsersByIDs(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, errors.New("db error"))

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: GetUserTotalCount がエラー",
			input: ranking.GetRankingsInput{Limit: 10, Offset: 0},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).Return(int64(0), errors.New("redis error"))

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc := tt.setup(t, ctrl)

			result, err := uc.GetUserRankings(context.Background(), tt.input)

			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, ranking.RankingsResult{}, result)
			} else {
				require.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}

// TestGetUserRank_正常系_異常系 はユーザー個別順位取得のユースケースを検証する。
func TestGetUserRank_正常系_異常系(t *testing.T) {
	t.Parallel()

	const userID = int64(10)

	tests := []struct {
		name        string
		userID      int64
		setup       func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase
		wantErr     bool
		checkErr    func(t *testing.T, err error)
		checkResult func(t *testing.T, r rankingdomain.UserRankResult)
	}{
		{
			name:   "正常系: ユーザー順位取得成功",
			userID: userID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(8000), true, nil)
				store.EXPECT().GetUserRank(gomock.Any(), userID).
					Return(int64(3), nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).
					Return(int64(100), nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.UserRankResult) {
				t.Helper()
				assert.Equal(t, userID, r.UserID)
				assert.Equal(t, "テストユーザー", r.UserName)
				assert.Equal(t, int64(8000), r.Points)
				assert.Equal(t, int64(3), r.Rank)
				assert.Equal(t, int64(100), r.TotalUsers)
			},
		},
		{
			name:   "異常系: GetUser がエラー",
			userID: userID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("", rankingdomain.ErrUserNotFound)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrUserNotFound)
			},
		},
		{
			name:   "異常系: ポイント未登録で ErrPointsNotFound",
			userID: userID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(0), false, nil)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrPointsNotFound)
			},
		},
		{
			name:   "異常系: GetUserPoints(store) がエラー",
			userID: userID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				errRedis := errors.New("redis error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(0), false, errRedis)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:   "異常系: GetUserRank(store) がエラー",
			userID: userID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				errRedis := errors.New("redis error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(8000), true, nil)
				store.EXPECT().GetUserRank(gomock.Any(), userID).
					Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:   "異常系: GetUserTotalCount がエラー",
			userID: userID,
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				outboxRepo := mockoutbox.NewMockRepository(ctrl)
				notifier := mockoutbox.NewMockNotifier(ctrl)

				errRedis := errors.New("redis error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(8000), true, nil)
				store.EXPECT().GetUserRank(gomock.Any(), userID).
					Return(int64(3), nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).
					Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, outboxRepo, notifier, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc := tt.setup(t, ctrl)

			result, err := uc.GetUserRank(context.Background(), tt.userID)

			if tt.wantErr {
				require.Error(t, err)
				if tt.checkErr != nil {
					tt.checkErr(t, err)
				}
				assert.Equal(t, rankingdomain.UserRankResult{}, result)
			} else {
				require.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
}
