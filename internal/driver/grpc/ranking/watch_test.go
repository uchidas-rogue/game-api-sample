// WatchUserRankings（server streaming）のテスト。
//
// テスト設計（フロー図・テスト仕様表）は docs/testing/grpc-ranking.md §5 が正本。
// 分岐を追加・変更したら、まず図と表を更新してからここを直す。
//
// stream は手書きのフェイクを使う。対象の型は生成コードの型エイリアス
// （grpc.ServerStreamingServer[...]）で、mockgen の //go:generate を書く interface 定義
// ファイルが無く、生成物の管理対象（make/app.mk の GEN_ARTIFACT_PATHS・.testignore）も
// 増えるため。検証したいのは「何を・何回・どの順で送ったか」だけなので、
// bufconn 経由の実 RPC も挟まない（handler_test.go と同じ方針）。
package ranking_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankingv1 "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/gen/rankingv1"
	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	mockranking "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking/mock"
)

// errClientGone は Send がクライアント切断で失敗したことを表すテスト用のエラー。
// gRPC の status を持たない素のエラーにしてあるのは、ハンドラがこれを包まず
// そのまま返すこと（docs/testing/grpc-ranking.md §5 の WE3）を固定するため。
var errClientGone = errors.New("client is gone")

// streamWaitTimeout はストリームの進行を待つ上限。
//
// ローカルでの実測はいずれもミリ秒未満（チャネル1往復ぶん）で、待ちが発生すること自体が
// 不具合（ハンドラが受信・終了しない）を意味する。CI 高負荷時の余裕として 2 桁多く取り、
// 失敗時にテストがハングせず原因つきで落ちるようにする（AGENTS.md §3 Flaky 防止）。
const streamWaitTimeout = 5 * time.Second

// fakeStream が RankingService_WatchUserRankingsServer を満たすことをコンパイル時に検証する。
var _ rankingv1.RankingService_WatchUserRankingsServer = (*fakeStream)(nil)

// fakeStream は server streaming の送信口のフェイク。
//
// Send の内容・回数・順序を記録し、**並行呼び出しを検出して落とす**。
// grpc.ServerStream の SendMsg は並行呼び出しが許されていないため、
// 「受信ループを分割して送信を並行化する」実装への回帰をここで止める。
type fakeStream struct {
	// 使わないメソッド（SetHeader / SendHeader / SetTrailer / SendMsg / RecvMsg）を
	// 埋めるためだけの埋め込み。nil のままなので、呼ばれた場合は nil 参照で落ちる
	// （= このハンドラがそれらを使わないことの表明）。
	grpc.ServerStream

	t   *testing.T
	ctx context.Context
	// failAt は N 回目の Send を失敗させる（0 なら常に成功）。
	failAt int

	// sending は Send の実行中を表す。重なりの検出にだけ使う。
	sending atomic.Bool

	mu   sync.Mutex
	sent []*rankingv1.WatchUserRankingsResponse

	// sends は Send 1 回につき 1 つ値が入る。逐次 push のテストで待ち合わせに使う。
	sends chan struct{}
}

// newFakeStream は failAt 回目の Send を失敗させるフェイクを返す（0 なら常に成功）。
func newFakeStream(t *testing.T, failAt int) *fakeStream {
	t.Helper()
	return &fakeStream{
		t:      t,
		ctx:    t.Context(),
		failAt: failAt,
		sends:  make(chan struct{}, maxRecordedSends),
	}
}

// maxRecordedSends は sends チャネルのバッファ。待ち合わせに使わないケースで
// Send がブロックしないよう、テストが送る最大件数より十分大きく取る。
const maxRecordedSends = 16

// Send は送信内容を記録する。failAt 回目では errClientGone を返す。
func (s *fakeStream) Send(res *rankingv1.WatchUserRankingsResponse) error {
	if !s.sending.CompareAndSwap(false, true) {
		// t.Fatal はテスト goroutine 以外から呼べないため Error を使う。
		s.t.Error("Send が並行に呼ばれた。ServerStream の SendMsg は並行呼び出し不可")
	}
	defer s.sending.Store(false)

	s.mu.Lock()
	s.sent = append(s.sent, res)
	n := len(s.sent)
	s.mu.Unlock()

	select {
	case s.sends <- struct{}{}:
	default:
	}

	if s.failAt == n {
		return errClientGone
	}
	return nil
}

// Context はストリームの ctx を返す。ハンドラがこれを購読へ渡すことを固定する。
func (s *fakeStream) Context() context.Context {
	return s.ctx
}

// responses は送信済みメッセージの写しを返す。
func (s *fakeStream) responses() []*rankingv1.WatchUserRankingsResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// watchResult は i 件目（0 始まり）の push を表すハブの戻り値。
// 全フィールドに連番を入れて、送信順の入れ替わりと変換漏れを検出できるようにする。
func watchResult(i int) rankingusecase.RankingsResult {
	n := int64(i + 1)
	return rankingusecase.RankingsResult{
		Rankings:   []rankingdomain.RankEntry{{Rank: n, ID: n, Name: fmt.Sprintf("%d位", n), Score: n}},
		TotalCount: n,
	}
}

// assertSent は送信内容が push した順と一致するかを検証する。
func assertSent(t *testing.T, stream *fakeStream, want int) {
	t.Helper()

	got := stream.responses()
	require.Len(t, got, want, "Send の回数")
	for i, res := range got {
		n := int64(i + 1)
		assert.Equal(t, n, res.GetTotalCount(), "push した順にそのまま送ること")
		require.Len(t, res.GetRankings(), 1)
		entry := res.GetRankings()[0]
		assert.Equal(t, n, entry.GetRank())
		assert.Equal(t, n, entry.GetId())
		assert.Equal(t, fmt.Sprintf("%d位", n), entry.GetName())
		assert.Equal(t, n, entry.GetScore())
	}
}

// watchCase は WatchUserRankings のテストケース1件。データのみを持つ。
type watchCase struct {
	name string

	// ---- 入力 ----
	limit int32

	// ---- どこで失敗させるか ----
	// callsWatcher が false なら watcher を EXPECT しない（呼ばれれば gomock が落とす）。
	callsWatcher bool
	// watchErr は購読開始で返させるエラー。nil なら購読が成立する。
	watchErr error
	// pushes はハブが購読チャネルへ流す件数。
	pushes int
	// sendErrAt は N 回目の Send を失敗させる（0 なら常に成功）。
	sendErrAt int

	// ---- 期待結果 ----
	// wantCode は codes.OK なら正常系。wantSendErr が true のときは見ない。
	wantCode      codes.Code
	wantMessage   string
	wantRetryInfo bool
	// wantSendErr は Send のエラーが包まれずそのまま返ることを期待する。
	wantSendErr bool
	// wantSends は Send が呼ばれるべき回数。
	wantSends int
}

// TestHandler_WatchUserRankings は購読開始の検証・エラー写像・送信ループを検証する。
func TestHandler_WatchUserRankings(t *testing.T) {
	t.Parallel()

	// docs/testing/grpc-ranking.md「5. ストリーミング」の仕様表と 1 対 1 で対応する。
	// 並び順は図のパスが短い順。
	tests := []watchCase{
		{
			// #1 WA→WB→WE1
			name:        "limit が負値: watcher を呼ばない",
			limit:       -1,
			wantCode:    codes.InvalidArgument,
			wantMessage: "limit must not be negative",
		},
		{
			// #2 WA→WB→WC→WE2
			// ハブ停止は §4 の写像に通さない。RetryInfo を載せないことで
			// ZSet 揮発（#4）と取り違えていないことも同時に固定する。
			name:         "購読開始が ErrWatcherStopped: Unavailable（RetryInfo 無し）",
			limit:        10,
			callsWatcher: true,
			watchErr:     rankingusecase.ErrWatcherStopped,
			wantCode:     codes.Unavailable,
			wantMessage:  "ranking watch is unavailable",
		},
		{
			// #3 WA→WB→WC→WX→R8
			name:         "購読開始が予期せぬエラー: Internal",
			limit:        10,
			callsWatcher: true,
			watchErr:     errors.New("subscribe failed"),
			wantCode:     codes.Internal,
			wantMessage:  "internal server error",
		},
		{
			// #4 …→WC→WX→R9
			// ハブは初回 fetch の失敗をラップして返す（ranking-watch.md §1）。
			// 判定が errors.Is で行われていることも併せて固定する。
			name:          "購読開始が ErrRankingUnavailable: Unavailable と RetryInfo",
			limit:         10,
			callsWatcher:  true,
			watchErr:      fmt.Errorf("initial user rankings: %w", rankingdomain.ErrRankingUnavailable),
			wantCode:      codes.Unavailable,
			wantMessage:   "ranking is unavailable",
			wantRetryInfo: true,
		},
		{
			// #5 …→WC→WD→WS1→WE3
			// 残り 1 件を送らずに止まること（= 送信ループを抜けること）も見る。
			name:         "初回の Send が失敗: エラーをそのまま返して打ち切る",
			limit:        10,
			callsWatcher: true,
			pushes:       2,
			sendErrAt:    1,
			wantSendErr:  true,
			wantSends:    1,
		},
		{
			// #6 …→WS1→WU→WS2→WE3
			name:         "2 件目の Send が失敗: エラーをそのまま返して打ち切る",
			limit:        10,
			callsWatcher: true,
			pushes:       3,
			sendErrAt:    2,
			wantSendErr:  true,
			wantSends:    2,
		},
		{
			// #7 …→WU→WS2→WK→WZ
			// 既定値の適用はハブ（NormalizeLimit）の責務なので、ハンドラは生値を渡す。
			// 表の 1 行を「未設定（0）」「明示指定 + 複数 push」の 2 ケースへ展開している。
			name:         "正常系: 未設定なら 0 をそのまま渡す",
			callsWatcher: true,
			pushes:       1,
			wantCode:     codes.OK,
			wantSends:    1,
		},
		{
			name:         "正常系: 複数回の push が順に Send されクローズで終わる",
			limit:        10,
			callsWatcher: true,
			pushes:       3,
			wantCode:     codes.OK,
			wantSends:    3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runWatchCase(t, tt)
		})
	}
}

func runWatchCase(t *testing.T, tc watchCase) {
	t.Helper()

	ctrl := gomock.NewController(t)
	uc := mockranking.NewMockUsecase(ctrl)
	w := mockranking.NewMockWatcher(ctrl)
	stream := newFakeStream(t, tc.sendErrAt)

	if tc.callsWatcher {
		// 値を詰めてから閉じたチャネルを返す。送信の順序と件数はこれで決まり、
		// 待ち合わせが要らない（時間依存を持ち込まない）。受け取った端から送るか
		// どうかは TestHandler_WatchUserRankings_逐次push_受信のたびに送る が見る。
		ch := make(chan rankingusecase.RankingsResult, tc.pushes)
		for i := range tc.pushes {
			ch <- watchResult(i)
		}
		close(ch)

		w.EXPECT().WatchUserRankings(gomock.Any(), int(tc.limit)).
			DoAndReturn(func(ctx context.Context, _ int) (<-chan rankingusecase.RankingsResult, error) {
				// stream の ctx をそのまま渡していること。別の ctx を渡すと、
				// クライアント切断時にハブ側の購読者が解除されない。
				assert.Equal(t, stream.Context(), ctx, "購読には stream.Context() をそのまま渡すこと")
				if tc.watchErr != nil {
					return nil, tc.watchErr
				}
				return ch, nil
			})
	}

	h := rankinghandler.NewHandler(uc, w, slogtest.NewLogger(t, nil))

	err := h.WatchUserRankings(&rankingv1.WatchUserRankingsRequest{Limit: tc.limit}, stream)

	switch {
	case tc.wantSendErr:
		require.ErrorIs(t, err, errClientGone)
		assert.Equal(t, errClientGone, err, "Send のエラーは status に包まずそのまま返す")
	case tc.wantCode != codes.OK:
		st := assertStatus(t, err, tc.wantCode, tc.wantMessage)
		assertRetryInfo(t, st, tc.wantRetryInfo)
	default:
		require.NoError(t, err)
	}

	assertSent(t, stream, tc.wantSends)
}

// TestHandler_WatchUserRankings_逐次push_受信のたびに送る は、ハブが 1 件ずつ流す
// 実際の使われ方を再現する。
//
// 仕様表のケース 7 と同じパスを通るが手続きが違う（バッファ無しのチャネルで、
// ハンドラが受信するまで push が返らない）。「全件受け取ってから送る」実装に変わると
// 最初の Send まで進めず、この関数だけが落ちる。
// ハンドラを別 goroutine で走らせるため、fakeStream の並行 Send 検出もここで効く。
func TestHandler_WatchUserRankings_逐次push_受信のたびに送る(t *testing.T) {
	t.Parallel()

	const (
		limit  = int32(10)
		pushes = 2
	)

	ctrl := gomock.NewController(t)
	uc := mockranking.NewMockUsecase(ctrl)
	w := mockranking.NewMockWatcher(ctrl)
	stream := newFakeStream(t, 0)

	ch := make(chan rankingusecase.RankingsResult)
	var recv <-chan rankingusecase.RankingsResult = ch
	w.EXPECT().WatchUserRankings(gomock.Any(), int(limit)).Return(recv, nil)

	h := rankinghandler.NewHandler(uc, w, slogtest.NewLogger(t, nil))

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.WatchUserRankings(&rankingv1.WatchUserRankingsRequest{Limit: limit}, stream)
	}()

	for i := range pushes {
		select {
		case ch <- watchResult(i):
		case <-time.After(streamWaitTimeout):
			t.Fatalf("%d 件目の push をハンドラが受信しない", i+1)
		}
		select {
		case <-stream.sends:
		case <-time.After(streamWaitTimeout):
			t.Fatalf("%d 件目の push が Send されない", i+1)
		}
	}
	close(ch)

	select {
	case err := <-errCh:
		require.NoError(t, err, "チャネルのクローズは正常終了")
	case <-time.After(streamWaitTimeout):
		t.Fatal("チャネルのクローズでハンドラが終了しない")
	}

	assertSent(t, stream, pushes)
}
