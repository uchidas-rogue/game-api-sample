// Package ranking_test は ranking ユースケースの外部テストパッケージ。
// 公開 API のみを対象とし、モックを用いてデータベース・Redis 接続をバイパスする。
//
// テスト設計（フロー図・テスト仕様表）は docs/testing/ranking.md にある。
// 分岐を追加・変更したら、まず図と表を更新してからここを直す。
//
// 境界値（Points の範囲・limit/offset の正規化）は domain の責務であり、
// internal/domain/ranking/constants_test.go が正本。ここでは重ねて検証しない。
package ranking_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
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

const (
	testUserID  = int64(1)
	testGuildID = int64(10)
)

// mocks は Usecase の依存一式。組み立て手続きをテーブルから追い出すための入れ物。
type mocks struct {
	tx       *mockshared.MockTransactor
	repo     *mockranking.MockRepository
	store    *mockranking.MockRankingStore
	outbox   *mockoutbox.MockRepository
	notifier *mockoutbox.MockNotifier
}

func newMocks(ctrl *gomock.Controller) *mocks {
	return &mocks{
		tx:       mockshared.NewMockTransactor(ctrl),
		repo:     mockranking.NewMockRepository(ctrl),
		store:    mockranking.NewMockRankingStore(ctrl),
		outbox:   mockoutbox.NewMockRepository(ctrl),
		notifier: mockoutbox.NewMockNotifier(ctrl),
	}
}

func (m *mocks) usecase(t *testing.T) ranking.Usecase {
	t.Helper()
	return m.usecaseWithLogger(slogtest.NewLogger(t, nil))
}

// usecaseWithLogger はログ出力そのものを検証したいケース用。
// 通常は usecase(t) を使い、ログは t.Log へ流す。
func (m *mocks) usecaseWithLogger(logger *slog.Logger) ranking.Usecase {
	return ranking.NewUsecase(m.tx, m.repo, m.store, m.outbox, m.notifier, logger)
}

// expectDoInTxRunsFn は DoInTx が fn を実際に実行する設定にする。
// トランザクション境界そのものの契約は docs/testing/transaction-boundary.md が
// Transactor 自身のテストで担保しているため、ここでは境界をモックしてよい。
func expectDoInTxRunsFn(tx *mockshared.MockTransactor) {
	tx.EXPECT().
		DoInTx(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, fn func(shared.Tx) error) error {
			return fn(nil)
		})
}

// ---------------------------------------------------------------------------
// 1. 読み取り系: GetGuildRankings / GetUserRankings
// ---------------------------------------------------------------------------

// rankingKind はギルド版・ユーザー版の区別。
// 2 メソッドは構造が同一なので **同じケース表を両方に流す**。
// これにより「片方にだけケースがある」非対称な状態が構造的に起きなくなる。
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

// rankStep は読み取り系フローで失敗させる地点。
type rankStep int

const (
	rankStepNone rankStep = iota
	rankStepGetRankings
	rankStepListNames
	rankStepTotalCount
)

// rankingsCase は GetXxxRankings のテストケース1件。データのみを持つ。
type rankingsCase struct {
	name  string
	input ranking.GetRankingsInput
	// entries は store が返すランキング。空スライスなら名前解決をスキップする経路になる。
	entries    []rankingdomain.RankEntry
	totalCount int64
	failAt     rankStep
	// wantNames は名前解決後に期待する Name の並び（正常系のみ）。
	wantNames []string
	// wantStoreArgs が true のとき、store に渡る offset/limit が正規化後の値であることを検証する。
	wantNormalizedArgs bool
	// nameMissingForLastID が true のとき、名前解決の戻り値から最後の entry の ID を落とす。
	// Redis の ZSet と MySQL がずれた状態（削除済みユーザー/ギルドが ZSet に残っている等）を模す。
	nameMissingForLastID bool
}

func TestUsecase_GetRankings(t *testing.T) {
	t.Parallel()

	errStore := errors.New("store error")

	entries := []rankingdomain.RankEntry{
		{Rank: 1, ID: 100, Score: 500},
		{Rank: 2, ID: 200, Score: 300},
	}

	// docs/testing/ranking.md「1. 読み取り系」のテスト仕様表と 1 対 1 で対応する。
	// 並び順は図のパスが短い順。
	tests := []rankingsCase{
		{
			// #1 A→B→C→E1
			name:    "store の取得がエラー: 名前解決も total count も呼ばれない",
			entries: entries,
			failAt:  rankStepGetRankings,
		},
		{
			// #2 A→B→C→D→F→Z
			name:       "entries が空: 名前解決を呼ばない",
			entries:    []rankingdomain.RankEntry{},
			totalCount: 0,
			wantNames:  []string{},
		},
		{
			// #3 …→D→G→E2
			name:    "名前解決がエラー: total count が呼ばれない",
			entries: entries,
			failAt:  rankStepListNames,
		},
		{
			// #4 …→G→H→H2→F→E3
			name:    "total count がエラー",
			entries: entries,
			failAt:  rankStepTotalCount,
		},
		{
			// #5 …→H→H2→F→Z
			name:       "正常系: 名前がマージされる",
			entries:    entries,
			totalCount: 42,
			wantNames:  []string{"名前100", "名前200"},
		},
		{
			// #6 5 と同一パス。正規化後の値が store へ渡る「結線」だけを検証する。
			// 正規化そのものの正しさは domain の NormalizeLimit / NormalizeOffset が担保する。
			name:               "正規化した offset/limit が store に渡る",
			input:              ranking.GetRankingsInput{Limit: 0, Offset: -5},
			entries:            entries,
			totalCount:         42,
			wantNames:          []string{"名前100", "名前200"},
			wantNormalizedArgs: true,
		},
		{
			// #7 …→H→H3→F→Z
			// 名前が引けなくてもエントリを落とさない（順位と件数が食い違わないため）。
			name:                 "名前マップに ID が無い: Name は空のままエントリは除外しない",
			entries:              entries,
			totalCount:           42,
			wantNames:            []string{"名前100", ""},
			nameMissingForLastID: true,
		},
	}

	for _, kind := range []rankingKind{kindGuild, kindUser} {
		for _, tt := range tests {
			t.Run(kind.String()+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				runRankingsCase(t, kind, tt, errStore)
			})
		}
	}
}

func runRankingsCase(t *testing.T, kind rankingKind, tc rankingsCase, errStore error) {
	t.Helper()

	ctrl := gomock.NewController(t)
	m := newMocks(ctrl)

	wantOffset := rankingdomain.NormalizeOffset(tc.input.Offset)
	wantLimit := rankingdomain.NormalizeLimit(tc.input.Limit)
	if tc.wantNormalizedArgs {
		// 正規化されていなければ生値が渡るので、この2つは必ず異なる値になっている。
		require.NotEqual(t, tc.input.Limit, wantLimit, "テストの前提: 生値と正規化後が異なること")
		require.NotEqual(t, tc.input.Offset, wantOffset, "テストの前提: 生値と正規化後が異なること")
	}

	expectRankingsCalls(kind, m, tc, errStore, wantOffset, wantLimit)

	uc := m.usecase(t)
	var (
		res ranking.RankingsResult
		err error
	)
	if kind == kindGuild {
		res, err = uc.GetGuildRankings(context.Background(), tc.input)
	} else {
		res, err = uc.GetUserRankings(context.Background(), tc.input)
	}

	if tc.failAt != rankStepNone {
		require.Error(t, err)
		assert.ErrorIs(t, err, errStore)
		assert.Equal(t, ranking.RankingsResult{}, res, "エラー時はゼロ値を返す")
		return
	}

	require.NoError(t, err)
	assert.Equal(t, tc.totalCount, res.TotalCount)
	require.Len(t, res.Rankings, len(tc.wantNames))
	for i, want := range tc.wantNames {
		assert.Equal(t, want, res.Rankings[i].Name, "%d 件目の名前", i+1)
	}
}

func expectRankingsCalls(kind rankingKind, m *mocks, tc rankingsCase, errStore error, wantOffset, wantLimit int) {
	// C: ランキング取得
	if tc.failAt == rankStepGetRankings {
		if kind == kindGuild {
			m.store.EXPECT().GetGuildRankings(gomock.Any(), wantOffset, wantLimit).Return(nil, errStore)
		} else {
			m.store.EXPECT().GetUserRankings(gomock.Any(), wantOffset, wantLimit).Return(nil, errStore)
		}
		return
	}
	// entries はケース間で共有しないようコピーを渡す（usecase が Name を書き換えるため）。
	entries := make([]rankingdomain.RankEntry, len(tc.entries))
	copy(entries, tc.entries)
	if kind == kindGuild {
		m.store.EXPECT().GetGuildRankings(gomock.Any(), wantOffset, wantLimit).Return(entries, nil)
	} else {
		m.store.EXPECT().GetUserRankings(gomock.Any(), wantOffset, wantLimit).Return(entries, nil)
	}

	// D: entries が空なら名前解決はスキップされる
	if len(entries) > 0 {
		ids := make([]int64, len(entries))
		for i, e := range entries {
			ids[i] = e.ID
		}
		if tc.failAt == rankStepListNames {
			if kind == kindGuild {
				m.repo.EXPECT().ListGuildsByIDs(gomock.Any(), nil, ids).Return(nil, errStore)
			} else {
				m.repo.EXPECT().ListUsersByIDs(gomock.Any(), nil, ids).Return(nil, errStore)
			}
			return
		}
		// H: 名前マップに ID が無い分岐を通すため、指定時は末尾の ID を落とす。
		resolved := ids
		if tc.nameMissingForLastID {
			resolved = ids[:len(ids)-1]
		}
		if kind == kindGuild {
			guilds := make(map[int64]rankingdomain.Guild, len(resolved))
			for _, id := range resolved {
				guilds[id] = rankingdomain.Guild{ID: id, Name: fmt.Sprintf("名前%d", id)}
			}
			m.repo.EXPECT().ListGuildsByIDs(gomock.Any(), nil, ids).Return(guilds, nil)
		} else {
			users := make(map[int64]string, len(resolved))
			for _, id := range resolved {
				users[id] = fmt.Sprintf("名前%d", id)
			}
			m.repo.EXPECT().ListUsersByIDs(gomock.Any(), nil, ids).Return(users, nil)
		}
	}

	// F: 総数取得
	if tc.failAt == rankStepTotalCount {
		if kind == kindGuild {
			m.store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), errStore)
		} else {
			m.store.EXPECT().GetUserTotalCount(gomock.Any()).Return(int64(0), errStore)
		}
		return
	}
	if kind == kindGuild {
		m.store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(tc.totalCount, nil)
	} else {
		m.store.EXPECT().GetUserTotalCount(gomock.Any()).Return(tc.totalCount, nil)
	}
}

// ---------------------------------------------------------------------------
// 2. 単一順位取得: GetGuildRank / GetUserRank
// ---------------------------------------------------------------------------

// singleRankStep は単一順位取得フローで失敗させる地点。
type singleRankStep int

const (
	singleStepNone         singleRankStep = iota
	singleStepGetEntity                   // repo.GetGuild / GetUser
	singleStepGetScore                    // store.GetGuildScore / GetUserPoints
	singleStepScoreMissing                // exists = false
	singleStepGetRank
	singleStepTotalCount
)

type singleRankCase struct {
	name      string
	failAt    singleRankStep
	wantErrIs error
}

func TestUsecase_GetRank(t *testing.T) {
	t.Parallel()

	errStore := errors.New("store error")
	errRepo := errors.New("repo error")

	// docs/testing/ranking.md「2. 単一順位取得」の仕様表と 1 対 1。
	// 並び順は図のパスが短い順。
	tests := []singleRankCase{
		// #1 A→B→E1
		{name: "repo の取得がエラー: store が一切呼ばれない", failAt: singleStepGetEntity, wantErrIs: errRepo},
		// #2 A→B→C→E2
		{name: "スコア/ポイント取得がエラー", failAt: singleStepGetScore, wantErrIs: errStore},
		// #3 …→C→D→E3。wantErrIs は kind ごとに異なるためランナーで決める。
		{name: "スコア/ポイント未登録", failAt: singleStepScoreMissing},
		// #4 …→D→F→E4
		{name: "rank 取得がエラー", failAt: singleStepGetRank, wantErrIs: errStore},
		// #5 …→F→G→E5
		{name: "total count がエラー", failAt: singleStepTotalCount, wantErrIs: errStore},
		// #6 …→G→Z
		{name: "正常系: 名前・スコア・順位・総数が揃う", failAt: singleStepNone},
	}

	for _, kind := range []rankingKind{kindGuild, kindUser} {
		for _, tt := range tests {
			t.Run(kind.String()+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				runSingleRankCase(t, kind, tt, errRepo, errStore)
			})
		}
	}
}

func runSingleRankCase(t *testing.T, kind rankingKind, tc singleRankCase, errRepo, errStore error) {
	t.Helper()

	const (
		wantScore = int64(500)
		wantRank  = int64(3)
		wantTotal = int64(99)
		wantName  = "テスト対象"
	)

	ctrl := gomock.NewController(t)
	m := newMocks(ctrl)
	id := testGuildID
	if kind == kindUser {
		id = testUserID
	}

	expectSingleRankCalls(kind, m, tc, id, errRepo, errStore, wantScore, wantRank, wantTotal, wantName)

	uc := m.usecase(t)

	wantErrIs := tc.wantErrIs
	if tc.failAt == singleStepScoreMissing {
		wantErrIs = rankingdomain.ErrScoreNotFound
		if kind == kindUser {
			wantErrIs = rankingdomain.ErrPointsNotFound
		}
	}

	if kind == kindGuild {
		res, err := uc.GetGuildRank(context.Background(), id)
		if tc.failAt != singleStepNone {
			require.Error(t, err)
			assert.ErrorIs(t, err, wantErrIs)
			assert.Equal(t, rankingdomain.GuildRankResult{}, res, "エラー時はゼロ値を返す")
			return
		}
		require.NoError(t, err)
		assert.Equal(t, rankingdomain.GuildRankResult{
			GuildID: id, GuildName: wantName, Score: wantScore, Rank: wantRank, TotalGuilds: wantTotal,
		}, res)
		return
	}

	res, err := uc.GetUserRank(context.Background(), id)
	if tc.failAt != singleStepNone {
		require.Error(t, err)
		assert.ErrorIs(t, err, wantErrIs)
		assert.Equal(t, rankingdomain.UserRankResult{}, res, "エラー時はゼロ値を返す")
		return
	}
	require.NoError(t, err)
	assert.Equal(t, rankingdomain.UserRankResult{
		UserID: id, UserName: wantName, Points: wantScore, Rank: wantRank, TotalUsers: wantTotal,
	}, res)
}

func expectSingleRankCalls(
	kind rankingKind, m *mocks, tc singleRankCase, id int64,
	errRepo, errStore error, wantScore, wantRank, wantTotal int64, wantName string,
) {
	// B: 名前取得
	if tc.failAt == singleStepGetEntity {
		if kind == kindGuild {
			m.repo.EXPECT().GetGuild(gomock.Any(), nil, id).Return(rankingdomain.Guild{}, errRepo)
		} else {
			m.repo.EXPECT().GetUser(gomock.Any(), nil, id).Return("", errRepo)
		}
		return
	}
	if kind == kindGuild {
		m.repo.EXPECT().GetGuild(gomock.Any(), nil, id).Return(rankingdomain.Guild{ID: id, Name: wantName}, nil)
	} else {
		m.repo.EXPECT().GetUser(gomock.Any(), nil, id).Return(wantName, nil)
	}

	// C: スコア/ポイント取得
	if tc.failAt == singleStepGetScore {
		if kind == kindGuild {
			m.store.EXPECT().GetGuildScore(gomock.Any(), id).Return(int64(0), false, errStore)
		} else {
			m.store.EXPECT().GetUserPoints(gomock.Any(), id).Return(int64(0), false, errStore)
		}
		return
	}
	// D: exists 判定
	if tc.failAt == singleStepScoreMissing {
		if kind == kindGuild {
			m.store.EXPECT().GetGuildScore(gomock.Any(), id).Return(int64(0), false, nil)
		} else {
			m.store.EXPECT().GetUserPoints(gomock.Any(), id).Return(int64(0), false, nil)
		}
		return
	}
	if kind == kindGuild {
		m.store.EXPECT().GetGuildScore(gomock.Any(), id).Return(wantScore, true, nil)
	} else {
		m.store.EXPECT().GetUserPoints(gomock.Any(), id).Return(wantScore, true, nil)
	}

	// F: 順位取得
	if tc.failAt == singleStepGetRank {
		if kind == kindGuild {
			m.store.EXPECT().GetGuildRank(gomock.Any(), id).Return(int64(0), errStore)
		} else {
			m.store.EXPECT().GetUserRank(gomock.Any(), id).Return(int64(0), errStore)
		}
		return
	}
	if kind == kindGuild {
		m.store.EXPECT().GetGuildRank(gomock.Any(), id).Return(wantRank, nil)
	} else {
		m.store.EXPECT().GetUserRank(gomock.Any(), id).Return(wantRank, nil)
	}

	// G: 総数取得
	if tc.failAt == singleStepTotalCount {
		if kind == kindGuild {
			m.store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(int64(0), errStore)
		} else {
			m.store.EXPECT().GetUserTotalCount(gomock.Any()).Return(int64(0), errStore)
		}
		return
	}
	if kind == kindGuild {
		m.store.EXPECT().GetGuildTotalCount(gomock.Any()).Return(wantTotal, nil)
	} else {
		m.store.EXPECT().GetUserTotalCount(gomock.Any()).Return(wantTotal, nil)
	}
}

// ---------------------------------------------------------------------------
// 3. AddUserPoints
// ---------------------------------------------------------------------------

// addStep は AddUserPoints のフロー上で失敗させる地点。
type addStep int

const (
	addStepNone addStep = iota
	addStepGetUser
	addStepGetGuildID
	addStepGetUserPoints
	addStepGetGuildScore
	addStepInsertUserHistory
	addStepInsertGuildHistory
	addStepIncrUserPoints
	addStepIncrGuildScore
	addStepInsertEvent
)

type addPointsCase struct {
	name string

	// ---- 入力 ----
	// points は 0 のとき有効な既定値を使う。IsValidScore の境界は domain の責務。
	points int64

	// ---- どこで失敗させるか ----
	failAt addStep
	// invalidPoints が true なら IsValidScore=false の値を渡す。
	invalidPoints bool
	// firstTimeUser / firstTimeGuild は現在値の取得が「未登録」を返す経路。
	firstTimeUser  bool
	firstTimeGuild bool
	// notifyFails はコミット後の通知が失敗する経路。
	notifyFails bool

	// ---- 期待結果 ----
	wantErrIs          error
	wantPreviousTotal  int64
	wantGuildPrevTotal int64
}

func TestUsecase_AddUserPoints(t *testing.T) {
	t.Parallel()

	errRepo := errors.New("repo error")
	errNotify := errors.New("notify error")

	// docs/testing/ranking.md「3. AddUserPoints」の仕様表と 1 対 1。
	tests := []addPointsCase{
		{
			// #1 A→B→E1
			name:          "IsValidScore が false: DoInTx に入らない",
			invalidPoints: true,
			wantErrIs:     rankingdomain.ErrInvalidPoints,
		},
		{
			// #2 A→B→C→D→E3
			name:      "GetUser がエラー: 以降の repo が呼ばれない",
			failAt:    addStepGetUser,
			wantErrIs: rankingdomain.ErrUserNotFound,
		},
		{
			// #3 …→D→F→E4
			name:      "GetUserGuildID がエラー（ギルド未所属）",
			failAt:    addStepGetGuildID,
			wantErrIs: rankingdomain.ErrUserNotInGuild,
		},
		{
			// #4 …→F→G→E5
			name:      "GetUserPoints が予期せぬエラー",
			failAt:    addStepGetUserPoints,
			wantErrIs: errRepo,
		},
		{
			// #5 …→G→I→E6
			name:      "GetGuildScore が予期せぬエラー",
			failAt:    addStepGetGuildScore,
			wantErrIs: errRepo,
		},
		{
			// #6 …→I→K→E7
			name:      "InsertUserPointHistory がエラー",
			failAt:    addStepInsertUserHistory,
			wantErrIs: errRepo,
		},
		{
			// #7 …→K→L→E8
			name:      "InsertGuildScoreHistory がエラー",
			failAt:    addStepInsertGuildHistory,
			wantErrIs: errRepo,
		},
		{
			// #8 …→L→M→E9
			name:      "IncrementUserPoints がエラー",
			failAt:    addStepIncrUserPoints,
			wantErrIs: errRepo,
		},
		{
			// #9 …→M→N→E10
			name:      "IncrementGuildScore がエラー",
			failAt:    addStepIncrGuildScore,
			wantErrIs: errRepo,
		},
		{
			// #10 …→N→O→E11
			name:      "InsertEvent がエラー: Notify が呼ばれない",
			failAt:    addStepInsertEvent,
			wantErrIs: errRepo,
		},
		{
			// #11 …→H2→J2→…→Q→Z
			name:               "正常系: 既存ポイント・既存ギルドスコアあり",
			wantPreviousTotal:  1000,
			wantGuildPrevTotal: 5000,
		},
		{
			// #12 …→H→J→…→Z
			name:           "正常系: 初回ユーザー かつ 初回ギルド",
			firstTimeUser:  true,
			firstTimeGuild: true,
		},
		{
			// #13 …→Q→R→Z
			name:               "Notify が失敗してもリクエストは成功する",
			notifyFails:        true,
			wantPreviousTotal:  1000,
			wantGuildPrevTotal: 5000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runAddPointsCase(t, tt, errRepo, errNotify)
		})
	}
}

func runAddPointsCase(t *testing.T, tc addPointsCase, errRepo, errNotify error) {
	t.Helper()

	points := tc.points
	if points == 0 {
		points = 100
	}
	if tc.invalidPoints {
		points = -1
	}
	input := ranking.AddUserPointsInput{UserID: testUserID, Points: points, Reason: "test"}

	ctrl := gomock.NewController(t)
	m := newMocks(ctrl)
	expectAddPointsCalls(m, tc, input, errRepo, errNotify)

	// 通知失敗を握り潰していないことの観測点は WARN ログだけなので、
	// このケースに限りログを捕捉できる logger に差し替える。
	var logBuf bytes.Buffer
	uc := m.usecase(t)
	if tc.notifyFails {
		uc = m.usecaseWithLogger(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	}

	res, err := uc.AddUserPoints(context.Background(), input)

	if tc.wantErrIs != nil {
		require.Error(t, err)
		assert.ErrorIs(t, err, tc.wantErrIs)
		assert.Equal(t, rankingdomain.UserPointAddResult{}, res, "エラー時はゼロ値を返す")
		return
	}

	require.NoError(t, err)
	if tc.notifyFails {
		assert.Contains(t, logBuf.String(), `"level":"WARN"`, "通知失敗は WARN ログに残ること")
		assert.Contains(t, logBuf.String(), "outbox notify failed")
		assert.Contains(t, logBuf.String(), errNotify.Error())
	}
	assert.Equal(t, rankingdomain.UserPointAddResult{
		UserID:             testUserID,
		Points:             points,
		PreviousTotal:      tc.wantPreviousTotal,
		NewTotal:           tc.wantPreviousTotal + points,
		GuildID:            testGuildID,
		GuildPreviousTotal: tc.wantGuildPrevTotal,
		GuildNewTotal:      tc.wantGuildPrevTotal + points,
	}, res)
}

func expectAddPointsCalls(m *mocks, tc addPointsCase, input ranking.AddUserPointsInput, errRepo, errNotify error) {
	// B: 入力が不正なら境界に入らない
	if tc.invalidPoints {
		return
	}
	expectDoInTxRunsFn(m.tx)

	// D: GetUser
	if tc.failAt == addStepGetUser {
		m.repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), input.UserID).Return("", rankingdomain.ErrUserNotFound)
		return
	}
	m.repo.EXPECT().GetUser(gomock.Any(), gomock.Any(), input.UserID).Return("tester", nil)

	// F: GetUserGuildID
	if tc.failAt == addStepGetGuildID {
		m.repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), input.UserID).Return(int64(0), rankingdomain.ErrUserNotInGuild)
		return
	}
	m.repo.EXPECT().GetUserGuildID(gomock.Any(), gomock.Any(), input.UserID).Return(testGuildID, nil)

	// G: 個人ポイント現在値。ErrPointsNotFound は初回ユーザーとして正常扱いされる。
	switch {
	case tc.failAt == addStepGetUserPoints:
		m.repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), input.UserID).Return(rankingdomain.UserPoint{}, errRepo)
		return
	case tc.firstTimeUser:
		m.repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), input.UserID).
			Return(rankingdomain.UserPoint{}, rankingdomain.ErrPointsNotFound)
	default:
		m.repo.EXPECT().GetUserPoints(gomock.Any(), gomock.Any(), input.UserID).
			Return(rankingdomain.UserPoint{UserID: input.UserID, Points: tc.wantPreviousTotal}, nil)
	}

	// I: ギルドスコア現在値。ErrScoreNotFound は初回ギルドとして正常扱いされる。
	switch {
	case tc.failAt == addStepGetGuildScore:
		m.repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), testGuildID).Return(rankingdomain.GuildScore{}, errRepo)
		return
	case tc.firstTimeGuild:
		m.repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), testGuildID).
			Return(rankingdomain.GuildScore{}, rankingdomain.ErrScoreNotFound)
	default:
		m.repo.EXPECT().GetGuildScore(gomock.Any(), gomock.Any(), testGuildID).
			Return(rankingdomain.GuildScore{GuildID: testGuildID, Score: tc.wantGuildPrevTotal}, nil)
	}

	// K: 個人ポイント履歴
	if tc.failAt == addStepInsertUserHistory {
		m.repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), input.UserID, input.Points, input.Reason).Return(errRepo)
		return
	}
	m.repo.EXPECT().InsertUserPointHistory(gomock.Any(), gomock.Any(), input.UserID, input.Points, input.Reason).Return(nil)

	// L: ギルドスコア履歴
	if tc.failAt == addStepInsertGuildHistory {
		m.repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), testGuildID, input.UserID, input.Points).Return(errRepo)
		return
	}
	m.repo.EXPECT().InsertGuildScoreHistory(gomock.Any(), gomock.Any(), testGuildID, input.UserID, input.Points).Return(nil)

	// M: 個人ポイント累計加算
	if tc.failAt == addStepIncrUserPoints {
		m.repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), input.UserID, input.Points).Return(errRepo)
		return
	}
	m.repo.EXPECT().IncrementUserPoints(gomock.Any(), gomock.Any(), input.UserID, input.Points).Return(nil)

	// N: ギルドスコア累計加算
	if tc.failAt == addStepIncrGuildScore {
		m.repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), testGuildID, input.Points).Return(errRepo)
		return
	}
	m.repo.EXPECT().IncrementGuildScore(gomock.Any(), gomock.Any(), testGuildID, input.Points).Return(nil)

	// O: outbox イベント登録。payload は domain のマーシャラで組み立てた値と一致すること。
	wantPayload := outboxdomain.MarshalRankingScoreAddedPayload(outboxdomain.RankingScoreAddedPayload{
		UserID:  input.UserID,
		GuildID: testGuildID,
		Points:  input.Points,
	})
	if tc.failAt == addStepInsertEvent {
		m.outbox.EXPECT().
			InsertEvent(gomock.Any(), gomock.Any(), outboxdomain.EventTypeRankingScoreAdded, wantPayload).
			Return(uint64(0), errRepo)
		return
	}
	m.outbox.EXPECT().
		InsertEvent(gomock.Any(), gomock.Any(), outboxdomain.EventTypeRankingScoreAdded, wantPayload).
		Return(uint64(1), nil)

	// Q: コミット後の通知。失敗してもリクエストは成功させる。
	if tc.notifyFails {
		m.notifier.EXPECT().Notify(gomock.Any()).Return(errNotify)
		return
	}
	m.notifier.EXPECT().Notify(gomock.Any()).Return(nil)
}
