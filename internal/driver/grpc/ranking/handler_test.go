// Package ranking_test はランキング gRPC ハンドラの外部テストパッケージ。
//
// テスト設計（フロー図・テスト仕様表）は docs/testing/grpc-ranking.md にある。
// 分岐を追加・変更したら、まず図と表を更新してからここを直す。
//
// limit/offset の正規化と points の値域は domain / usecase の責務であり、
// ここではエラー → status code の**マッピング**と pb メッセージへの変換だけを検証する。
// 検証対象が変換とエラー経路である以上、bufconn 経由の RPC は挟まない
// （同じものしか見えず定型コードが増えるだけ）。ハンドラのメソッドを直接呼ぶ。
package ranking_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankingv1 "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/gen/rankingv1"
	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
)

const (
	testGuildID = int64(1)
	testUserID  = int64(10)
)

// retryInfoName は google.rpc.RetryInfo の proto フルネーム。
// errdetails パッケージは driver のテストから import できない（.golangci.yml の depguard）ため、
// protoreflect が返すメッセージ名で判定する。
const retryInfoName = "google.rpc.RetryInfo"

// newHandler はモック化した usecase を持つ Handler を返す。
func newHandler(t *testing.T, uc *mockranking.MockUsecase) *rankinghandler.Handler {
	t.Helper()
	return rankinghandler.NewHandler(uc, slogtest.NewLogger(t, nil))
}

// assertStatus はエラーが期待どおりの code / message を持つことを検証する。
func assertStatus(t *testing.T, err error, wantCode codes.Code, wantMessage string) *status.Status {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok, "gRPC の status を持つエラーであること: %v", err)
	assert.Equal(t, wantCode, st.Code())
	assert.Equal(t, wantMessage, st.Message())
	return st
}

// assertRetryInfo は details に RetryInfo が載っているかを検証する。
// Unavailable は「再試行すれば直る」ことを伝えるための code なので、
// details が落ちていないことを code と同じ強さで固定する。
// 遅延値そのものの検証は errors_test.go の責務（写像の詳細はそちらが正本）。
func assertRetryInfo(t *testing.T, st *status.Status, want bool) {
	t.Helper()
	found := false
	for _, detail := range st.Details() {
		m, ok := detail.(proto.Message)
		if ok && m.ProtoReflect().Descriptor().FullName() == retryInfoName {
			found = true
		}
	}
	assert.Equal(t, want, found, "RetryInfo の有無が期待と違う")
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
// 1. 一覧系: GetUserRankings / GetGuildRankings
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
	limit  int32
	offset int32

	// ---- どこで弾かれるか ----
	rejectAt listRejectAt
	// ucErr は usecase に返させるエラー。nil かつ listReachUsecase なら正常系。
	ucErr error
	// wantInput は usecase に渡るべき入力。正規化せず生値が渡ることを確認する。
	wantInput rankingusecase.GetRankingsInput

	// ---- 期待結果 ----
	// wantCode が codes.OK なら正常系として結果を検証する。
	wantCode      codes.Code
	wantMessage   string
	wantRetryInfo bool
}

func TestHandler_GetRankings(t *testing.T) {
	t.Parallel()

	errUnexpected := errors.New("redis error")

	// docs/testing/grpc-ranking.md「1. 一覧系」の仕様表と 1 対 1 で対応する。
	// 並び順は図のパスが短い順。
	tests := []rankingsCase{
		{
			// #1 A→B→E1
			name:        "limit が負値: usecase を呼ばない",
			limit:       -1,
			rejectAt:    listRejectLimit,
			wantCode:    codes.InvalidArgument,
			wantMessage: "limit must not be negative",
		},
		{
			// #2 A→B→C→E2
			name:        "offset が負値: usecase を呼ばない",
			limit:       10,
			offset:      -5,
			rejectAt:    listRejectOffset,
			wantCode:    codes.InvalidArgument,
			wantMessage: "offset must not be negative",
		},
		{
			// #3 …→C→D→X→R8
			name:        "usecase が予期せぬエラー: Internal",
			limit:       10,
			ucErr:       errUnexpected,
			wantInput:   rankingusecase.GetRankingsInput{Limit: 10, Offset: 0},
			wantCode:    codes.Internal,
			wantMessage: "internal server error",
		},
		{
			// #4 …→C→D→X→R9
			// Redis 揮発を「空のランキング」で返さないことを固定する。
			name:          "usecase が ErrRankingUnavailable: Unavailable と RetryInfo",
			limit:         10,
			ucErr:         rankingdomain.ErrRankingUnavailable,
			wantInput:     rankingusecase.GetRankingsInput{Limit: 10, Offset: 0},
			wantCode:      codes.Unavailable,
			wantMessage:   "ranking is unavailable",
			wantRetryInfo: true,
		},
		{
			// #5 …→D→F→Z
			// 既定値の適用は usecase の責務なので、ハンドラは生値をそのまま渡す。
			// 表の 1 行を「未設定（0）」「明示指定」の 2 ケースへ展開している。
			name:      "正常系: 未設定なら 0 をそのまま渡す",
			wantInput: rankingusecase.GetRankingsInput{Limit: 0, Offset: 0},
			wantCode:  codes.OK,
		},
		{
			name:      "正常系: limit/offset 指定がそのまま渡り Result が変換される",
			limit:     10,
			offset:    20,
			wantInput: rankingusecase.GetRankingsInput{Limit: 10, Offset: 20},
			wantCode:  codes.OK,
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

	h := newHandler(t, uc)
	ctx := t.Context()

	var (
		entries     []*rankingv1.RankEntry
		totalCount  int64
		hasResponse bool
		err         error
	)
	if kind == kindGuild {
		var res *rankingv1.GetGuildRankingsResponse
		res, err = h.GetGuildRankings(ctx, &rankingv1.GetGuildRankingsRequest{Limit: tc.limit, Offset: tc.offset})
		hasResponse = res != nil
		entries, totalCount = res.GetRankings(), res.GetTotalCount()
	} else {
		var res *rankingv1.GetUserRankingsResponse
		res, err = h.GetUserRankings(ctx, &rankingv1.GetUserRankingsRequest{Limit: tc.limit, Offset: tc.offset})
		hasResponse = res != nil
		entries, totalCount = res.GetRankings(), res.GetTotalCount()
	}

	if tc.wantCode != codes.OK {
		st := assertStatus(t, err, tc.wantCode, tc.wantMessage)
		assertRetryInfo(t, st, tc.wantRetryInfo)
		assert.False(t, hasResponse, "エラー時はレスポンスを返さない")
		return
	}

	require.NoError(t, err)
	assert.Equal(t, int64(1), totalCount)
	require.Len(t, entries, 1)
	assert.Equal(t, int64(1), entries[0].GetRank())
	assert.Equal(t, int64(1), entries[0].GetId())
	assert.Equal(t, "1位", entries[0].GetName())
	assert.Equal(t, int64(9000), entries[0].GetScore())
}

// ---------------------------------------------------------------------------
// 2. 単一順位取得: GetUserRank / GetGuildRank
// ---------------------------------------------------------------------------

// singleRankFail は単一順位取得で usecase に返させる失敗の種類。
type singleRankFail int

const (
	singleRankNone        singleRankFail = iota
	singleRankRejectID                   // ID が値域外（usecase を呼ばない）
	singleRankNotFound                   // ギルド/ユーザーが存在しない
	singleRankScoreNone                  // スコア/ポイントが未登録
	singleRankUnexpected                 // 予期せぬエラー
	singleRankUnavailable                // ランキング未構築（Redis 揮発）
)

type singleRankCase struct {
	name string

	// ---- 入力 ----
	// id は 0 のとき kind ごとの既定 ID に差し替える（0 は値域外の入力そのもの）。
	id int64

	// ---- どこで失敗させるか ----
	failAt singleRankFail

	// ---- 期待結果 ----
	wantCode codes.Code
	// wantMessage は kind ごとに文言が異なるためランナーで解決する。
	wantRetryInfo bool
}

func TestHandler_GetRank(t *testing.T) {
	t.Parallel()

	// docs/testing/grpc-ranking.md「2. 単一順位取得」の仕様表と 1 対 1 で対応する。
	tests := []singleRankCase{
		{
			// #1 A→B→E1
			// 未設定（proto3 のスカラ既定値 0）が値域外として弾かれること。
			// `id < 0` と書き誤ると 0 が通ってしまうので、境界値は 0 を使う。
			name:     "ID が 0 以下: usecase を呼ばない",
			failAt:   singleRankRejectID,
			wantCode: codes.InvalidArgument,
		},
		{
			// #2 A→B→C→X→R1 / R2
			name:     "エンティティ未存在: NotFound",
			failAt:   singleRankNotFound,
			wantCode: codes.NotFound,
		},
		{
			// #3 …→C→X→R6 / R7
			name:     "スコア/ポイント未登録: NotFound",
			failAt:   singleRankScoreNone,
			wantCode: codes.NotFound,
		},
		{
			// #4 …→X→R8
			name:     "予期せぬエラー: Internal",
			failAt:   singleRankUnexpected,
			wantCode: codes.Internal,
		},
		{
			// #5 …→X→R9
			// 揮発を「対象が未登録」の NotFound（#3）と取り違えないことを固定する。
			name:          "ErrRankingUnavailable: Unavailable と RetryInfo",
			failAt:        singleRankUnavailable,
			wantCode:      codes.Unavailable,
			wantRetryInfo: true,
		},
		{
			// #6 …→C→D→Z
			name:     "正常系: Result がレスポンスへ変換される",
			failAt:   singleRankNone,
			wantCode: codes.OK,
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

	id := tc.id
	if tc.failAt != singleRankRejectID && id == 0 {
		id = testGuildID
		if kind == kindUser {
			id = testUserID
		}
	}

	if tc.failAt != singleRankRejectID {
		expectSingleRankCall(kind, uc, id, tc.failAt)
	}

	h := newHandler(t, uc)
	ctx := t.Context()

	if kind == kindGuild {
		res, err := h.GetGuildRank(ctx, &rankingv1.GetGuildRankRequest{GuildId: id})
		if tc.wantCode != codes.OK {
			st := assertStatus(t, err, tc.wantCode, wantSingleRankMessage(kind, tc.failAt))
			assertRetryInfo(t, st, tc.wantRetryInfo)
			assert.Nil(t, res, "エラー時はレスポンスを返さない")
			return
		}
		require.NoError(t, err)
		assert.Equal(t, testGuildID, res.GetGuildId())
		assert.Equal(t, "テストギルド", res.GetGuildName())
		assert.Equal(t, int64(9000), res.GetScore())
		assert.Equal(t, int64(1), res.GetRank())
		assert.Equal(t, int64(10), res.GetTotalGuilds())
		return
	}

	res, err := h.GetUserRank(ctx, &rankingv1.GetUserRankRequest{UserId: id})
	if tc.wantCode != codes.OK {
		st := assertStatus(t, err, tc.wantCode, wantSingleRankMessage(kind, tc.failAt))
		assertRetryInfo(t, st, tc.wantRetryInfo)
		assert.Nil(t, res, "エラー時はレスポンスを返さない")
		return
	}
	require.NoError(t, err)
	assert.Equal(t, testUserID, res.GetUserId())
	assert.Equal(t, "テストユーザー", res.GetUserName())
	assert.Equal(t, int64(8000), res.GetPoints())
	assert.Equal(t, int64(3), res.GetRank())
	assert.Equal(t, int64(100), res.GetTotalUsers())
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
		case singleRankUnavailable:
			ucErr = rankingdomain.ErrRankingUnavailable
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
	case singleRankUnavailable:
		ucErr = rankingdomain.ErrRankingUnavailable
	case singleRankNone, singleRankRejectID:
		res = result
	}
	uc.EXPECT().GetUserRank(gomock.Any(), id).Return(res, ucErr)
}

// wantSingleRankMessage は kind ごとに異なる期待文言を返す。
// 「ギルド版とユーザー版で別のメッセージになっていること」を固定する。
func wantSingleRankMessage(kind rankingKind, failAt singleRankFail) string {
	if kind == kindGuild {
		switch failAt {
		case singleRankRejectID:
			return "invalid guild_id"
		case singleRankNotFound:
			return "guild not found"
		case singleRankScoreNone:
			return "score not found"
		case singleRankUnexpected:
			return "internal server error"
		case singleRankUnavailable:
			return "ranking is unavailable"
		case singleRankNone:
			return ""
		}
		return ""
	}
	switch failAt {
	case singleRankRejectID:
		return "invalid user_id"
	case singleRankNotFound:
		return "user not found"
	case singleRankScoreNone:
		return "points not found"
	case singleRankUnexpected:
		return "internal server error"
	case singleRankUnavailable:
		return "ranking is unavailable"
	case singleRankNone:
		return ""
	}
	return ""
}

// ---------------------------------------------------------------------------
// 3. AddUserPoints
// ---------------------------------------------------------------------------

// addRejectAt は AddUserPoints でリクエストが弾かれる地点。
type addRejectAt int

const (
	addReachUsecase addRejectAt = iota
	addRejectUserID
	addRejectReason
)

type addPointsCase struct {
	name string

	// ---- 入力 ----
	// userID が 0 のとき testUserID に差し替える（0 は値域外の入力そのもの）。
	userID int64
	// reason が空文字のとき testReason に差し替える。
	reason string

	// ---- どこで弾かれるか ----
	rejectAt addRejectAt
	// ucErr は usecase に返させるエラー。nil かつ addReachUsecase なら正常系。
	ucErr error

	// ---- 期待結果 ----
	wantCode    codes.Code
	wantMessage string
}

const (
	testPoints = int64(500)
	testReason = "クエストクリア"
)

func TestHandler_AddUserPoints(t *testing.T) {
	t.Parallel()

	// docs/testing/grpc-ranking.md「3. AddUserPoints」の仕様表と 1 対 1 で対応する。
	tests := []addPointsCase{
		{
			// #1 A→B→E1
			name:        "user_id が 0 以下: usecase を呼ばない",
			rejectAt:    addRejectUserID,
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid user_id",
		},
		{
			// #2 A→B→C→E2
			name:        "reason が空: usecase を呼ばない",
			userID:      testUserID,
			rejectAt:    addRejectReason,
			wantCode:    codes.InvalidArgument,
			wantMessage: "reason is required",
		},
		{
			// #3 …→C→D→X→R2
			name:        "usecase が ErrUserNotFound: NotFound",
			ucErr:       rankingdomain.ErrUserNotFound,
			wantCode:    codes.NotFound,
			wantMessage: "user not found",
		},
		{
			// #4 …→D→X→R3
			name:        "usecase が ErrUserNotInGuild: PermissionDenied",
			ucErr:       rankingdomain.ErrUserNotInGuild,
			wantCode:    codes.PermissionDenied,
			wantMessage: "user is not a member of the guild",
		},
		{
			// #5 …→X→R5
			name:        "usecase が ErrInvalidPoints: InvalidArgument",
			ucErr:       rankingdomain.ErrInvalidPoints,
			wantCode:    codes.InvalidArgument,
			wantMessage: "invalid points",
		},
		{
			// #6 …→X→R8
			name:        "usecase が予期せぬエラー: Internal",
			ucErr:       errors.New("unexpected error"),
			wantCode:    codes.Internal,
			wantMessage: "internal server error",
		},
		{
			// #7 …→D→F→Z
			name:     "正常系: 集計フィールドが Result と一致する",
			wantCode: codes.OK,
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

	userID := tc.userID
	if tc.rejectAt != addRejectUserID && userID == 0 {
		userID = testUserID
	}
	reason := tc.reason
	if tc.rejectAt != addRejectReason && reason == "" {
		reason = testReason
	}

	if tc.rejectAt == addReachUsecase {
		result := rankingdomain.UserPointAddResult{}
		if tc.ucErr == nil {
			result = rankingdomain.UserPointAddResult{
				UserID:        testUserID,
				Points:        testPoints,
				PreviousTotal: 1000,
				NewTotal:      1000 + testPoints,
				GuildID:       testGuildID,
			}
		}
		// リクエストの値がそのまま usecase の入力になることを引数で検証する。
		uc.EXPECT().AddUserPoints(gomock.Any(), rankingusecase.AddUserPointsInput{
			UserID: testUserID, Points: testPoints, Reason: testReason,
		}).Return(result, tc.ucErr)
	}

	h := newHandler(t, uc)

	res, err := h.AddUserPoints(t.Context(), &rankingv1.AddUserPointsRequest{
		UserId: userID,
		Points: testPoints,
		Reason: reason,
	})

	if tc.wantCode != codes.OK {
		st := assertStatus(t, err, tc.wantCode, tc.wantMessage)
		assertRetryInfo(t, st, false)
		assert.Nil(t, res, "エラー時はレスポンスを返さない")
		return
	}

	require.NoError(t, err)
	assert.Equal(t, testUserID, res.GetUserId())
	assert.Equal(t, testPoints, res.GetPoints())
	assert.Equal(t, int64(1000), res.GetPreviousTotal())
	assert.Equal(t, int64(1000)+testPoints, res.GetNewTotal())
	assert.Equal(t, testGuildID, res.GetGuildId())
}
