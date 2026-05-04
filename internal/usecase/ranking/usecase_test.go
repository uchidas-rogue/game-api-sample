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

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
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

// TestSubmitGuildScore_正常系_スコア初回送信 はスコアが未登録の場合の正常系を検証する。
func TestSubmitGuildScore_正常系_スコア初回送信(t *testing.T) {
	t.Parallel()

	const (
		guildID = int64(1)
		userID  = int64(10)
		score   = int64(5000)
	)

	tests := []struct {
		name        string
		setup       func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase
		input       ranking.SubmitGuildScoreInput
		wantErr     bool
		checkErr    func(t *testing.T, err error)
		checkResult func(t *testing.T, r rankingdomain.GuildScoreSubmitResult)
	}{
		{
			name: "正常系: スコア初回送信でハイスコアになる",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID, Name: "テストギルド"}, nil)
				repo.EXPECT().IsUserInGuild(gomock.Any(), gomock.Any(), userID, guildID).
					Return(true, nil)
				// スコア未登録 → ErrScoreNotFound を返す
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, score).
					Return(nil)
				// isHighScore=true なので IncrementGuildScore が呼ばれる（差分 = score - 0）
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, score).
					Return(nil)
				store.EXPECT().IncrementGuildScore(gomock.Any(), guildID, score).
					Return(nil)
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(1), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: score},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.GuildScoreSubmitResult) {
				t.Helper()
				assert.Equal(t, guildID, r.GuildID)
				assert.Equal(t, score, r.Score)
				assert.True(t, r.IsHighScore)
				assert.Equal(t, int64(0), r.PreviousScore)
				assert.Equal(t, int64(1), r.Rank)
			},
		},
		{
			name: "正常系: 既存スコアより高いスコアを送信してハイスコア更新",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				const prevScore = int64(3000)
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID, Name: "テストギルド"}, nil)
				repo.EXPECT().IsUserInGuild(gomock.Any(), gomock.Any(), userID, guildID).
					Return(true, nil)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{GuildID: guildID, Score: prevScore}, nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, score).
					Return(nil)
				// 差分: 5000 - 3000 = 2000
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, score-prevScore).
					Return(nil)
				store.EXPECT().IncrementGuildScore(gomock.Any(), guildID, score-prevScore).
					Return(nil)
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(2), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: score},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.GuildScoreSubmitResult) {
				t.Helper()
				assert.True(t, r.IsHighScore)
				assert.Equal(t, int64(3000), r.PreviousScore)
				assert.Equal(t, int64(2), r.Rank)
			},
		},
		{
			name: "正常系: 既存スコア以下のスコアを送信してハイスコアにならない",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				const prevScore = int64(8000)
				const lowerScore = int64(5000)
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID, Name: "テストギルド"}, nil)
				repo.EXPECT().IsUserInGuild(gomock.Any(), gomock.Any(), userID, guildID).
					Return(true, nil)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{GuildID: guildID, Score: prevScore}, nil)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, lowerScore).
					Return(nil)
				// isHighScore=false なので IncrementGuildScore は呼ばれない
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(3), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: 5000},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.GuildScoreSubmitResult) {
				t.Helper()
				assert.False(t, r.IsHighScore)
				assert.Equal(t, int64(8000), r.PreviousScore)
			},
		},
		{
			name: "異常系: スコアが負数で ErrInvalidScore",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: -1},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrInvalidScore)
			},
		},
		{
			name: "異常系: スコアが最大値超過で ErrInvalidScore",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: int64(rankingdomain.MaxScore) + 1},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrInvalidScore)
			},
		},
		{
			name: "境界値: スコアが MaxScore ちょうどで成功する",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID, Name: "テストギルド"}, nil)
				repo.EXPECT().IsUserInGuild(gomock.Any(), gomock.Any(), userID, guildID).
					Return(true, nil)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				maxScore := int64(rankingdomain.MaxScore)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, maxScore).
					Return(nil)
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, maxScore).
					Return(nil)
				store.EXPECT().IncrementGuildScore(gomock.Any(), guildID, maxScore).
					Return(nil)
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(1), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: int64(rankingdomain.MaxScore)},
			wantErr: false,
		},
		{
			name: "異常系: GetGuild がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{}, errDB)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: score},
			wantErr: true,
		},
		{
			name: "異常系: IsUserInGuild がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				repo.EXPECT().IsUserInGuild(gomock.Any(), gomock.Any(), userID, guildID).
					Return(false, errDB)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: score},
			wantErr: true,
		},
		{
			name: "異常系: ユーザーがギルド非所属で ErrUserNotInGuild",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				repo.EXPECT().IsUserInGuild(gomock.Any(), gomock.Any(), userID, guildID).
					Return(false, nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: score},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrUserNotInGuild)
			},
		},
		{
			name: "異常系: InsertGuildScoreHistory がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				repo.EXPECT().IsUserInGuild(gomock.Any(), gomock.Any(), userID, guildID).
					Return(true, nil)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, score).
					Return(errDB)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: score},
			wantErr: true,
		},
		{
			name: "異常系: IncrementGuildScore(repo) がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				repo.EXPECT().IsUserInGuild(gomock.Any(), gomock.Any(), userID, guildID).
					Return(true, nil)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, score).
					Return(nil)
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, score).
					Return(errDB)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: score},
			wantErr: true,
		},
		{
			name: "異常系: IncrementGuildScore(store) がエラー",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				errRedis := errors.New("redis error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				repo.EXPECT().IsUserInGuild(gomock.Any(), gomock.Any(), userID, guildID).
					Return(true, nil)
				repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
				repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), guildID, userID, score).
					Return(nil)
				repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), guildID, score).
					Return(nil)
				store.EXPECT().IncrementGuildScore(gomock.Any(), guildID, score).
					Return(errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: score},
			wantErr: true,
		},
		{
			name: "異常系: Transactor.DoInTx 自体がエラー（begin失敗）",
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)

				errTx := errors.New("tx begin error")
				tx.EXPECT().DoInTx(gomock.Any(), gomock.Any()).Return(errTx)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			input:   ranking.SubmitGuildScoreInput{GuildID: guildID, UserID: userID, Score: score},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			uc := tt.setup(t, ctrl)

			result, err := uc.SubmitGuildScore(context.Background(), tt.input)

			if tt.wantErr {
				require.Error(t, err)
				if tt.checkErr != nil {
					tt.checkErr(t, err)
				}
				assert.Equal(t, rankingdomain.GuildScoreSubmitResult{}, result)
			} else {
				require.NoError(t, err)
				if tt.checkResult != nil {
					tt.checkResult(t, result)
				}
			}
		})
	}
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

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				// NormalizeLimit(0) = 10 (DefaultRankingLimit)
				store.EXPECT().GetGuildRankings(gomock.Any(), 0, rankingdomain.DefaultRankingLimit).
					Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				store.EXPECT().GetGuildRankings(gomock.Any(), 0, rankingdomain.MaxRankingLimit).
					Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).
					Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				errRedis := errors.New("redis error")
				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).Return(nil, errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				errDB := errors.New("db error")
				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).
					Return([]rankingdomain.RankEntry{{Rank: 1, ID: 1}}, nil)
				repo.EXPECT().ListGuildsByIDs(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, errDB)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				errRedis := errors.New("redis error")
				store.EXPECT().GetGuildRankings(gomock.Any(), 0, 10).Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID, Name: "テストギルド"}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(9000), true, nil)
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(1), nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).
					Return(int64(10), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{}, rankingdomain.ErrGuildNotFound)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(0), false, nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				errRedis := errors.New("redis error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(0), false, errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				errRedis := errors.New("redis error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(9000), true, nil)
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				errRedis := errors.New("redis error")
				repo.EXPECT().GetGuild(gomock.Any(), gomock.Any(), guildID).
					Return(rankingdomain.Guild{ID: guildID}, nil)
				store.EXPECT().GetGuildScore(gomock.Any(), guildID).
					Return(int64(9000), true, nil)
				store.EXPECT().GetGuildRank(gomock.Any(), guildID).
					Return(int64(1), nil)
				store.EXPECT().GetGuildTotalCount(gomock.Any()).
					Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

// TestAddUserPoints_正常系_異常系 はユーザーポイント加算のユースケースを検証する。
func TestAddUserPoints_正常系_異常系(t *testing.T) {
	t.Parallel()

	const (
		userID = int64(10)
		points = int64(500)
		reason = "クエストクリア報酬"
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
			name:  "正常系: ポイント初回加算（既存ポイントなし）",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).
					Return(nil)
				store.EXPECT().IncrementUserPoints(gomock.Any(), userID, points).
					Return(nil)
				store.EXPECT().GetUserRank(gomock.Any(), userID).
					Return(int64(1), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.UserPointAddResult) {
				t.Helper()
				assert.Equal(t, userID, r.UserID)
				assert.Equal(t, points, r.Points)
				assert.Equal(t, int64(0), r.PreviousTotal)
				assert.Equal(t, points, r.NewTotal)
				assert.Equal(t, int64(1), r.Rank)
			},
		},
		{
			name:  "正常系: 既存ポイントに加算",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				const prevPoints = int64(1000)
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{UserID: userID, Points: prevPoints}, nil)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).
					Return(nil)
				store.EXPECT().IncrementUserPoints(gomock.Any(), userID, points).
					Return(nil)
				store.EXPECT().GetUserRank(gomock.Any(), userID).
					Return(int64(5), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: false,
			checkResult: func(t *testing.T, r rankingdomain.UserPointAddResult) {
				t.Helper()
				assert.Equal(t, int64(1000), r.PreviousTotal)
				assert.Equal(t, int64(1500), r.NewTotal)
				assert.Equal(t, int64(5), r.Rank)
			},
		},
		{
			name:  "異常系: ポイントが負数で ErrInvalidPoints",
			input: ranking.AddUserPointsInput{UserID: userID, Points: -1, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrInvalidPoints)
			},
		},
		{
			name:  "異常系: ポイントが最大値超過で ErrInvalidPoints",
			input: ranking.AddUserPointsInput{UserID: userID, Points: int64(rankingdomain.MaxScore) + 1, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrInvalidPoints)
			},
		},
		{
			name:  "異常系: GetUser がエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("", rankingdomain.ErrUserNotFound)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
			checkErr: func(t *testing.T, err error) {
				t.Helper()
				assert.ErrorIs(t, err, rankingdomain.ErrUserNotFound)
			},
		},
		{
			name:  "異常系: InsertUserPointHistory がエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(errDB)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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
				newDoInTxCaller(tx)

				errDB := errors.New("db error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).
					Return(errDB)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
			},
			wantErr: true,
		},
		{
			name:  "異常系: IncrementUserPoints(store) がエラー",
			input: ranking.AddUserPointsInput{UserID: userID, Points: points, Reason: reason},
			setup: func(t *testing.T, ctrl *gomock.Controller) ranking.Usecase {
				t.Helper()
				repo := mockranking.NewMockRepository(ctrl)
				store := mockranking.NewMockRankingStore(ctrl)
				tx := mockshared.NewMockTransactor(ctrl)
				newDoInTxCaller(tx)

				errRedis := errors.New("redis error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), userID).
					Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
				repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), userID, points, reason).
					Return(nil)
				repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), userID, points).
					Return(nil)
				store.EXPECT().IncrementUserPoints(gomock.Any(), userID, points).
					Return(errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				entries := []rankingdomain.RankEntry{
					{Rank: 1, ID: 1, Score: 8000},
					{Rank: 2, ID: 2, Score: 6000},
				}
				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).Return(entries, nil)
				repo.EXPECT().ListUsersByIDs(gomock.Any(), gomock.Any(), []int64{1, 2}).
					Return(map[int64]string{1: "ユーザーA", 2: "ユーザーB"}, nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).Return(int64(2), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).Return(int64(0), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).Return(nil, errors.New("redis error"))

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).
					Return([]rankingdomain.RankEntry{{Rank: 1, ID: 1}}, nil)
				repo.EXPECT().ListUsersByIDs(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(nil, errors.New("db error"))

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				store.EXPECT().GetUserRankings(gomock.Any(), 0, 10).Return([]rankingdomain.RankEntry{}, nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).Return(int64(0), errors.New("redis error"))

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(8000), true, nil)
				store.EXPECT().GetUserRank(gomock.Any(), userID).
					Return(int64(3), nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).
					Return(int64(100), nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("", rankingdomain.ErrUserNotFound)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(0), false, nil)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				errRedis := errors.New("redis error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(0), false, errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				errRedis := errors.New("redis error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(8000), true, nil)
				store.EXPECT().GetUserRank(gomock.Any(), userID).
					Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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

				errRedis := errors.New("redis error")
				repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), userID).
					Return("テストユーザー", nil)
				store.EXPECT().GetUserPoints(gomock.Any(), userID).
					Return(int64(8000), true, nil)
				store.EXPECT().GetUserRank(gomock.Any(), userID).
					Return(int64(3), nil)
				store.EXPECT().GetUserTotalCount(gomock.Any()).
					Return(int64(0), errRedis)

				return ranking.NewUsecase(tx, repo, store, slogtest.NewLogger(t, nil))
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
