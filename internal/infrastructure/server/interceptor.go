package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// gRPC インターセプタが使う固定値。この層でしか使わないので server パッケージで定義する
// （AGENTS.md §2）。
//
// 自前実装にしている理由: go-grpc-middleware 等を入れれば同じものが手に入るが、
// 本リポジトリの本体依存はごく少数に保ってある。recover が 15 行程度で書けるものに
// 依存を1つ増やす価値はない（設計判断は docs/testing/grpc-server.md）。
const (
	// requestIDMetadataKey は request_id を運ぶ metadata のキー。
	// HTTP 側（echo の middleware.RequestID）の X-Request-Id と同じ名前にして、
	// 2つの delivery をまたいだログの突き合わせをできるようにする。
	// gRPC の metadata キーは小文字へ正規化されるため小文字で定義する。
	requestIDMetadataKey = "x-request-id"

	// requestIDBytes は採番する request_id の乱数バイト数（hex で 32 文字になる）。
	requestIDBytes = 16

	// accessLogMessage は 1 RPC ぶんのアクセスログの msg。
	accessLogMessage = "rpc"

	// panicLogMessage は recover が拾った panic のログの msg。
	panicLogMessage = "rpc panic recovered"

	// panicStatusMessage は panic をクライアントへ返すときの説明。
	// panic の内容そのものは返さない（内部構造の露出になる）。ログとの
	// 突き合わせは request_id で行う。
	panicStatusMessage = "internal server error"
)

// requestIDContextKey は request_id を ctx に載せるための非公開キー型。
// 文字列をキーにすると他パッケージの値と衝突しうるため、この層でしか
// 生成できない型を使う。
type requestIDContextKey struct{}

// RequestIDFromContext は RequestID インターセプタが ctx に載せた request_id を返す。
// インターセプタを通っていない ctx では空文字列を返す。
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

// UnaryRequestID は unary RPC に request_id を付与するインターセプタを返す。
//
// 受信 metadata に x-request-id があればそれを尊重する（クライアントや
// API Gateway が採番した ID を上書きすると、クライアント側のログと
// 突き合わせられなくなるため）。無ければ採番する。
// 採番した ID は ctx と応答ヘッダの両方へ載せる。
func UnaryRequestID() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := resolveRequestID(ctx)
		// 応答ヘッダにも載せる。障害報告に添える ID をクライアントが取得できるように
		// するため（HTTP の X-Request-Id レスポンスヘッダに相当）。
		// SetHeader はヘッダ送信後に呼ぶとエラーになるが、ここは handler より前なので
		// 未送信であることが保証されている。仮に失敗しても RPC 自体は続けるのが
		// 正しいので戻り値は捨てる。
		_ = grpc.SetHeader(ctx, metadata.Pairs(requestIDMetadataKey, id))
		return handler(context.WithValue(ctx, requestIDContextKey{}, id), req)
	}
}

// StreamRequestID はストリーミング RPC に request_id を付与するインターセプタを返す。
// 振る舞いは UnaryRequestID と同じ。ハンドラへ ctx を届けるために ServerStream を
// 差し替える（ServerStream.Context() は差し替えないと元の ctx を返す）。
func StreamRequestID() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		id := resolveRequestID(ctx)
		_ = ss.SetHeader(metadata.Pairs(requestIDMetadataKey, id))
		return handler(srv, &requestIDStream{
			ServerStream: ss,
			ctx:          context.WithValue(ctx, requestIDContextKey{}, id),
		})
	}
}

// requestIDStream が grpc.ServerStream を満たすことをコンパイル時に検証する。
var _ grpc.ServerStream = (*requestIDStream)(nil)

// requestIDStream は Context() だけを差し替えた grpc.ServerStream。
// 埋め込みにより Context 以外のメソッドは元のストリームへ委譲される。
type requestIDStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context は request_id を載せた ctx を返す。
func (s *requestIDStream) Context() context.Context { return s.ctx }

// resolveRequestID は受信 metadata の x-request-id を返し、無ければ採番する。
func resolveRequestID(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get(requestIDMetadataKey); len(values) > 0 && values[0] != "" {
			return values[0]
		}
	}
	return newRequestID()
}

// newRequestID は乱数から request_id を採番する。
func newRequestID() string {
	buf := make([]byte, requestIDBytes)
	// crypto/rand.Read は読み取りに失敗した場合プロセスを落とす仕様で、
	// 戻り値の error は互換のために残っているだけなので捨ててよい。
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// UnaryAccessLog は unary RPC のアクセスログを出力するインターセプタを返す。
func UnaryAccessLog(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logAccess(ctx, logger, info.FullMethod, err, time.Since(start))
		return resp, err
	}
}

// StreamAccessLog はストリーミング RPC のアクセスログを出力するインターセプタを返す。
// latency はストリームが開いてから閉じるまでの時間になる（unary の「処理時間」とは
// 意味が違う点に注意）。
func StreamAccessLog(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logAccess(ss.Context(), logger, info.FullMethod, err, time.Since(start))
		return err
	}
}

// logAccess は 1 RPC ぶんのアクセスログを出力する。
//
// 属性は echo.go の RequestLoggerWithConfig と揃える（method / code / latency /
// request_id）。2つの delivery のログを同じ手順で追えるようにするため。
// ctx を渡すのも同じ理由で、slog.Handler が ctx 経由の trace_id 等を拾えるようにする
// （OTel/Datadog 連携を見据えた形）。
func logAccess(ctx context.Context, logger *slog.Logger, fullMethod string, err error, latency time.Duration) {
	attrs := []slog.Attr{
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("method", fullMethod),
		slog.String("code", status.Code(err).String()),
		slog.Duration("latency", latency),
	}
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
		logger.LogAttrs(ctx, slog.LevelError, accessLogMessage, attrs...)
		return
	}
	logger.LogAttrs(ctx, slog.LevelInfo, accessLogMessage, attrs...)
}

// UnaryRecover は unary RPC の panic を codes.Internal へ変換するインターセプタを返す。
func UnaryRecover(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				resp = nil
				err = recoveredToStatus(ctx, logger, info.FullMethod, recovered)
			}
		}()
		return handler(ctx, req)
	}
}

// StreamRecover はストリーミング RPC の panic を codes.Internal へ変換する
// インターセプタを返す。
func StreamRecover(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = recoveredToStatus(ss.Context(), logger, info.FullMethod, recovered)
			}
		}()
		return handler(srv, ss)
	}
}

// recoveredToStatus は panic をログに記録し、codes.Internal の status へ変換する。
//
// recover はチェーンの最内側（handler のすぐ外）に置く。観測系より外側に置くと、
// panic した RPC がアクセスログにも request_id にも残らず、運用側からは
// 「特定の RPC だけ Internal が返る」としか見えなくなる（AGENTS.md §2）。
func recoveredToStatus(ctx context.Context, logger *slog.Logger, fullMethod string, recovered any) error {
	err := fmt.Errorf("panic: %v", recovered)
	if cause, ok := recovered.(error); ok {
		err = fmt.Errorf("panic: %w", cause)
	}

	logger.LogAttrs(ctx, slog.LevelError, panicLogMessage,
		slog.String("request_id", RequestIDFromContext(ctx)),
		slog.String("method", fullMethod),
		slog.Any("error", err),
		// スタックはこのログにしか残らない（クライアントへは返さない）。
		slog.String("stack", string(debug.Stack())),
	)
	return status.Error(codes.Internal, panicStatusMessage)
}
