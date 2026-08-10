// Package repository_test は RankingRepository の外部テスト。
package repository_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/repository"
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	mocksqlc "github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc/mock"
)

func TestRankingRepository_GetGuild(t *testing.T) {
	t.Parallel()

	errDB := errors.New("connection refused")
	now := time.Now()

	tests := []struct {
		name      string
		stubGuild sqlc.Guild
		stubErr   error
		wantErr   error
	}{
		{
			name: "正常系: ギルド取得成功",
			stubGuild: sqlc.Guild{
				ID:        1,
				Name:      "テストギルド",
				CreatedAt: sql.NullTime{Time: now, Valid: true},
				UpdatedAt: sql.NullTime{Time: now, Valid: true},
			},
		},
		{
			name:    "異常系: sql.ErrNoRows は ErrGuildNotFound に変換される",
			stubErr: sql.ErrNoRows,
			wantErr: rankingdomain.ErrGuildNotFound,
		},
		{
			name:    "異常系: その他の DB エラーは原エラーをラップして返す",
			stubErr: errDB,
			wantErr: errDB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetGuild(gomock.Any(), int64(1)).Return(tt.stubGuild, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.GetGuild(context.Background(), dummyTx{}, 1)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, rankingdomain.Guild{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.stubGuild.ID, got.ID)
			assert.Equal(t, tt.stubGuild.Name, got.Name)
		})
	}
}

func TestRankingRepository_GetUser(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name     string
		stubUser sqlc.User
		stubErr  error
		wantErr  error
		wantName string
	}{
		{
			name:     "正常系: ユーザー名取得成功",
			stubUser: sqlc.User{ID: 1, Name: "プレイヤー1"},
			wantName: "プレイヤー1",
		},
		{
			name:    "異常系: sql.ErrNoRows は ErrUserNotFound に変換される",
			stubErr: sql.ErrNoRows,
			wantErr: rankingdomain.ErrUserNotFound,
		},
		{
			name:    "異常系: その他の DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: errDB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetUser(gomock.Any(), int64(1)).Return(tt.stubUser, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.GetUser(context.Background(), dummyTx{}, 1)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, got)
		})
	}
}

func TestRankingRepository_IncrementGuildScore(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{name: "正常系: スコア加算成功"},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().IncrementGuildScore(gomock.Any(), sqlc.IncrementGuildScoreParams{
				GuildID: int64(1),
				Score:   int64(100),
			}).Return(tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			err := repo.IncrementGuildScore(context.Background(), dummyTx{}, 1, 100)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRankingRepository_InsertGuildScoreHistory(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{name: "正常系: 履歴挿入成功"},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().InsertGuildScoreHistory(gomock.Any(), sqlc.InsertGuildScoreHistoryParams{
				GuildID: int64(1),
				UserID:  int64(2),
				Score:   int64(100),
			}).Return(tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			err := repo.InsertGuildScoreHistory(context.Background(), dummyTx{}, 1, 2, 100)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestRankingRepository_BulkIncrementGuildScores は sqlc が生成できない可変行数 upsert を
// go-sqlmock で検証する。クエリ文字列・引数・引数の順序まで固定する。
func TestRankingRepository_BulkIncrementGuildScores(t *testing.T) {
	t.Parallel()

	const wantQueryMulti = "INSERT INTO guild_scores (guild_id, score) VALUES (?, ?),(?, ?),(?, ?)" +
		" ON DUPLICATE KEY UPDATE score = score + VALUES(score)"
	const wantQuerySingle = "INSERT INTO guild_scores (guild_id, score) VALUES (?, ?)" +
		" ON DUPLICATE KEY UPDATE score = score + VALUES(score)"
	errDB := errors.New("bulk upsert failed")

	// guild_id 昇順ソートは並行バッチ tx 間のデッドロック防止のための必須要件。
	// 入力を降順で与え、発行時に昇順へ並び替わることを引数順で検証する。
	t.Run("正常系: guild_id 昇順にソートしてから1文の複数行upsertを発行する（デッドロック防止の必須要件）", func(t *testing.T) {
		t.Parallel()

		db, mock := newSqlmockDB(t)
		mock.ExpectExec(wantQueryMulti).
			WithArgs(int64(1), int64(100), int64(2), int64(200), int64(3), int64(300)).
			WillReturnResult(sqlmock.NewResult(0, 3))

		repo := repository.NewRankingRepositoryWithExecer(db)
		err := repo.BulkIncrementGuildScores(context.Background(), nil, []rankingdomain.GuildScoreDelta{
			{GuildID: 3, Points: 300},
			{GuildID: 1, Points: 100},
			{GuildID: 2, Points: 200},
		})

		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("正常系: 呼び出し元のスライスはソートで破壊されない", func(t *testing.T) {
		t.Parallel()

		db, mock := newSqlmockDB(t)
		mock.ExpectExec(wantQueryMulti).
			WithArgs(int64(1), int64(100), int64(2), int64(200), int64(3), int64(300)).
			WillReturnResult(sqlmock.NewResult(0, 3))

		input := []rankingdomain.GuildScoreDelta{
			{GuildID: 3, Points: 300},
			{GuildID: 1, Points: 100},
			{GuildID: 2, Points: 200},
		}
		repo := repository.NewRankingRepositoryWithExecer(db)
		require.NoError(t, repo.BulkIncrementGuildScores(context.Background(), nil, input))

		assert.Equal(t, []rankingdomain.GuildScoreDelta{
			{GuildID: 3, Points: 300},
			{GuildID: 1, Points: 100},
			{GuildID: 2, Points: 200},
		}, input)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("正常系: 単一件でも正しいプレースホルダ数でupsertされる", func(t *testing.T) {
		t.Parallel()

		db, mock := newSqlmockDB(t)
		mock.ExpectExec(wantQuerySingle).
			WithArgs(int64(7), int64(50)).
			WillReturnResult(sqlmock.NewResult(0, 1))

		repo := repository.NewRankingRepositoryWithExecer(db)
		err := repo.BulkIncrementGuildScores(context.Background(), nil, []rankingdomain.GuildScoreDelta{
			{GuildID: 7, Points: 50},
		})

		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("正常系: 空スライスの場合はExecを発行せずnilを返す", func(t *testing.T) {
		t.Parallel()

		db, mock := newSqlmockDB(t)
		// ExpectExec を設定しないため、Exec が発行されたら ExpectationsWereMet で検知できる

		repo := repository.NewRankingRepositoryWithExecer(db)
		err := repo.BulkIncrementGuildScores(context.Background(), nil, []rankingdomain.GuildScoreDelta{})

		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("異常系: Exec失敗時はエラーがラップされて返る", func(t *testing.T) {
		t.Parallel()

		db, mock := newSqlmockDB(t)
		mock.ExpectExec(wantQuerySingle).
			WithArgs(int64(1), int64(100)).
			WillReturnError(errDB)

		repo := repository.NewRankingRepositoryWithExecer(db)
		err := repo.BulkIncrementGuildScores(context.Background(), nil, []rankingdomain.GuildScoreDelta{
			{GuildID: 1, Points: 100},
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, errDB)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// TestRankingRepository_BulkInsertGuildScoreHistories は履歴の一括挿入を検証する。
// スコア加算と違い履歴は集約もソートもせず、渡された順のままイベント単位で挿入する。
func TestRankingRepository_BulkInsertGuildScoreHistories(t *testing.T) {
	t.Parallel()

	const wantQueryMulti = "INSERT INTO guild_score_histories (guild_id, user_id, score) VALUES (?, ?, ?),(?, ?, ?)"
	const wantQuerySingle = "INSERT INTO guild_score_histories (guild_id, user_id, score) VALUES (?, ?, ?)"
	errDB := errors.New("bulk insert failed")

	t.Run("正常系: 複数件を1回の複数行INSERTで発行する（入力順を保持する）", func(t *testing.T) {
		t.Parallel()

		db, mock := newSqlmockDB(t)
		// 同一 guild_id が並んでも集約されず、guild_id 降順の入力もそのままの順で発行される。
		mock.ExpectExec(wantQueryMulti).
			WithArgs(int64(2), int64(20), int64(200), int64(1), int64(10), int64(100)).
			WillReturnResult(sqlmock.NewResult(0, 2))

		repo := repository.NewRankingRepositoryWithExecer(db)
		err := repo.BulkInsertGuildScoreHistories(context.Background(), nil, []rankingdomain.GuildScoreHistoryEntry{
			{GuildID: 2, UserID: 20, Points: 200},
			{GuildID: 1, UserID: 10, Points: 100},
		})

		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("正常系: 空スライスの場合はExecを発行せずnilを返す", func(t *testing.T) {
		t.Parallel()

		db, mock := newSqlmockDB(t)

		repo := repository.NewRankingRepositoryWithExecer(db)
		err := repo.BulkInsertGuildScoreHistories(context.Background(), nil, []rankingdomain.GuildScoreHistoryEntry{})

		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("異常系: Exec失敗時はエラーがラップされて返る", func(t *testing.T) {
		t.Parallel()

		db, mock := newSqlmockDB(t)
		mock.ExpectExec(wantQuerySingle).
			WithArgs(int64(1), int64(10), int64(100)).
			WillReturnError(errDB)

		repo := repository.NewRankingRepositoryWithExecer(db)
		err := repo.BulkInsertGuildScoreHistories(context.Background(), nil, []rankingdomain.GuildScoreHistoryEntry{
			{GuildID: 1, UserID: 10, Points: 100},
		})

		require.Error(t, err)
		assert.ErrorIs(t, err, errDB)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestRankingRepository_GetUserGuildID(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name        string
		stubGuildID int64
		stubErr     error
		wantErr     error
	}{
		{name: "正常系: ギルドID取得成功", stubGuildID: 5},
		{name: "異常系: sql.ErrNoRows は ErrUserNotInGuild に変換される", stubErr: sql.ErrNoRows, wantErr: rankingdomain.ErrUserNotInGuild},
		{name: "異常系: その他の DB エラーはラップして返す", stubErr: errDB, wantErr: errDB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetUserGuildID(gomock.Any(), int64(1)).Return(tt.stubGuildID, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.GetUserGuildID(context.Background(), dummyTx{}, 1)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.stubGuildID, got)
		})
	}
}

func TestRankingRepository_ListGuildsByIDs(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	now := time.Now()

	tests := []struct {
		name       string
		guildIDs   []int64
		stubGuilds []sqlc.Guild
		stubErr    error
		wantCount  int
		wantErr    bool
	}{
		{
			name:     "正常系: 複数ギルド取得",
			guildIDs: []int64{1, 2},
			stubGuilds: []sqlc.Guild{
				{ID: 1, Name: "ギルドA", CreatedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
				{ID: 2, Name: "ギルドB", CreatedAt: sql.NullTime{Time: now, Valid: true}, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
			},
			wantCount: 2,
		},
		{
			name:       "正常系: 空リスト",
			guildIDs:   []int64{},
			stubGuilds: []sqlc.Guild{},
			wantCount:  0,
		},
		{
			name:     "異常系: DB エラーはラップして返す",
			guildIDs: []int64{1},
			stubErr:  errDB,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().ListGuildsByIDs(gomock.Any(), tt.guildIDs).Return(tt.stubGuilds, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.ListGuildsByIDs(context.Background(), dummyTx{}, tt.guildIDs)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, tt.wantCount)
			// 件数だけでは sqlc 型 → domain 型のフィールド取り違え（Name の欠落等）を
			// 検出できない（testing-principles.md §10）。この変換を検証できるのは
			// infrastructure 層のここだけなので、要素の中身まで突合する。
			for _, want := range tt.stubGuilds {
				g, ok := got[want.ID]
				require.Truef(t, ok, "guild_id=%d がキーとして存在すること", want.ID)
				assert.Equal(t, want.ID, g.ID)
				assert.Equal(t, want.Name, g.Name)
				assert.Equal(t, want.CreatedAt.Time, g.CreatedAt)
				assert.Equal(t, want.UpdatedAt.Time, g.UpdatedAt)
			}
		})
	}
}

func TestRankingRepository_ListAllGuildScores(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	now := time.Now()

	tests := []struct {
		name       string
		stubScores []sqlc.GuildScore
		stubErr    error
		wantCount  int
		wantErr    bool
	}{
		{
			name: "正常系: 複数スコア取得",
			stubScores: []sqlc.GuildScore{
				{GuildID: 1, Score: 100, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
				{GuildID: 2, Score: 200, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
			},
			wantCount: 2,
		},
		{
			name:       "正常系: 空リスト",
			stubScores: []sqlc.GuildScore{},
			wantCount:  0,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().ListAllGuildScores(gomock.Any()).Return(tt.stubScores, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.ListAllGuildScores(context.Background(), dummyTx{})

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, tt.wantCount)
			// 添字を揃えて突合することで、フィールドの取り違えに加えて
			// SQL の ORDER BY 由来の並びが崩れる不具合も検出する（§10）。
			for i, want := range tt.stubScores {
				assert.Equal(t, want.GuildID, got[i].GuildID, "%d 件目", i+1)
				assert.Equal(t, want.Score, got[i].Score, "%d 件目", i+1)
				assert.Equal(t, want.UpdatedAt.Time, got[i].UpdatedAt, "%d 件目", i+1)
			}
		})
	}
}

func TestRankingRepository_GetUserPoints(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	now := time.Now()

	tests := []struct {
		name      string
		stubPoint sqlc.UserPoint
		stubErr   error
		wantErr   error
	}{
		{
			name: "正常系: ユーザーポイント取得成功",
			stubPoint: sqlc.UserPoint{
				UserID:    1,
				Points:    500,
				UpdatedAt: sql.NullTime{Time: now, Valid: true},
			},
		},
		{
			name:    "異常系: sql.ErrNoRows は ErrPointsNotFound に変換される",
			stubErr: sql.ErrNoRows,
			wantErr: rankingdomain.ErrPointsNotFound,
		},
		{
			name:    "異常系: その他の DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: errDB,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().GetUserPoints(gomock.Any(), int64(1)).Return(tt.stubPoint, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.GetUserPoints(context.Background(), dummyTx{}, 1)

			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, rankingdomain.UserPoint{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.stubPoint.UserID, got.UserID)
			assert.Equal(t, tt.stubPoint.Points, got.Points)
		})
	}
}

func TestRankingRepository_IncrementUserPoints(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{name: "正常系: ポイント加算成功"},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().IncrementUserPoints(gomock.Any(), sqlc.IncrementUserPointsParams{
				UserID: int64(1),
				Points: int64(200),
			}).Return(tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			err := repo.IncrementUserPoints(context.Background(), dummyTx{}, 1, 200)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRankingRepository_InsertUserPointHistory(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name    string
		stubErr error
		wantErr bool
	}{
		{name: "正常系: 履歴挿入成功"},
		{name: "異常系: DB エラーはラップして返す", stubErr: errDB, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().InsertUserPointHistory(gomock.Any(), sqlc.InsertUserPointHistoryParams{
				UserID: int64(1),
				Points: int64(200),
				Reason: "battle",
			}).Return(tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			err := repo.InsertUserPointHistory(context.Background(), dummyTx{}, 1, 200, "battle")

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRankingRepository_ListUsersByIDs(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")

	tests := []struct {
		name      string
		userIDs   []int64
		stubUsers []sqlc.User
		stubErr   error
		wantCount int
		wantErr   bool
	}{
		{
			name:      "正常系: 複数ユーザー取得",
			userIDs:   []int64{1, 2},
			stubUsers: []sqlc.User{{ID: 1, Name: "ユーザーA"}, {ID: 2, Name: "ユーザーB"}},
			wantCount: 2,
		},
		{
			name:      "正常系: 空リスト",
			userIDs:   []int64{},
			stubUsers: []sqlc.User{},
			wantCount: 0,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			userIDs: []int64{1},
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().ListUsersByIDs(gomock.Any(), tt.userIDs).Return(tt.stubUsers, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.ListUsersByIDs(context.Background(), dummyTx{}, tt.userIDs)

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
			for _, u := range tt.stubUsers {
				assert.Equal(t, u.Name, got[u.ID])
			}
		})
	}
}

func TestRankingRepository_ListAllUserPoints(t *testing.T) {
	t.Parallel()

	errDB := errors.New("db error")
	now := time.Now()

	tests := []struct {
		name       string
		stubPoints []sqlc.UserPoint
		stubErr    error
		wantCount  int
		wantErr    bool
	}{
		{
			name: "正常系: 複数ポイント取得",
			stubPoints: []sqlc.UserPoint{
				{UserID: 1, Points: 100, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
				{UserID: 2, Points: 200, UpdatedAt: sql.NullTime{Time: now, Valid: true}},
			},
			wantCount: 2,
		},
		{
			name:       "正常系: 空リスト",
			stubPoints: []sqlc.UserPoint{},
			wantCount:  0,
		},
		{
			name:    "異常系: DB エラーはラップして返す",
			stubErr: errDB,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockQ := mocksqlc.NewMockQuerier(ctrl)
			mockQ.EXPECT().ListAllUserPoints(gomock.Any()).Return(tt.stubPoints, tt.stubErr)

			repo := repository.NewRankingRepositoryWithQuerier(mockQ)
			got, err := repo.ListAllUserPoints(context.Background(), dummyTx{})

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, errDB)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, tt.wantCount)
			// ListAllGuildScores と同じ理由で添字を揃えて突合する（§10）。
			// 対になる2メソッドで検証の厚みを揃える（§8 対称性チェック）。
			for i, want := range tt.stubPoints {
				assert.Equal(t, want.UserID, got[i].UserID, "%d 件目", i+1)
				assert.Equal(t, want.Points, got[i].Points, "%d 件目", i+1)
				assert.Equal(t, want.UpdatedAt.Time, got[i].UpdatedAt, "%d 件目", i+1)
			}
		})
	}
}
