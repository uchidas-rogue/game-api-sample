// Package gacha_test はガチャ HTTP ハンドラの外部テストパッケージ。
//
// テスト設計（フロー図・テスト仕様表）は docs/testing/http-gacha.md にある。
// 分岐を追加・変更したら、まず図と表を更新してからここを直す。
//
// 入力値の境界（pull_count の範囲）は domain の IsValidPullCount が正本であり、
// internal/domain/gacha のケース配列で網羅済み。ここでは重ねて検証しない。
package gacha_test

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

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
	gachahandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/gacha"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	gachausecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
	mockuc "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha/mock"
)

const (
	multiPath     = "/users/:userID/gacha/multi"
	testUserID    = int64(1)
	testPullCount = 10
	// 正常系で usecase に返させる Result の値。レスポンスへ透過されることを確認する。
	testDrawnItemID = int64(10)
	testRemaining   = 500
)

// validMultiBody は正常系で使うリクエストボディ。
func validMultiBody() string { return fmt.Sprintf(`{"pull_count":%d}`, testPullCount) }

// wantRangeMessage は pull_count の範囲エラー文言。ハンドラと同じく domain 定数から作る。
func wantRangeMessage() string {
	return fmt.Sprintf(`"message":"pull_count must be between %d and %d"`,
		gachadomain.MinPullCount, gachadomain.MaxPullCount)
}

// rejectAt はリクエストが弾かれる地点。reachUsecase なら usecase まで到達する。
// テーブルが「どこまで進んで弾かれるか」をデータとして表現するために使う。
type rejectAt int

const (
	reachUsecase rejectAt = iota
	rejectUserID
	rejectBind
	rejectPullCount
)

// multiCase は Multi のテストケース1件。**データのみ**を持ち、手続きは持たない。
type multiCase struct {
	name string

	// ---- 入力 ----
	paramUserID string
	// body は空文字のとき validMultiBody() を使う。
	body string

	// ---- どこで弾かれるか ----
	rejectAt rejectAt
	// ucErr は usecase に返させるエラー。nil かつ reachUsecase なら正常系。
	ucErr error

	// ---- 期待結果 ----
	wantStatus int
	// wantBodyContains はレスポンスに含まれるべき文字列。
	wantBodyContains []string
	// wantBodyAbsent はレスポンスに現れてはならない文字列。
	wantBodyAbsent []string
}

func TestHandler_Multi(t *testing.T) {
	t.Parallel()

	errUnexpected := errors.New("db connection lost")

	// docs/testing/http-gacha.md の「1-2. テスト仕様表」と 1 対 1 で対応する。
	// 並び順は図のパスが短い順（エラー・早期リターンが先、正常系フルフローが後）。
	tests := []multiCase{
		{
			// #1 A→B→E1
			name:             "userID が数値でない: usecase を呼ばない",
			paramUserID:      "abc",
			rejectAt:         rejectUserID,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid userID"`},
		},
		{
			// #2 A→B→E1
			// #1 と同一パスだが判定は `err != nil || userID <= 0` の 2 条件。
			// userID <= 0 側を落とす実装ミスは #1 では検出できない。
			name:             "userID が 0 以下: usecase を呼ばない",
			paramUserID:      "0",
			rejectAt:         rejectUserID,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid userID"`},
		},
		{
			// #3 A→B→C→E2
			name:             "ボディが不正な JSON: usecase を呼ばない",
			paramUserID:      "1",
			body:             `{`,
			rejectAt:         rejectBind,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{`"message":"invalid request body"`},
		},
		{
			// #4 A→B→C→D→E3
			// 範囲外の値のバリエーション（上限超過・負数）は domain の責務なので増やさない。
			name:             "pull_count が範囲外: usecase を呼ばない",
			paramUserID:      "1",
			body:             `{"pull_count":0}`,
			rejectAt:         rejectPullCount,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{wantRangeMessage()},
		},
		{
			// #5 …→D→F→G→E4
			// ハンドラ側の事前検証と usecase 側の検証は二重防御。
			// どちらから来てもクライアントから見た応答は同一であること。
			name:             "usecase が ErrInvalidPullCount: 事前検証と同じ 400 を返す",
			paramUserID:      "1",
			ucErr:            gachadomain.ErrInvalidPullCount,
			wantStatus:       http.StatusBadRequest,
			wantBodyContains: []string{wantRangeMessage()},
		},
		{
			// #6 …→F→G→E5
			name:             "usecase が ErrUserNotFound: 404",
			paramUserID:      "1",
			ucErr:            gachadomain.ErrUserNotFound,
			wantStatus:       http.StatusNotFound,
			wantBodyContains: []string{`"message":"user not found"`},
		},
		{
			// #7 …→G→E6
			name:             "usecase が ErrInsufficientGems: 402",
			paramUserID:      "1",
			ucErr:            gachadomain.ErrInsufficientGems,
			wantStatus:       http.StatusPaymentRequired,
			wantBodyContains: []string{`"message":"insufficient gems"`},
		},
		{
			// #8 …→G→E7
			name:             "usecase が ErrNoItemsAvailable: 503",
			paramUserID:      "1",
			ucErr:            gachadomain.ErrNoItemsAvailable,
			wantStatus:       http.StatusServiceUnavailable,
			wantBodyContains: []string{`"message":"gacha is unavailable"`},
		},
		{
			// #9 …→G→E7
			// #8 と終端は同じだが errors.Is の判定対象が違う。片方の case 落ちを検出する。
			name:             "usecase が ErrInvalidItemWeights: 503",
			paramUserID:      "1",
			ucErr:            gachadomain.ErrInvalidItemWeights,
			wantStatus:       http.StatusServiceUnavailable,
			wantBodyContains: []string{`"message":"gacha is unavailable"`},
		},
		{
			// #10 …→G→E8
			name:             "usecase が予期せぬエラー: 500 で詳細を漏らさない",
			paramUserID:      "1",
			ucErr:            errUnexpected,
			wantStatus:       http.StatusInternalServerError,
			wantBodyContains: []string{`"message":"internal server error"`},
			wantBodyAbsent:   []string{errUnexpected.Error()},
		},
		{
			// #11 …→F→H→Z
			name:        "正常系: Result がレスポンスへ変換される",
			paramUserID: "1",
			wantStatus:  http.StatusOK,
			wantBodyContains: []string{
				fmt.Sprintf(`"user_id":%d`, testUserID),
				fmt.Sprintf(`"remaining_gems":%d`, testRemaining),
				fmt.Sprintf(`"id":%d`, testDrawnItemID),
				`"name":"potion"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runMultiCase(t, tt)
		})
	}
}

// runMultiCase はテーブルの1行を実行する唯一の手続き。
// 対象の生成・モックの期待組み立て・リクエスト発行・アサーションはすべてここにある。
func runMultiCase(t *testing.T, tc multiCase) {
	t.Helper()

	ctrl := gomock.NewController(t)
	uc := mockuc.NewMockUsecase(ctrl)

	// 入力バリデーションで弾かれるケースでは usecase を EXPECT しない。
	// 呼ばれてしまえば gomock が失敗させるため、表の「usecase が呼ばれない」の検証になる。
	if tc.rejectAt == reachUsecase {
		uc.EXPECT().Multi(gomock.Any(), testUserID, testPullCount).
			Return(newMultiResult(tc.ucErr), tc.ucErr).Times(1)
	}

	body := tc.body
	if body == "" {
		body = validMultiBody()
	}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath(multiPath)
	c.SetParamNames("userID")
	c.SetParamValues(tc.paramUserID)

	h := gachahandler.NewHandler(uc, slogtest.NewLogger(t, nil))
	require.NoError(t, h.Multi(c), "ハンドラは echo にエラーを返さず自分で JSON を書く")

	assert.Equal(t, tc.wantStatus, rec.Code)
	got := strings.TrimSpace(rec.Body.String())
	for _, want := range tc.wantBodyContains {
		assert.Contains(t, got, want)
	}
	for _, absent := range tc.wantBodyAbsent {
		assert.NotContains(t, got, absent, "内部エラーの詳細をレスポンスに含めない")
	}
}

// newMultiResult は usecase が返す Result を組み立てる。エラー時はゼロ値。
func newMultiResult(ucErr error) gachausecase.Result {
	if ucErr != nil {
		return gachausecase.Result{}
	}
	return gachausecase.Result{
		UserID:        testUserID,
		RemainingGems: testRemaining,
		DrawnItems: []gachadomain.Item{
			{ID: testDrawnItemID, Name: "potion", Rarity: gachadomain.RarityN},
		},
	}
}
