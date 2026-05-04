// Package ranking_test はランキングハンドラの外部テストパッケージ。
package ranking_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
)

// newEchoContext は指定のメソッド・パス・ボディで echo.Context を生成するヘルパー。
func newEchoContext(e *echo.Echo, method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	var reqBody *strings.Reader
	if body != "" {
		reqBody = strings.NewReader(body)
	} else {
		reqBody = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

func TestHandler_GetGuildRankings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		queryParams    string
		setupMock      func(m *mockranking.MockUsecase)
		wantStatusCode int
		wantBodyPart   string
	}{
		{
			name:        "正常系: limit と offset を指定して取得",
			queryParams: "?limit=10&offset=0",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetGuildRankings(gomock.Any(), rankingusecase.GetRankingsInput{Limit: 10, Offset: 0}).
					Return(rankingusecase.RankingsResult{
						Rankings: []rankingdomain.RankEntry{
							{Rank: 1, ID: 1, Name: "ギルドA", Score: 9000},
						},
						TotalCount: 1,
					}, nil)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"total_count":1`,
		},
		{
			name:        "正常系: クエリパラメータなしでもデフォルト動作（limit=0, offset=0 をそのままusecase に渡す）",
			queryParams: "",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetGuildRankings(gomock.Any(), rankingusecase.GetRankingsInput{Limit: 0, Offset: 0}).
					Return(rankingusecase.RankingsResult{Rankings: []rankingdomain.RankEntry{}, TotalCount: 0}, nil)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"total_count":0`,
		},
		{
			name:        "異常系: usecase がエラーを返す場合は 500",
			queryParams: "?limit=10&offset=0",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetGuildRankings(gomock.Any(), gomock.Any()).
					Return(rankingusecase.RankingsResult{}, errors.New("redis error"))
			},
			wantStatusCode: http.StatusInternalServerError,
			wantBodyPart:   `"message":"internal server error"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockUC := mockranking.NewMockUsecase(ctrl)
			tt.setupMock(mockUC)

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/rankings/guilds"+tt.queryParams, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/rankings/guilds")

			h := rankinghandler.NewHandler(mockUC, slogtest.NewLogger(t, nil))
			require.NoError(t, h.GetGuildRankings(c))

			assert.Equal(t, tt.wantStatusCode, rec.Code)
			assert.Contains(t, strings.TrimSpace(rec.Body.String()), tt.wantBodyPart)
		})
	}
}

func TestHandler_GetGuildRank(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		paramGuildID   string
		setupMock      func(m *mockranking.MockUsecase)
		wantStatusCode int
		wantBodyPart   string
	}{
		{
			name:         "正常系: ギルド順位取得成功",
			paramGuildID: "1",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetGuildRank(gomock.Any(), int64(1)).
					Return(rankingdomain.GuildRankResult{
						GuildID: 1, GuildName: "テストギルド", Score: 9000, Rank: 1, TotalGuilds: 10,
					}, nil)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"guild_name":"テストギルド"`,
		},
		{
			name:           "異常系: guildID が数値でない",
			paramGuildID:   "abc",
			setupMock:      func(_ *mockranking.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid guildID"`,
		},
		{
			name:           "異常系: guildID が 0 以下",
			paramGuildID:   "-1",
			setupMock:      func(_ *mockranking.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid guildID"`,
		},
		{
			name:         "異常系: ギルド未存在は 404",
			paramGuildID: "1",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetGuildRank(gomock.Any(), int64(1)).
					Return(rankingdomain.GuildRankResult{}, rankingdomain.ErrGuildNotFound)
			},
			wantStatusCode: http.StatusNotFound,
			wantBodyPart:   `"message":"guild not found"`,
		},
		{
			name:         "異常系: スコア未登録は 404",
			paramGuildID: "1",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetGuildRank(gomock.Any(), int64(1)).
					Return(rankingdomain.GuildRankResult{}, rankingdomain.ErrScoreNotFound)
			},
			wantStatusCode: http.StatusNotFound,
			wantBodyPart:   `"message":"score not found"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockUC := mockranking.NewMockUsecase(ctrl)
			tt.setupMock(mockUC)

			e := echo.New()
			c, rec := newEchoContext(e, http.MethodGet, "/", "")
			c.SetPath("/guilds/:guildID/ranking")
			c.SetParamNames("guildID")
			c.SetParamValues(tt.paramGuildID)

			h := rankinghandler.NewHandler(mockUC, slogtest.NewLogger(t, nil))
			require.NoError(t, h.GetGuildRank(c))

			assert.Equal(t, tt.wantStatusCode, rec.Code)
			assert.Contains(t, strings.TrimSpace(rec.Body.String()), tt.wantBodyPart)
		})
	}
}

func TestHandler_AddUserPoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		paramUserID    string
		body           string
		setupMock      func(m *mockranking.MockUsecase)
		wantStatusCode int
		wantBodyPart   string
	}{
		{
			name:        "正常系: ポイント加算成功（ギルド集計フィールド含む）",
			paramUserID: "10",
			body:        `{"points":500,"reason":"クエストクリア"}`,
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().AddUserPoints(gomock.Any(), rankingusecase.AddUserPointsInput{
					UserID: 10, Points: 500, Reason: "クエストクリア",
				}).Return(rankingdomain.UserPointAddResult{
					UserID:             10,
					Points:             500,
					PreviousTotal:      1000,
					NewTotal:           1500,
					GuildID:            1,
					GuildPreviousTotal: 5000,
					GuildNewTotal:      5500,
				}, nil)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"new_total":1500`,
		},
		{
			name:        "正常系: レスポンスにギルド集計フィールドが含まれる",
			paramUserID: "10",
			body:        `{"points":100,"reason":"デイリーボーナス"}`,
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().AddUserPoints(gomock.Any(), rankingusecase.AddUserPointsInput{
					UserID: 10, Points: 100, Reason: "デイリーボーナス",
				}).Return(rankingdomain.UserPointAddResult{
					UserID:             10,
					Points:             100,
					PreviousTotal:      0,
					NewTotal:           100,
					GuildID:            3,
					GuildPreviousTotal: 0,
					GuildNewTotal:      100,
				}, nil)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"guild_id":3`,
		},
		{
			name:        "正常系: レスポンスに rank/guild_rank が含まれない",
			paramUserID: "10",
			body:        `{"points":50,"reason":"ログインボーナス"}`,
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().AddUserPoints(gomock.Any(), rankingusecase.AddUserPointsInput{
					UserID: 10, Points: 50, Reason: "ログインボーナス",
				}).Return(rankingdomain.UserPointAddResult{
					UserID:             10,
					Points:             50,
					PreviousTotal:      100,
					NewTotal:           150,
					GuildID:            1,
					GuildPreviousTotal: 1000,
					GuildNewTotal:      1050,
				}, nil)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"new_total":150`,
		},
		{
			name:           "異常系: userID が数値でない",
			paramUserID:    "abc",
			body:           `{"points":500,"reason":"クエスト"}`,
			setupMock:      func(_ *mockranking.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid userID"`,
		},
		{
			name:           "異常系: userID が 0 以下",
			paramUserID:    "0",
			body:           `{"points":500,"reason":"クエスト"}`,
			setupMock:      func(_ *mockranking.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid userID"`,
		},
		{
			name:           "異常系: reason が空",
			paramUserID:    "10",
			body:           `{"points":500,"reason":""}`,
			setupMock:      func(_ *mockranking.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"reason is required"`,
		},
		{
			name:           "異常系: 不正な JSON ボディ",
			paramUserID:    "10",
			body:           `{`,
			setupMock:      func(_ *mockranking.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid request body"`,
		},
		{
			name:        "異常系: ユーザー未存在は 404",
			paramUserID: "10",
			body:        `{"points":500,"reason":"クエスト"}`,
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().AddUserPoints(gomock.Any(), gomock.Any()).
					Return(rankingdomain.UserPointAddResult{}, rankingdomain.ErrUserNotFound)
			},
			wantStatusCode: http.StatusNotFound,
			wantBodyPart:   `"message":"user not found"`,
		},
		{
			name:        "異常系: ギルド未所属は 403",
			paramUserID: "10",
			body:        `{"points":500,"reason":"クエスト"}`,
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().AddUserPoints(gomock.Any(), gomock.Any()).
					Return(rankingdomain.UserPointAddResult{}, rankingdomain.ErrUserNotInGuild)
			},
			wantStatusCode: http.StatusForbidden,
			wantBodyPart:   `"message":"user is not a member of the guild"`,
		},
		{
			name:        "異常系: ポイント無効は 400",
			paramUserID: "10",
			body:        `{"points":-1,"reason":"クエスト"}`,
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().AddUserPoints(gomock.Any(), gomock.Any()).
					Return(rankingdomain.UserPointAddResult{}, rankingdomain.ErrInvalidPoints)
			},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid points"`,
		},
		{
			name:        "異常系: 予期せぬエラーは 500",
			paramUserID: "10",
			body:        `{"points":500,"reason":"クエスト"}`,
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().AddUserPoints(gomock.Any(), gomock.Any()).
					Return(rankingdomain.UserPointAddResult{}, errors.New("unexpected error"))
			},
			wantStatusCode: http.StatusInternalServerError,
			wantBodyPart:   `"message":"internal server error"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockUC := mockranking.NewMockUsecase(ctrl)
			tt.setupMock(mockUC)

			e := echo.New()
			c, rec := newEchoContext(e, http.MethodPost, "/", tt.body)
			c.SetPath("/users/:userID/points")
			c.SetParamNames("userID")
			c.SetParamValues(tt.paramUserID)

			h := rankinghandler.NewHandler(mockUC, slogtest.NewLogger(t, nil))
			require.NoError(t, h.AddUserPoints(c))

			assert.Equal(t, tt.wantStatusCode, rec.Code)
			assert.Contains(t, strings.TrimSpace(rec.Body.String()), tt.wantBodyPart)
		})
	}
}

func TestHandler_GetUserRankings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		queryParams    string
		setupMock      func(m *mockranking.MockUsecase)
		wantStatusCode int
		wantBodyPart   string
	}{
		{
			name:        "正常系: ユーザーランキング取得成功",
			queryParams: "?limit=5&offset=0",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetUserRankings(gomock.Any(), rankingusecase.GetRankingsInput{Limit: 5, Offset: 0}).
					Return(rankingusecase.RankingsResult{
						Rankings:   []rankingdomain.RankEntry{{Rank: 1, ID: 1, Name: "ユーザーA", Score: 8000}},
						TotalCount: 1,
					}, nil)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"total_count":1`,
		},
		{
			name:        "異常系: usecase がエラーを返す場合は 500",
			queryParams: "?limit=10&offset=0",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetUserRankings(gomock.Any(), gomock.Any()).
					Return(rankingusecase.RankingsResult{}, errors.New("redis error"))
			},
			wantStatusCode: http.StatusInternalServerError,
			wantBodyPart:   `"message":"internal server error"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockUC := mockranking.NewMockUsecase(ctrl)
			tt.setupMock(mockUC)

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/rankings/users"+tt.queryParams, nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			c.SetPath("/rankings/users")

			h := rankinghandler.NewHandler(mockUC, slogtest.NewLogger(t, nil))
			require.NoError(t, h.GetUserRankings(c))

			assert.Equal(t, tt.wantStatusCode, rec.Code)
			assert.Contains(t, strings.TrimSpace(rec.Body.String()), tt.wantBodyPart)
		})
	}
}

func TestHandler_GetUserRank(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		paramUserID    string
		setupMock      func(m *mockranking.MockUsecase)
		wantStatusCode int
		wantBodyPart   string
	}{
		{
			name:        "正常系: ユーザー順位取得成功",
			paramUserID: "10",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetUserRank(gomock.Any(), int64(10)).
					Return(rankingdomain.UserRankResult{
						UserID: 10, UserName: "テストユーザー", Points: 8000, Rank: 3, TotalUsers: 100,
					}, nil)
			},
			wantStatusCode: http.StatusOK,
			wantBodyPart:   `"user_name":"テストユーザー"`,
		},
		{
			name:           "異常系: userID が数値でない",
			paramUserID:    "abc",
			setupMock:      func(_ *mockranking.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid userID"`,
		},
		{
			name:           "異常系: userID が 0 以下",
			paramUserID:    "0",
			setupMock:      func(_ *mockranking.MockUsecase) {},
			wantStatusCode: http.StatusBadRequest,
			wantBodyPart:   `"message":"invalid userID"`,
		},
		{
			name:        "異常系: ユーザー未存在は 404",
			paramUserID: "10",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetUserRank(gomock.Any(), int64(10)).
					Return(rankingdomain.UserRankResult{}, rankingdomain.ErrUserNotFound)
			},
			wantStatusCode: http.StatusNotFound,
			wantBodyPart:   `"message":"user not found"`,
		},
		{
			name:        "異常系: ポイント未登録は 404",
			paramUserID: "10",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetUserRank(gomock.Any(), int64(10)).
					Return(rankingdomain.UserRankResult{}, rankingdomain.ErrPointsNotFound)
			},
			wantStatusCode: http.StatusNotFound,
			wantBodyPart:   `"message":"points not found"`,
		},
		{
			name:        "異常系: 予期せぬエラーは 500",
			paramUserID: "10",
			setupMock: func(m *mockranking.MockUsecase) {
				m.EXPECT().GetUserRank(gomock.Any(), int64(10)).
					Return(rankingdomain.UserRankResult{}, errors.New("unexpected error"))
			},
			wantStatusCode: http.StatusInternalServerError,
			wantBodyPart:   `"message":"internal server error"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockUC := mockranking.NewMockUsecase(ctrl)
			tt.setupMock(mockUC)

			e := echo.New()
			c, rec := newEchoContext(e, http.MethodGet, "/", "")
			c.SetPath("/users/:userID/ranking")
			c.SetParamNames("userID")
			c.SetParamValues(tt.paramUserID)

			h := rankinghandler.NewHandler(mockUC, slogtest.NewLogger(t, nil))
			require.NoError(t, h.GetUserRank(c))

			assert.Equal(t, tt.wantStatusCode, rec.Code)
			assert.Contains(t, strings.TrimSpace(rec.Body.String()), tt.wantBodyPart)
		})
	}
}
