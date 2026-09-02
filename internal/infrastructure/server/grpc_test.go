package server_test

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/server"
	"github.com/uchidas-rogue/game-api-sample/internal/testutil/slogtest"
)

// テスト用サービスとクライアントの固定値。
const (
	// testCodecName はテスト専用コーデックの content-subtype。
	testCodecName = "servertestraw"

	testServiceName      = "server.test.v1.TestService"
	testUnaryMethod      = "Unary"
	testStreamMethod     = "Stream"
	testUnaryFullMethod  = "/" + testServiceName + "/" + testUnaryMethod
	testStreamFullMethod = "/" + testServiceName + "/" + testStreamMethod

	// requestIDHeader はインターセプタが読み書きする metadata のキー。
	// 実装側の const とは意図的に別に持つ（キー名が変わればテストが落ちるべきなので、
	// 実装の値を参照しない）。
	requestIDHeader = "x-request-id"

	// requestIDHexLen は採番される request_id の文字数（16 バイトの hex）。
	requestIDHexLen = 32

	// accessLogMsg / panicLogMsg はログ行の照合に使う文字列。実装側の const を
	// 参照しないのは、ログの文言が変わったらテストが落ちるべきだから
	// （ログは運用が読む出力なので、契約として固定する）。
	accessLogMsg = "rpc"
	panicLogMsg  = `msg="rpc panic recovered"`

	// bufSize は bufconn のバッファサイズ。1 RPC ぶんのやり取りに十分な値。
	bufSize = 1024 * 1024

	// startupPollInterval / startupPollLimit は「サーバが listen を始めた」ことを
	// ログで待つときのポーリング間隔と回数（上限 2 秒）。
	// gRPC には echo.ListenerAddr() に相当する race-safe な getter が無いため、
	// 内部ロック付きの slogtest.Recorder を経由して起動を観測する。
	startupPollInterval = 50 * time.Millisecond
	startupPollLimit    = 40

	// runReturnTimeout は RunGRPC が返るのを待つ上限。
	// ローカル想定（強制切断ケースで grpcShutdownTimeout ≒ 5 秒）に対し、
	// CI 高負荷時の余裕を見て 4 倍取る（AGENTS.md §3 Flaky 防止）。
	runReturnTimeout = 20 * time.Second
)

// init はテスト専用コーデックを登録する。
//
// encoding.RegisterCodec はグローバルなレジストリへの書き込みで thread-safe ではないため、
// テスト関数の中ではなく init で呼ぶ（t.Parallel() 配下のクライアント生成と競合する）。
// proto 生成型を使わないのは、internal/infrastructure のテストから
// internal/driver（生成物の置き場）を import するのが層の依存規約に反するため。
func init() {
	encoding.RegisterCodec(rawCodec{})
}

// rawMessage はテスト用サービスがやり取りする素のバイト列。
type rawMessage []byte

// rawCodec が encoding.Codec を満たすことをコンパイル時に検証する。
var _ encoding.Codec = rawCodec{}

// rawCodec は protobuf に依存せずに RPC を通すための最小コーデック。
type rawCodec struct{}

// Marshal は rawMessage をそのままバイト列にする。
func (rawCodec) Marshal(v any) ([]byte, error) {
	msg, ok := v.(*rawMessage)
	if !ok {
		return nil, fmt.Errorf("rawCodec: unexpected type %T", v)
	}
	return *msg, nil
}

// Unmarshal はバイト列を rawMessage へ書き戻す。
func (rawCodec) Unmarshal(data []byte, v any) error {
	msg, ok := v.(*rawMessage)
	if !ok {
		return fmt.Errorf("rawCodec: unexpected type %T", v)
	}
	*msg = append((*msg)[:0], data...)
	return nil
}

// Name は content-subtype に使われる名前を返す。
func (rawCodec) Name() string { return testCodecName }

// testHandlers はテスト用サービスの振る舞い。ケースごとに差し替える。
type testHandlers struct {
	// unary は unary RPC の本体。
	unary func(ctx context.Context) error
	// stream は server streaming RPC の本体。
	stream func(ctx context.Context, ss grpc.ServerStream) error
}

// registerTestService は h の振る舞いを持つテスト用サービスを srv へ登録する。
//
// 生成コードを使わずに grpc.ServiceDesc を手で組み立てている。HandlerType に nil を
// 渡した実装（第 2 引数の ss）を登録すると grpc-go は型検査を飛ばすため、
// インターフェースを定義せずに済む。
func registerTestService(t *testing.T, srv *grpc.Server, h testHandlers) {
	t.Helper()

	desc := grpc.ServiceDesc{
		ServiceName: testServiceName,
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: testUnaryMethod,
			Handler: func(_ any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				var in rawMessage
				if err := dec(&in); err != nil {
					return nil, err
				}
				handler := func(ctx context.Context, _ any) (any, error) {
					if err := h.unary(ctx); err != nil {
						return nil, err
					}
					out := rawMessage("ok")
					return &out, nil
				}
				if interceptor == nil {
					return handler(ctx, &in)
				}
				return interceptor(ctx, &in, &grpc.UnaryServerInfo{FullMethod: testUnaryFullMethod}, handler)
			},
		}},
		Streams: []grpc.StreamDesc{{
			StreamName:    testStreamMethod,
			ServerStreams: true,
			Handler: func(_ any, ss grpc.ServerStream) error {
				return h.stream(ss.Context(), ss)
			},
		}},
	}
	srv.RegisterService(&desc, nil)
}

// startBufconnServer は bufconn 上に NewGRPC のサーバを立て、接続済みのクライアントを返す。
func startBufconnServer(t *testing.T, logger *slog.Logger, h testHandlers) *grpc.ClientConn {
	t.Helper()

	lis := bufconn.Listen(bufSize)
	srv := server.NewGRPC(logger)
	registerTestService(t, srv, h)

	go func() {
		// Serve は Stop 後に ErrServerStopped を返す。テストの後片付けで起きるため無視する。
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.CallContentSubtype(testCodecName)),
	)
	require.NoError(t, err)
	// Cleanup は後入れ先出しなので、conn を閉じてからサーバを止める。
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// interceptorCase は docs/testing/grpc-server.md §2 の仕様表 1 行ぶん。
// unary と stream で同じ形を使う（表の 1〜4 と 5〜8 は同じ観点の別実装）。
type interceptorCase struct {
	name string
	// incomingRequestID が空でなければ、その値を x-request-id として送る。
	incomingRequestID string
	// panicValue が非 nil なら handler は panic する。
	panicValue any
	// handlerErr が非 nil なら handler はそのエラーを返す。
	handlerErr error
	wantCode   codes.Code
	// wantLogLevel はアクセスログに出るべきレベル。
	wantLogLevel string
	// wantPanicLog は panic ログが出るべきか。
	wantPanicLog bool
}

// TestGRPCInterceptors_Unary は unary RPC がインターセプタ3本を通ったときの
// request_id・アクセスログ・panic 変換を bufconn 経由で検証する。
func TestGRPCInterceptors_Unary(t *testing.T) {
	t.Parallel()

	tests := []interceptorCase{
		{
			// #1 A→B→D→E→F→G→P→I→E1
			// panic の値は error。stream 側（#5）は文字列にしてあり、recover が
			// 受け取る値の型で分岐する箇所（%w / %v）を 2 つのケースで両方通す。
			name:         "handler の panic は Internal に変換され、panic とアクセスログの両方が残る",
			panicValue:   errors.New("boom"),
			wantCode:     codes.Internal,
			wantLogLevel: "ERROR",
			wantPanicLog: true,
		},
		{
			// #2 A→B→D→E→F→G→H→I→E1
			name:         "handler が返した status code はそのまま返り、アクセスログは ERROR になる",
			handlerErr:   status.Error(codes.FailedPrecondition, "no stock"),
			wantCode:     codes.FailedPrecondition,
			wantLogLevel: "ERROR",
		},
		{
			// #3 A→B→C→E→F→G→H→I→Z
			name:              "受信した x-request-id を尊重する",
			incomingRequestID: "client-generated-id",
			wantCode:          codes.OK,
			wantLogLevel:      "INFO",
		},
		{
			// #4 A→B→D→E→F→G→H→I→Z
			name:         "x-request-id が無ければ採番する",
			wantCode:     codes.OK,
			wantLogLevel: "INFO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger, rec := slogtest.NewRecordingLogger(t, nil)
			handlerIDs := make(chan string, 1)
			conn := startBufconnServer(t, logger, testHandlers{
				unary: func(ctx context.Context) error {
					handlerIDs <- server.RequestIDFromContext(ctx)
					if tt.panicValue != nil {
						panic(tt.panicValue)
					}
					return tt.handlerErr
				},
				stream: unusedStreamHandler,
			})

			var header metadata.MD
			in, out := rawMessage("ping"), rawMessage(nil)
			err := conn.Invoke(outgoingCtx(t, tt.incomingRequestID), testUnaryFullMethod, &in, &out, grpc.Header(&header))

			assert.Equal(t, tt.wantCode, status.Code(err))
			assertRequestIDPropagation(t, tt, header, handlerIDs, rec)
		})
	}
}

// TestGRPCInterceptors_Stream は server streaming RPC について
// TestGRPCInterceptors_Unary と同じ観点を検証する。
//
// unary と同一パスでもケースを統合しないのは、実装が別関数で、片方だけ壊れうるため
// （stream 側は ServerStream をラップし忘れると ctx が handler に届かない）。
func TestGRPCInterceptors_Stream(t *testing.T) {
	t.Parallel()

	tests := []interceptorCase{
		{
			// #5 A→B→D→E→F→G→P→I→E1
			// panic の値は文字列（#1 は error）。
			name:         "handler の panic は Internal に変換され、panic とアクセスログの両方が残る",
			panicValue:   "boom",
			wantCode:     codes.Internal,
			wantLogLevel: "ERROR",
			wantPanicLog: true,
		},
		{
			// #6 A→B→D→E→F→G→H→I→E1
			name:         "handler が返した status code はそのまま返り、アクセスログは ERROR になる",
			handlerErr:   status.Error(codes.FailedPrecondition, "no stock"),
			wantCode:     codes.FailedPrecondition,
			wantLogLevel: "ERROR",
		},
		{
			// #7 A→B→C→E→F→G→H→I→Z
			name:              "受信した x-request-id を尊重する",
			incomingRequestID: "client-generated-id",
			wantCode:          codes.OK,
			wantLogLevel:      "INFO",
		},
		{
			// #8 A→B→D→E→F→G→H→I→Z
			name:         "x-request-id が無ければ採番する",
			wantCode:     codes.OK,
			wantLogLevel: "INFO",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger, rec := slogtest.NewRecordingLogger(t, nil)
			handlerIDs := make(chan string, 1)
			conn := startBufconnServer(t, logger, testHandlers{
				unary: func(context.Context) error { return nil },
				stream: func(ctx context.Context, ss grpc.ServerStream) error {
					handlerIDs <- server.RequestIDFromContext(ctx)
					if tt.panicValue != nil {
						panic(tt.panicValue)
					}
					if tt.handlerErr != nil {
						return tt.handlerErr
					}
					out := rawMessage("ok")
					return ss.SendMsg(&out)
				},
			})

			cs, err := conn.NewStream(outgoingCtx(t, tt.incomingRequestID),
				&grpc.StreamDesc{StreamName: testStreamMethod, ServerStreams: true}, testStreamFullMethod)
			require.NoError(t, err)
			in := rawMessage("ping")
			require.NoError(t, cs.SendMsg(&in))
			require.NoError(t, cs.CloseSend())

			assert.Equal(t, tt.wantCode, status.Code(drainStream(cs)))

			header, err := cs.Header()
			require.NoError(t, err)
			assertRequestIDPropagation(t, tt, header, handlerIDs, rec)
		})
	}
}

// unusedStreamHandler は当該テストで呼ばれないストリームハンドラ。
// 呼ばれてしまった場合に静かに成功しないよう、明示的にエラーを返す。
func unusedStreamHandler(context.Context, grpc.ServerStream) error {
	return status.Error(codes.Unimplemented, "stream is not used in this test")
}

// outgoingCtx は requestID が空でなければ x-request-id を載せた ctx を返す。
func outgoingCtx(t *testing.T, requestID string) context.Context {
	t.Helper()
	if requestID == "" {
		return t.Context()
	}
	return metadata.AppendToOutgoingContext(t.Context(), requestIDHeader, requestID)
}

// drainStream はストリームを終端まで読み、終了ステータスを返す。
// 正常終了（io.EOF）は nil にする。
func drainStream(cs grpc.ClientStream) error {
	for {
		var out rawMessage
		err := cs.RecvMsg(&out)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// assertRequestIDPropagation は request_id が「応答ヘッダ・ハンドラの ctx・
// アクセスログ」の3箇所で同じ値になっていること、およびログの出力内容を検証する。
func assertRequestIDPropagation(
	t *testing.T,
	tt interceptorCase,
	header metadata.MD,
	handlerIDs <-chan string,
	rec *slogtest.Recorder,
) {
	t.Helper()

	values := header.Get(requestIDHeader)
	require.Len(t, values, 1, "応答ヘッダに x-request-id が 1 つ載ること")
	id := values[0]

	if tt.incomingRequestID != "" {
		assert.Equal(t, tt.incomingRequestID, id, "受信した x-request-id を上書きしないこと")
	} else {
		assert.Len(t, id, requestIDHexLen, "採番された request_id は 16 バイトの hex であること")
		_, err := hex.DecodeString(id)
		assert.NoError(t, err, "採番された request_id が hex であること")
	}

	select {
	case handlerID := <-handlerIDs:
		assert.Equal(t, id, handlerID, "ハンドラの ctx にも同じ request_id が届くこと")
	default:
		t.Fatal("ハンドラが呼ばれていない")
	}

	assert.Equal(t, 1, rec.Count("level="+tt.wantLogLevel, "msg="+accessLogMsg,
		"code="+tt.wantCode.String(), "request_id="+id),
		"アクセスログが request_id 付きで 1 件出ること: %v", rec.Lines())

	wantPanicLogs := 0
	if tt.wantPanicLog {
		wantPanicLogs = 1
	}
	assert.Equal(t, wantPanicLogs, rec.Count("level=ERROR", panicLogMsg, "request_id="+id),
		"panic ログの有無: %v", rec.Lines())
}

// TestNewGRPC_UnaryInterceptorOrder は NewGRPC に登録する unary インターセプタの
// 順序を AST で検証する。
//
// 【この検査が捕捉する既知の実例】
// HTTP 側で BodyLimit をアクセスログより外側に登録し、413 で弾いたリクエストが
// アクセスログにも request_id にも残らなくなった（TestNew_MiddlewareOrder）。
// gRPC で同じ誤りをすると、panic した RPC が観測できなくなる。
//
// 【なぜ振る舞いテストだけでは足りないか】
// TestGRPCInterceptors_Unary は「panic が」ログに残ることしか見ない。別の
// インターセプタ（認証・レート制限）が同じ誤りで追加されても、それを狙った
// RPC を送るケースを誰かが書かない限り検知できない。
// 順序は「引数の並び」なので ruleguard（式単位のマッチ）では表現できず、
// AST を直接読む（determ. §3）。
func TestNewGRPC_UnaryInterceptorOrder(t *testing.T) {
	t.Parallel()

	// 観測系インターセプタ。RequestID が先なのは、アクセスログが ctx の request_id を読むため。
	wantHead := []string{"UnaryRequestID", "UnaryAccessLog"}

	got := middlewareCallsIn(t, "grpc.go", "NewGRPC", "ChainUnaryInterceptor")

	require.GreaterOrEqualf(t, len(got), len(wantHead),
		"NewGRPC に登録された unary インターセプタが %d 個未満: %v", len(wantHead), got)
	assert.Equal(t, wantHead, got[:len(wantHead)],
		"観測系インターセプタを先頭（= 最外側）に置くこと。前に挟んだインターセプタが短絡すると、"+
			"その RPC はアクセスログにも request_id にも残らない（AGENTS.md §2）。実際の登録順: %v", got)

	// 単数形は Chain 系と併用でき、この AST 検査を迂回する。リポジトリ全体での
	// 不使用は .golangci.yml の ruleguard（grpcSingleInterceptor）が強制しており、
	// ここでは NewGRPC 単体での二重確認に留める。
	assert.Empty(t, middlewareCallsIn(t, "grpc.go", "NewGRPC", "UnaryInterceptor"),
		"grpc.UnaryInterceptor（単数形）は使わない。ChainUnaryInterceptor の引数へ登録すること")
}

// TestNewGRPC_StreamInterceptorOrder は stream インターセプタについて
// TestNewGRPC_UnaryInterceptorOrder と同じ検査を行う。
func TestNewGRPC_StreamInterceptorOrder(t *testing.T) {
	t.Parallel()

	wantHead := []string{"StreamRequestID", "StreamAccessLog"}

	got := middlewareCallsIn(t, "grpc.go", "NewGRPC", "ChainStreamInterceptor")

	require.GreaterOrEqualf(t, len(got), len(wantHead),
		"NewGRPC に登録された stream インターセプタが %d 個未満: %v", len(wantHead), got)
	assert.Equal(t, wantHead, got[:len(wantHead)],
		"観測系インターセプタを先頭（= 最外側）に置くこと（AGENTS.md §2）。実際の登録順: %v", got)

	assert.Empty(t, middlewareCallsIn(t, "grpc.go", "NewGRPC", "StreamInterceptor"),
		"grpc.StreamInterceptor（単数形）は使わない。ChainStreamInterceptor の引数へ登録すること")
}

// TestNewGRPC_InterceptorOrderMatchesAcrossKinds は unary と stream の
// インターセプタの並びが一致していることを検証する。
//
// 片側だけを見る検査では、「unary は直したが stream を直し忘れた」形の抜けが
// 通ってしまう。並びが同じであること自体を固定する。
func TestNewGRPC_InterceptorOrderMatchesAcrossKinds(t *testing.T) {
	t.Parallel()

	unary := interceptorRoles(middlewareCallsIn(t, "grpc.go", "NewGRPC", "ChainUnaryInterceptor"), "Unary")
	stream := interceptorRoles(middlewareCallsIn(t, "grpc.go", "NewGRPC", "ChainStreamInterceptor"), "Stream")

	require.NotEmpty(t, unary, "unary インターセプタが登録されていること")
	assert.Equal(t, unary, stream,
		"unary と stream で同じ役割を同じ順に登録すること。片方にだけ観測系が無いと、"+
			"その種別の RPC だけがアクセスログから消える")
}

// interceptorRoles は "UnaryRecover" / "StreamRecover" のような名前から種別の
// 接頭辞を落として役割名（"Recover"）にする。接頭辞が付いていない名前はそのまま返し、
// 突き合わせで落ちるようにする。
func interceptorRoles(names []string, prefix string) []string {
	roles := make([]string, 0, len(names))
	for _, name := range names {
		roles = append(roles, strings.TrimPrefix(name, prefix))
	}
	return roles
}

// TestRunGRPC_ListenError は使用中のポートを渡した場合に起動エラーが返ることを検証する。
func TestRunGRPC_ListenError(t *testing.T) {
	t.Parallel()

	// #1 S→SL→SE1
	// 先にポートを占有する。0.0.0.0 で listen することで ":port" 指定も衝突させる。
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	defer ln.Close()

	logger, rec := slogtest.NewRecordingLogger(t, nil)
	srv := server.NewGRPC(logger)

	runErr := server.RunGRPC(t.Context(), srv, ln.Addr().(*net.TCPAddr).Port, logger, nil)

	require.Error(t, runErr, "ポート占有時はエラーを返すこと")
	assert.Contains(t, runErr.Error(), "failed to listen")
	assert.Zero(t, rec.Count("starting grpc server"), "listen に失敗したら Serve に進まないこと")
}

// TestRunGRPC_ServeStopped は Serve 側が先に終了した場合の戻り値を検証する。
// ErrServerStopped は停止要求の結果なのでエラー扱いしない。
func TestRunGRPC_ServeStopped(t *testing.T) {
	t.Parallel()

	// #2 S→SL→SR→SW→SE2
	logger, rec := slogtest.NewRecordingLogger(t, nil)
	srv := server.NewGRPC(logger)

	done := make(chan error, 1)
	go func() {
		done <- server.RunGRPC(t.Context(), srv, freePort(t), logger, nil)
	}()
	waitForServerStart(t, rec)

	srv.Stop()

	select {
	case err := <-done:
		require.NoError(t, err, "ErrServerStopped はエラー扱いしないこと")
	case <-time.After(runReturnTimeout):
		t.Fatal("RunGRPC が返らなかった")
	}
}

// TestRunGRPC_Shutdown は ctx キャンセル時の三段構えのシャットダウンを検証する。
//
// 【この検査が捕捉する既知の実例】
// grpc.Server.GracefulStop() は進行中のストリームが終わるまでブロックする。
// echo.go の Run と同型に書くと、WatchUserRankings のような server streaming を
// 掴んだクライアントが 1 つでもいる限り SIGTERM でプロセスが終わらず、デプロイが止まる。
func TestRunGRPC_Shutdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// openStream はストリームを開いたまま ctx をキャンセルするか。
		openStream bool
		// useOnShutdown は onShutdown を渡すか（nil 許容の検証）。
		useOnShutdown bool
		// stopStream は onShutdown が配信を止めてストリームを終わらせるか。
		stopStream bool
		// wantForcedStop は Stop による強制切断が起きるべきか。
		wantForcedStop bool
	}{
		{
			// #3 S→SL→SR→SW→SO→SG→ST→SZ
			name: "ストリーム無し・onShutdown が nil でも完了する",
		},
		{
			// #4 S→SL→SR→SW→SO→SH→SG→ST→SZ
			name:          "onShutdown が配信を止めればストリームがあっても完了する",
			openStream:    true,
			useOnShutdown: true,
			stopStream:    true,
		},
		{
			// #5 S→SL→SR→SW→SO→SH→SG→ST→SF→SZ
			name:           "onShutdown が配信を止めなければ強制切断して返る",
			openStream:     true,
			useOnShutdown:  true,
			wantForcedStop: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger, rec := slogtest.NewRecordingLogger(t, nil)
			srv := server.NewGRPC(logger)

			// release は「配信側が停止した」合図。閉じるとストリームの handler が終わる。
			release := make(chan struct{})
			var onShutdownCalls atomic.Int32
			var onShutdown func()
			if tt.useOnShutdown {
				onShutdown = func() {
					onShutdownCalls.Add(1)
					if tt.stopStream {
						close(release)
					}
				}
			}

			registerTestService(t, srv, testHandlers{
				unary: func(context.Context) error { return nil },
				stream: func(ctx context.Context, ss grpc.ServerStream) error {
					// 先に 1 件返してストリームが確立したことをクライアントへ知らせ、
					// あとは配信停止（release）か強制切断（ctx）まで居座る。
					out := rawMessage("ok")
					if err := ss.SendMsg(&out); err != nil {
						return err
					}
					select {
					case <-release:
					case <-ctx.Done():
					}
					return nil
				},
			})

			port := freePort(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan error, 1)
			go func() {
				done <- server.RunGRPC(ctx, srv, port, logger, onShutdown)
			}()
			waitForServerStart(t, rec)

			if tt.openStream {
				openBlockingStream(t, port)
			}

			cancel()

			select {
			case err := <-done:
				require.NoError(t, err, "強制切断でもエラーは返さない（デプロイのたびに異常終了扱いになるため）")
			case <-time.After(runReturnTimeout):
				t.Fatal("RunGRPC が返らなかった。GracefulStop が進行中のストリームでブロックしている")
			}

			if tt.useOnShutdown {
				assert.Equal(t, int32(1), onShutdownCalls.Load(), "onShutdown はちょうど 1 回呼ばれること")
			}

			forced := rec.Count("level=WARN", "forcing shutdown")
			if tt.wantForcedStop {
				assert.Equal(t, 1, forced, "強制切断した事実が WARN で残ること: %v", rec.Lines())
				return
			}
			assert.Zero(t, forced, "配信を止めたのに強制切断されている: %v", rec.Lines())
		})
	}
}

// openBlockingStream は port の gRPC サーバへストリームを 1 本張り、
// サーバ側の handler が動き出したことを最初のメッセージ受信で確認する。
// ストリームは閉じずに返る（掴んだまま ctx をキャンセルするのがテストの目的）。
func openBlockingStream(t *testing.T, port int) {
	t.Helper()

	conn, err := grpc.NewClient(fmt.Sprintf("passthrough:///127.0.0.1:%d", port),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.CallContentSubtype(testCodecName), grpc.WaitForReady(true)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	cs, err := conn.NewStream(t.Context(),
		&grpc.StreamDesc{StreamName: testStreamMethod, ServerStreams: true}, testStreamFullMethod)
	require.NoError(t, err)
	in := rawMessage("ping")
	require.NoError(t, cs.SendMsg(&in))

	var out rawMessage
	require.NoError(t, cs.RecvMsg(&out), "サーバ側 handler が動き出すこと")
}

// freePort は空きポート番号を返す。
//
// 返した直後に他プロセスが同じポートを取る可能性は残るが、その場合は
// listen 失敗として検知できる（黙って別の経路を通ることはない）。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// waitForServerStart はサーバが listen を開始するまで待つ。
//
// gRPC には echo.ListenerAddr() のような race-safe な getter が無いため、
// 内部ロック付きの slogtest.Recorder を経由して起動ログを観測する
// （AGENTS.md §3 Flaky 防止「共有フィールドを直接 read しない」）。
func waitForServerStart(t *testing.T, rec *slogtest.Recorder) {
	t.Helper()
	for range startupPollLimit {
		if rec.Count("starting grpc server") > 0 {
			return
		}
		time.Sleep(startupPollInterval)
	}
	t.Fatal("サーバが 2 秒以内に起動しなかった")
}
