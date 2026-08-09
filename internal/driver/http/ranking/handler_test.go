// Package ranking_test はランキング HTTP ハンドラの外部テストパッケージ。
//
// テスト設計（フロー図・テスト仕様表）は docs/testing/http-ranking.md にある。
// 分岐を追加・変更したら、まず図と表を更新してからここを直す。
//
// limit/offset の正規化と points の値域は domain / usecase の責務であり、
// ここではエラー → ステータスの**マッピング**と DTO 変換だけを検証する。
package ranking_test

import (
	"errors"
	"fmt"
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

const (
	testGuildID = int64(1)
	testUserID  = int64(10)
)

// newEchoContext は指定のメソッド・パス・ボディで echo.Context を生成するヘルパー。
func newEchoContext(e *echo.Echo, method, path, body string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec), rec
}

// newHandler はモック化した usecase を持つ Handler を返す。
func newHandler(t *testing.T, uc *mockranking.MockUsecase) *rankinghandler.Handler {
	t.Helper()
	return rankinghandler.NewHandler(uc, slogtest.NewLogger(t, nil))
}

// assertResponse はステータスとボディの部分一致をまとめて検証する。
func assertResponse(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, contains, absent []string) {
	t.Helper()
	assert.Equal(t, wantStatus, rec.Code)
	got := strings.TrimSpace(rec.Body.String())
	for _, want := range contains {
		assert.Contains(t, got, want)
	}
	for _, s := range absent {
		assert.NotContains(t, got, s)
	}
}

// rankingKind はギルド版・ユーザー版の区別。
// 構造が同一な 2 メソッドに **同じケース表を両方流す**ことで、
// 「片方にだけケースがある」非対称な状態が構造的に起きなくなる。
type rankingKind int

const (
	kindGuild rankingKind = iota
	kindUser
)

func (k rankingKind) String() string {
	if k == kindGuild {
		return "guild"
	}
	return "user"
}

// ---------------------------------------------------------------------------
// 1. 一覧系: GetGuildRankings / GetUserRankings
// ---------------------------------------------------------------------------

// listRejectAt は一覧系でリクエストが弾かれる地点。
type listRejectAt int

const (
	listReachUsecase listRejectAt = iota
	listRejectLimit
	listRejectOffset
)

// rankingsCase は GetXxxRankings のテストケース1件。データのみを持つ。
type rankingsCase struct {
	name string

	// ---- 入力 ----
	query string

	// ---- どこで弾かれるか ----
	rejectAt listRejectAt
	// ucErr は usecase に返させるエラー。nil かつ listReachUsecase なら正常系。
	ucErr error
	// wantInput は usecase に渡るべき入力。正規化せず生値が渡ることを確認する。
	wantInput rankingusecase.GetRankingsInput

	// ---- 期待結果 ----
	wantStatus       int
	wantBodyContains []string
}

func TestHandler_GetRankings(t *testing.T) {
	t.Parallel()

	errUnexpected := errors.New("redis error")

	// docs/testing/http-ranking.md「1. 一覧系」の仕様表と 1 対 1 で対応する。
	// 並び順は図のパスが短い順。
	tests := []rankingsCase{
		{
			// #1 A→B→E1（P3→PE1）
			name:             "limit が数値でない: usecase を呼ばない",
			query:            "?limit=abc",
			rejectAt:         listRejectLimit,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid limit"`},
		},
		{
			// #2 A→B→E1（P4→PE2）
			name:             "limit が負数: usecase を呼ばない",
			query:            "?limit=-1&offset=0",
			rejectAt:         listRejectLimit,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid limit"`},
		},
		{
			// #3 A→B→C→E2（P3→PE1）
			name:             "offset が数値でない: usecase を呼ばない",
			query:            "?limit=10&offset=abc",
			rejectAt:         listRejectOffset,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid offset"`},
		},
		{
			// #4 A→B→C→E2（P4→PE2）
			name:             "offset が負数: usecase を呼ばない",
			query:            "?limit=10&offset=-5",
			rejectAt:         listRejectOffset,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid offset"`},
		},
		{
			// #5 …→C→D→X→R8
			name:             "usecase が予期せぬエラー: 500",
			query:            "?limit=10&offset=0",
			ucErr:            errUnexpected,
			wantInput:        rankingusecase.GetRankingsInput{Limit: 10, Offset: 0},
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: []string{`"message":"internal server error"`},
		},
		{
			// #6 …→D→F→Z（P1→P2）
			// 既定値の適用は usecase の責務なので、ハンドラは生値 0 をそのまま渡す。
			name:             "正常系: クエリ未指定なら 0 をそのまま渡す",
			query:            "",
			wantInput:        rankingusecase.GetRankingsInput{Limit: 0, Offset: 0},
			wantStatus:       http.StatusOK,
			wantBodyContains: []string{`"total_count":1`, `"rank":1`},
		},
		{
			// #7 …→D→F→Z（P1→P3→P4→P5）
			name:       "正常系: limit/offset 指定がそのまま渡り Result が変換される",
			query:      "?limit=10&offset=0",
			wantInput:  rankingusecase.GetRankingsInput{Limit: 10, Offset: 0},
			wantStatus: http.StatusOK,
			wantBodyContains: []string{
				`"total_count":1`,
				`"rank":1`,
				`"score":9000`,
				`"name":"1位"`,
			},
		},
	}

	for _, kind := range []rankingKind{kindGuild, kindUser} {
		for _, tt := range tests {
			t.Run(kind.String()+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				runRankingsCase(t, kind, tt)
			})
		}
	}
}

func runRankingsCase(t *testing.T, kind rankingKind, tc rankingsCase) {
	t.Helper()

	ctrl := gomock.NewController(t)
	uc := mockranking.NewMockUsecase(ctrl)

	// 入力バリデーションで弾かれるケースでは EXPECT しない。
	// 呼ばれれば gomock が失敗させるため、「usecase が呼ばれない」の検証になる。
	if tc.rejectAt == listReachUsecase {
		result := rankingusecase.RankingsResult{}
		if tc.ucErr == nil {
			result = rankingusecase.RankingsResult{
				Rankings:   []rankingdomain.RankEntry{{Rank: 1, ID: 1, Name: "1位", Score: 9000}},
				TotalCount: 1,
			}
		}
		if kind == kindGuild {
			uc.EXPECT().GetGuildRankings(gomock.Any(), tc.wantInput).Return(result, tc.ucErr)
		} else {
			uc.EXPECT().GetUserRankings(gomock.Any(), tc.wantInput).Return(result, tc.ucErr)
		}
	}

	e := echo.New()
	path := "/rankings/guilds"
	if kind == kindUser {
		path = "/rankings/users"
	}
	c, rec := newEchoContext(e, http.MethodGet, path+tc.query, "")
	c.SetPath(path)

	h := newHandler(t, uc)
	if kind == kindGuild {
		require.NoError(t, h.GetGuildRankings(c))
	} else {
		require.NoError(t, h.GetUserRankings(c))
	}

	assertResponse(t, rec, tc.wantStatus, tc.wantBodyContains, nil)
}

// ---------------------------------------------------------------------------
// 2. 単一順位取得: GetGuildRank / GetUserRank
// ---------------------------------------------------------------------------

// singleRankFail は単一順位取得で usecase に返させる失敗の種類。
type singleRankFail int

const (
	singleRankNone       singleRankFail = iota
	singleRankRejectID                  // ID のパースで弾かれる（usecase を呼ばない）
	singleRankNotFound                  // ギルド/ユーザーが存在しない
	singleRankScoreNone                 // スコア/ポイントが未登録
	singleRankUnexpected                // 予期せぬエラー
)

type singleRankCase struct {
	name string

	// ---- 入力 ----
	paramID string

	// ---- どこで失敗させるか ----
	failAt singleRankFail

	// ---- 期待結果 ----
	wantStatus int
	// wantMessage は kind ごとに文言が異なるためランナーで解決する。
	wantBodyContains []string
}

func TestHandler_GetRank(t *testing.T) {
	t.Parallel()

	// docs/testing/http-ranking.md「2. 単一順位取得」の仕様表と 1 対 1 で対応する。
	tests := []singleRankCase{
		{
			// #1 A→B→E1
			name:       "ID が数値でない: usecase を呼ばない",
			paramID:    "abc",
			failAt:     singleRankRejectID,
			wantStatus: http.StatusBadRequest,
		},
		{
			// #2 A→B→E1
			// #1 と同一パスだが判定は `err != nil || id <= 0` の 2 条件。
			name:       "ID が 0 以下: usecase を呼ばない",
			paramID:    "0",
			failAt:     singleRankRejectID,
			wantStatus: http.StatusBadRequest,
		},
		{
			// #3 A→B→C→X→R1 / R2
			name:       "エンティティ未存在: 404",
			failAt:     singleRankNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			// #4 …→C→X→R6 / R7
			name:       "スコア/ポイント未登録: 404",
			failAt:     singleRankScoreNone,
			wantStatus: http.StatusNotFound,
		},
		{
			// #5 …→X→R8
			name:             "予期せぬエラー: 500",
			failAt:           singleRankUnexpected,
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: []string{`"message":"internal server error"`},
		},
		{
			// #6 …→C→D→Z
			name:       "正常系: Result がレスポンスへ変換される",
			failAt:     singleRankNone,
			wantStatus: http.StatusOK,
		},
	}

	for _, kind := range []rankingKind{kindGuild, kindUser} {
		for _, tt := range tests {
			t.Run(kind.String()+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				runSingleRankCase(t, kind, tt)
			})
		}
	}
}

func runSingleRankCase(t *testing.T, kind rankingKind, tc singleRankCase) {
	t.Helper()

	ctrl := gomock.NewController(t)
	uc := mockranking.NewMockUsecase(ctrl)

	id := testGuildID
	path := "/guilds/:guildID/ranking"
	paramName := "guildID"
	if kind == kindUser {
		id = testUserID
		path = "/users/:userID/ranking"
		paramName = "userID"
	}
	paramValue := tc.paramID
	if paramValue == "" {
		paramValue = fmt.Sprintf("%d", id)
	}

	if tc.failAt != singleRankRejectID {
		expectSingleRankCall(kind, uc, id, tc.failAt)
	}

	e := echo.New()
	c, rec := newEchoContext(e, http.MethodGet, "/", "")
	c.SetPath(path)
	c.SetParamNames(paramName)
	c.SetParamValues(paramValue)

	h := newHandler(t, uc)
	if kind == kindGuild {
		require.NoError(t, h.GetGuildRank(c))
	} else {
		require.NoError(t, h.GetUserRank(c))
	}

	assertResponse(t, rec, tc.wantStatus, append(tc.wantBodyContains, wantSingleRankBody(kind, tc.failAt)...), nil)
}

// expectSingleRankCall は failAt に応じた戻り値を usecase モックに設定する。
func expectSingleRankCall(kind rankingKind, uc *mockranking.MockUsecase, id int64, failAt singleRankFail) {
	if kind == kindGuild {
		var (
			res    rankingdomain.GuildRankResult
			ucErr  error
			result = rankingdomain.GuildRankResult{
				GuildID: id, GuildName: "テストギルド", Score: 9000, Rank: 1, TotalGuilds: 10,
			}
		)
		switch failAt {
		case singleRankNotFound:
			ucErr = rankingdomain.ErrGuildNotFound
		case singleRankScoreNone:
			ucErr = rankingdomain.ErrScoreNotFound
		case singleRankUnexpected:
			ucErr = errors.New("unexpected error")
		case singleRankNone, singleRankRejectID:
			res = result
		}
		uc.EXPECT().GetGuildRank(gomock.Any(), id).Return(res, ucErr)
		return
	}

	var (
		res    rankingdomain.UserRankResult
		ucErr  error
		result = rankingdomain.UserRankResult{
			UserID: id, UserName: "テストユーザー", Points: 8000, Rank: 3, TotalUsers: 100,
		}
	)
	switch failAt {
	case singleRankNotFound:
		ucErr = rankingdomain.ErrUserNotFound
	case singleRankScoreNone:
		ucErr = rankingdomain.ErrPointsNotFound
	case singleRankUnexpected:
		ucErr = errors.New("unexpected error")
	case singleRankNone, singleRankRejectID:
		res = result
	}
	uc.EXPECT().GetUserRank(gomock.Any(), id).Return(res, ucErr)
}

// wantSingleRankBody は kind ごとに異なる期待文言を返す。
// 「ギルド版とユーザー版で別のメッセージ・別のフィールド名になっていること」を固定する。
func wantSingleRankBody(kind rankingKind, failAt singleRankFail) []string {
	if kind == kindGuild {
		switch failAt {
		case singleRankRejectID:
			return []string{`"message":"invalid guildID"`}
		case singleRankNotFound:
			return []string{`"message":"guild not found"`}
		case singleRankScoreNone:
			return []string{`"message":"score not found"`}
		case singleRankNone:
			return []string{`"guild_name":"テストギルド"`, `"score":9000`, `"rank":1`, `"total_guilds":10`}
		case singleRankUnexpected:
			return nil
		}
		return nil
	}
	switch failAt {
	case singleRankRejectID:
		return []string{`"message":"invalid userID"`}
	case singleRankNotFound:
		return []string{`"message":"user not found"`}
	case singleRankScoreNone:
		return []string{`"message":"points not found"`}
	case singleRankNone:
		return []string{`"user_name":"テストユーザー"`, `"points":8000`, `"rank":3`, `"total_users":100`}
	case singleRankUnexpected:
		return nil
	}
	return nil
}

// ---------------------------------------------------------------------------
// 3. AddUserPoints
// ---------------------------------------------------------------------------

// addRejectAt は AddUserPoints でリクエストが弾かれる地点。
type addRejectAt int

const (
	addReachUsecase addRejectAt = iota
	addRejectUserID
	addRejectBind
	addRejectReason
)

type addPointsCase struct {
	name string

	// ---- 入力 ----
	paramUserID string
	// body は空文字のとき validPointsBody() を使う。
	body string

	// ---- どこで弾かれるか ----
	rejectAt addRejectAt
	// ucErr は usecase に返させるエラー。nil かつ addReachUsecase なら正常系。
	ucErr error

	// ---- 期待結果 ----
	wantStatus       int
	wantBodyContains []string
	wantBodyAbsent   []string
}

const (
	testPoints = int64(500)
	testReason = "クエストクリア"
)

func validPointsBody() string {
	return fmt.Sprintf(`{"points":%d,"reason":%q}`, testPoints, testReason)
}

func TestHandler_AddUserPoints(t *testing.T) {
	t.Parallel()

	// docs/testing/http-ranking.md「3. AddUserPoints」の仕様表と 1 対 1 で対応する。
	tests := []addPointsCase{
		{
			// #1 A→B→E1
			name:             "userID が数値でない: usecase を呼ばない",
			paramUserID:      "abc",
			rejectAt:         addRejectUserID,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid userID"`},
		},
		{
			// #2 A→B→E1
			name:             "userID が 0 以下: usecase を呼ばない",
			paramUserID:      "0",
			rejectAt:         addRejectUserID,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid userID"`},
		},
		{
			// #3 A→B→C→E2
			name:             "ボディが不正な JSON: usecase を呼ばない",
			body:             `{`,
			rejectAt:         addRejectBind,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid request body"`},
		},
		{
			// #4 A→B→C→D→E3
			name:             "reason が空: usecase を呼ばない",
			body:             `{"points":500,"reason":""}`,
			rejectAt:         addRejectReason,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"reason is required"`},
		},
		{
			// #5 …→D→F→X→R2
			name:             "usecase が ErrUserNotFound: 404",
			ucErr:            rankingdomain.ErrUserNotFound,
			wantStatus:       http.StatusNotFound,
			wantBodyContains: []string{`"message":"user not found"`},
		},
		{
			// #6 …→F→X→R3
			name:             "usecase が ErrUserNotInGuild: 403",
			ucErr:            rankingdomain.ErrUserNotInGuild,
			wantStatus:       http.StatusForbidden,
			wantBodyContains: []string{`"message":"user is not a member of the guild"`},
		},
		{
			// #7 …→X→R5
			name:             "usecase が ErrInvalidPoints: 400",
			ucErr:            rankingdomain.ErrInvalidPoints,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid points"`},
		},
		{
			// #8 …→X→R8
			name:             "usecase が予期せぬエラー: 500",
			ucErr:            errors.New("unexpected error"),
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: []string{`"message":"internal server error"`},
		},
		{
			// #9 …→F→G→Z
			// 順位は worker の Redis 反映後にしか確定しないため、レスポンスに含めない。
			name:       "正常系: 集計フィールドが揃い rank 系は含まれない",
			wantStatus: http.StatusOK,
			wantBodyContains: []string{
				fmt.Sprintf(`"user_id":%d`, testUserID),
				fmt.Sprintf(`"points":%d`, testPoints),
				`"previous_total":1000`,
				`"new_total":1500`,
				fmt.Sprintf(`"guild_id":%d`, testGuildID),
				`"guild_previous_total":5000`,
				`"guild_new_total":5500`,
			},
			wantBodyAbsent: []string{`"rank"`, `"guild_rank"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runAddPointsCase(t, tt)
		})
	}
}

func runAddPointsCase(t *testing.T, tc addPointsCase) {
	t.Helper()

	ctrl := gomock.NewController(t)
	uc := mockranking.NewMockUsecase(ctrl)

	if tc.rejectAt == addReachUsecase {
		result := rankingdomain.UserPointAddResult{}
		if tc.ucErr == nil {
			result = rankingdomain.UserPointAddResult{
				UserID:             testUserID,
				Points:             testPoints,
				PreviousTotal:      1000,
				NewTotal:           1000 + testPoints,
				GuildID:            testGuildID,
				GuildPreviousTotal: 5000,
				GuildNewTotal:      5000 + testPoints,
			}
		}
		// リクエストの値がそのまま usecase の入力になることを引数で検証する。
		uc.EXPECT().AddUserPoints(gomock.Any(), rankingusecase.AddUserPointsInput{
			UserID: testUserID, Points: testPoints, Reason: testReason,
		}).Return(result, tc.ucErr)
	}

	body := tc.body
	if body == "" {
		body = validPointsBody()
	}
	paramUserID := tc.paramUserID
	if paramUserID == "" {
		paramUserID = fmt.Sprintf("%d", testUserID)
	}

	e := echo.New()
	c, rec := newEchoContext(e, http.MethodPost, "/", body)
	c.SetPath("/users/:userID/points")
	c.SetParamNames("userID")
	c.SetParamValues(paramUserID)

	h := newHandler(t, uc)
	require.NoError(t, h.AddUserPoints(c))

	assertResponse(t, rec, tc.wantStatus, tc.wantBodyContains, tc.wantBodyAbsent)
}
