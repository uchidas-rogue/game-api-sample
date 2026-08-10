// Package server_test は server パッケージの外部テスト。
// New のミドルウェア動作と Run のグレースフルシャットダウン・起動エラーを検証する。
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/server"
)

// newTestLogger は bytes.Buffer へ JSON 形式でログを書き込む *slog.Logger を返す。
// テスト内でログ出力内容をアサーションするために使用する。
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h)
}

// parseLogLines は buf に書かれた改行区切りの JSON ログ行を map のスライスとして返す。
func parseLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var result []map[string]any
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for {
		var m map[string]any
		// 末尾に到達すると io.EOF が返る。デコードできた行だけを積む。
		if err := dec.Decode(&m); err != nil {
			break
		}
		result = append(result, m)
	}
	return result
}

// TestNew_RequestLoggingMiddleware は New が返す Echo インスタンスのミドルウェアを検証する。
func TestNew_RequestLoggingMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		handler        echo.HandlerFunc
		wantStatusCode int
		wantLogLevel   string
		wantError      bool
	}{
		{
			name: "正常系_200_Info ログが出力される",
			handler: func(c echo.Context) error {
				return c.String(http.StatusOK, "ok")
			},
			wantStatusCode: http.StatusOK,
			wantLogLevel:   "INFO",
			wantError:      false,
		},
		{
			name: "エラー系_HTTPError500_Error ログが出力される",
			handler: func(c echo.Context) error {
				return echo.NewHTTPError(http.StatusInternalServerError, "boom")
			},
			wantStatusCode: http.StatusInternalServerError,
			wantLogLevel:   "ERROR",
			wantError:      true,
		},
		{
			name: "パニック系_Recover が拾って 500 を返す",
			handler: func(c echo.Context) error {
				panic("unexpected panic")
			},
			wantStatusCode: http.StatusInternalServerError,
			wantLogLevel:   "", // Recover ミドルウェアが処理するためアクセスログの level は問わない
			wantError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logger := newTestLogger(&buf)
			e := server.New(logger)

			e.GET("/test", tt.handler)

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatusCode, rec.Code)

			if tt.wantLogLevel == "" {
				return
			}

			// アクセスログの出力を検証
			lines := parseLogLines(t, &buf)
			// request ログを探す
			var requestLog map[string]any
			for _, l := range lines {
				if l["msg"] == "request" {
					requestLog = l
					break
				}
			}
			require.NotNil(t, requestLog, "request ログが出力されていること")
			assert.Equal(t, tt.wantLogLevel, requestLog["level"])

			// 共通属性の検証
			assert.NotEmpty(t, requestLog["request_id"], "request_id が空でないこと")
			assert.Equal(t, http.MethodGet, requestLog["method"])
			assert.Equal(t, "/test", requestLog["uri"])
			assert.NotNil(t, requestLog["latency"])

			if tt.wantError {
				assert.NotNil(t, requestLog["error"], "エラー系では error 属性が付くこと")
			}
		})
	}
}

// TestNew_ServerTimeouts は New が返す Echo の *http.Server にタイムアウトが
// 設定されていることを検証する。
//
// net/http の既定はいずれも無制限で、ヘッダやボディを少しずつ送り続ける接続に
// ソケットとゴルーチンを占有され続ける（slowloris）。DBMaxOpenConns で
// コネクション数に上限を張っていても、その手前の HTTP 層が無制限だと意味がない。
//
// 具体的な秒数はテスト側に持ち込まない（実装の定数をテストに写すと二重管理になる）。
// 「無制限でないこと」と「値どうしの関係が壊れていないこと」だけを見る。
func TestNew_ServerTimeouts(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	e := server.New(newTestLogger(&buf))
	require.NotNil(t, e.Server, "Echo が *http.Server を保持していること")

	assert.Positive(t, e.Server.ReadHeaderTimeout, "ヘッダ読み取りが無制限だと slowloris を止められない")
	assert.Positive(t, e.Server.ReadTimeout, "リクエスト読み取りが無制限にならないこと")
	assert.Positive(t, e.Server.WriteTimeout, "レスポンス書き込みが無制限にならないこと")
	assert.Positive(t, e.Server.IdleTimeout, "keep-alive の滞留が無制限にならないこと")

	assert.LessOrEqual(t, e.Server.ReadHeaderTimeout, e.Server.ReadTimeout,
		"ヘッダ読み取りはリクエスト全体の読み取りに含まれる")
	assert.GreaterOrEqual(t, e.Server.IdleTimeout, e.Server.ReadTimeout,
		"keep-alive がリクエスト読み取りより短いと、負荷試験で接続が無駄に張り直される")
}

// TestNew_BodyLimit は過大なリクエストボディが 413 で弾かれることを検証する。
// 上限値そのものはテストに持ち込まず、「現行エンドポイントが必要とする桁を
// はるかに超えるボディは通らない」ことだけを見る。
func TestNew_BodyLimit(t *testing.T) {
	t.Parallel()

	// 現行エンドポイントのボディは最大でも数十バイト（{"points":N,"reason":"..."}）。
	const hugeBodySize = 1 << 20 // 1MiB

	tests := []struct {
		name       string
		body       string
		wantStatus int
		// wantLogLevel は弾かれたリクエストがアクセスログに残ることの検証用。
		// BodyLimit をログミドルウェアより外側に置くと 413 がログに出ないため、
		// 「弾いた事実が観測できる」ことをここで固定する。
		wantLogLevel string
	}{
		{
			name:         "通常サイズのボディは通る",
			body:         `{"points":100,"reason":"test"}`,
			wantStatus:   http.StatusOK,
			wantLogLevel: "INFO",
		},
		{
			name:         "過大なボディは 413 で弾かれ、アクセスログに残る",
			body:         strings.Repeat("a", hugeBodySize),
			wantStatus:   http.StatusRequestEntityTooLarge,
			wantLogLevel: "ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			e := server.New(newTestLogger(&buf))
			e.POST("/test", func(c echo.Context) error {
				return c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(tt.body))
			req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)

			var requestLog map[string]any
			for _, l := range parseLogLines(t, &buf) {
				if l["msg"] == "request" {
					requestLog = l
					break
				}
			}
			require.NotNil(t, requestLog, "弾かれたリクエストもアクセスログに残ること")
			assert.Equal(t, tt.wantLogLevel, requestLog["level"])
			assert.NotEmpty(t, requestLog["request_id"], "request_id が付くこと")
		})
	}
}

// TestNew_MiddlewareOrder は New に登録するミドルウェアの順序を AST で検証する。
//
// 【この検査が捕捉する既知の実例】
// BodyLimit を e.Use の先頭（アクセスログより外側）に置いた実装で、413 で弾いた
// リクエストがアクセスログにも request_id にも残らない状態になった。
// 拒否されている事実が運用側から観測できないため、原因調査の手がかりが消える。
//
// 【なぜ振る舞いテストだけでは足りないか】
// TestNew_BodyLimit は「BodyLimit が」ログに残ることしか見ない。別のミドルウェア
// （認証・レート制限等）が同じ誤りで追加されても、そのミドルウェアを狙った
// リクエストを送るケースを誰かが書かない限り検知できない。
// 順序は「文の並び」なので ruleguard（式単位のマッチ）では表現できず、
// AST を直接読む（determ. §3）。
//
// 【規則】ホワイトリスト方式（determ. §4）
// 観測系を先頭に固定し、その前に置いてよいものは無い。それ以外は必ず後ろ。
func TestNew_MiddlewareOrder(t *testing.T) {
	t.Parallel()

	// 観測系ミドルウェア。RequestID が先なのは、アクセスログが v.RequestID を読むため。
	wantHead := []string{"RequestID", "RequestLoggerWithConfig"}

	got := middlewareCallsIn(t, "echo.go", "New", "Use")

	require.GreaterOrEqualf(t, len(got), len(wantHead),
		"New に登録されたミドルウェアが %d 個未満: %v", len(wantHead), got)
	assert.Equal(t, wantHead, got[:len(wantHead)],
		"観測系ミドルウェアを先頭に置くこと。前に挟んだミドルウェアが短絡すると、"+
			"そのリクエストはアクセスログにも request_id にも残らない（AGENTS.md §2）。実際の登録順: %v", got)

	// Pre はルーティング前・アクセスログより外側で走るため、上の順序を迂回する。
	// リポジトリ全体での不使用は .golangci.yml の ruleguard が強制しており、
	// ここでは New 単体での二重確認に留める。
	assert.Empty(t, middlewareCallsIn(t, "echo.go", "New", "Pre"),
		"e.Pre はアクセスログの外側で完結するため使わない（AGENTS.md §2）")
}

// middlewareCallsIn は srcFile 内の関数 funcName にある `<recv>.<method>(f(...))` 形式の
// 呼び出しを登録順に走査し、引数として渡されたミドルウェア生成関数の名前を返す。
// 例: `e.Use(middleware.RequestID())` → "RequestID"
//
// go test の作業ディレクトリはパッケージディレクトリなので、srcFile は相対パスで届く。
func middlewareCallsIn(t *testing.T, srcFile, funcName, method string) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, srcFile, nil, parser.SkipObjectResolution)
	require.NoErrorf(t, err, "%s をパースできません", srcFile)

	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == funcName && fn.Recv == nil {
			target = fn
			break
		}
	}
	require.NotNilf(t, target, "%s に関数 %s が見つかりません", srcFile, funcName)

	var names []string
	ast.Inspect(target.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method || len(call.Args) == 0 {
			return true
		}
		names = append(names, calleeName(call.Args[0]))
		return true
	})
	return names
}

// calleeName は `middleware.RequestID()` / `RequestID()` から "RequestID" を取り出す。
// 関数呼び出し以外（変数を渡す等）の場合は "<unknown>" を返し、
// 「順序を判定できない書き方」として突き合わせで落ちるようにする。
func calleeName(arg ast.Expr) string {
	call, ok := arg.(*ast.CallExpr)
	if !ok {
		return "<unknown>"
	}
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	default:
		return "<unknown>"
	}
}

// TestRun_GracefulShutdown は ctx キャンセルでサーバが正常終了することを検証する。
func TestRun_GracefulShutdown(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := newTestLogger(&buf)
	e := server.New(logger)
	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, e, 0, logger)
	}()

	// Echo がリッスンを開始するまでポーリング（最大 2 秒）。
	// e.ListenerAddr() は内部で mutex RLock を取るため race-safe。
	var started bool
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if e.ListenerAddr() != nil {
			started = true
			break
		}
	}
	require.True(t, started, "サーバが 2 秒以内に起動すること")

	// ctx をキャンセルしてシャットダウンを要求
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "グレースフルシャットダウンは nil を返すこと")
	case <-time.After(5 * time.Second):
		t.Fatal("Run が 5 秒以内に終了しなかった")
	}

	// ログ出力の検証
	logStr := buf.String()
	assert.True(t, strings.Contains(logStr, "starting http server"), "起動ログが出力されていること")
	assert.True(t, strings.Contains(logStr, "shutting down http server"), "シャットダウンログが出力されていること")
}

// TestRun_StartError は使用中のポートを渡した場合に起動エラーが返ることを検証する。
func TestRun_StartError(t *testing.T) {
	t.Parallel()

	// 先にポートを占有する。0.0.0.0 で listen することで ":port" 指定も衝突させる。
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	occupiedPort := addr.Port

	var buf bytes.Buffer
	logger := newTestLogger(&buf)
	e := server.New(logger)

	// キャンセルしない ctx を渡す。errCh 経路でエラーが返るはず。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- server.Run(ctx, e, occupiedPort, logger)
	}()

	select {
	case runErr := <-done:
		require.Error(t, runErr, "ポート占有時はエラーを返すこと")
		assert.Contains(t, runErr.Error(), "failed to start server", "エラーメッセージに 'failed to start server' が含まれること")
	case <-time.After(5 * time.Second):
		t.Fatal("Run が 5 秒以内に終了しなかった")
	}
}
