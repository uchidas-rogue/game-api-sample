package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

// gRPC サーバの運用値。いずれもこの層固有なので server パッケージで定義する
// （AGENTS.md §2）。環境ごとに変える必要が出たら configs へ移す。
const (
	// grpcShutdownTimeout は GracefulStop の完了を待つ上限。超過したら Stop で強制切断する。
	// HTTP 側の shutdownTimeout と名前が違うのは、同じパッケージに両方があるため。
	//
	// HTTP より短くしてあるのは、RunGRPC が先に onShutdown で配信を止めてから待つので、
	// この時点で残っているのは短い unary の往復（DB/Redis への数 ms）だけになるため。
	// 長くすると、ストリームを掴んだままのクライアントがいる限り SIGTERM から
	// 実際の終了までデプロイが待たされる。
	grpcShutdownTimeout = 5 * time.Second

	// maxRecvMsgSize は受信メッセージの上限（バイト）。既定は 4MiB。
	// 本 API のリクエストは最大でも数十バイトなので、HTTP の bodyLimit（64K）と
	// 桁を揃えて絞る。
	maxRecvMsgSize = 64 * 1024

	// keepaliveTime はサーバがアイドル接続へ PING を送るまでの無通信時間。
	// モバイル回線の NAT はアイドル接続を数分で切るため、既定（2 時間）のままだと
	// streaming が黙って死ぬ。それより十分短い間隔で PING を流して経路を維持する。
	keepaliveTime = 30 * time.Second

	// keepaliveTimeout は PING の応答を待つ上限。超えたら接続を切る。
	keepaliveTimeout = 10 * time.Second

	// keepaliveMinTime はクライアントからの PING を許容する最短間隔。
	// これより短い間隔で PING が来ると GOAWAY(ENHANCE_YOUR_CALM) で切断される。
	// クライアント（Unity）側の keepalive 設定より短くしておくこと。
	keepaliveMinTime = 10 * time.Second
)

// NewGRPC はインターセプタと keepalive を設定した gRPC サーバを生成する。
// サービスの登録（RegisterXxxServer）は呼び出し側で行う。
func NewGRPC(logger *slog.Logger) *grpc.Server {
	return grpc.NewServer(
		grpc.MaxRecvMsgSize(maxRecvMsgSize),

		// Unity クライアントは長時間のストリーム（ランキングの push）を張る。
		// keepalive を設定しないと、モバイル NAT にアイドル接続を落とされた時点で
		// ストリームが無言で止まる（サーバもクライアントも切断に気づかない）。
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    keepaliveTime,
			Timeout: keepaliveTimeout,
		}),
		// PermitWithoutStream を false（既定）にすると、アイドル時にクライアントが
		// 送る keepalive PING が「不正な PING」と判定され ENHANCE_YOUR_CALM で
		// 切断される。クライアント側の keepalive を機能させるために true にする。
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             keepaliveMinTime,
			PermitWithoutStream: true,
		}),

		// ChainUnaryInterceptor は「先に渡したものが最外側」で、echo.Echo.Use と
		// 同じ並びになる。したがって HTTP と同じ規約をそのまま適用する:
		// 観測系（RequestID → アクセスログ）を先頭に置き、リクエストを短絡しうる
		// ものは必ずその後ろへ置く（AGENTS.md §2）。
		//
		// recover を観測系より外側に置いてはならない。外側に置くと panic した RPC が
		// アクセスログにも request_id にも残らず、運用側から観測できなくなる。
		// 順序は TestNewGRPC_UnaryInterceptorOrder / TestNewGRPC_StreamInterceptorOrder が
		// AST で検証し、単数形 grpc.UnaryInterceptor による迂回は
		// scripts/ruleguard/rules.go の grpcSingleInterceptor が塞ぐ。
		grpc.ChainUnaryInterceptor(
			UnaryRequestID(),
			UnaryAccessLog(logger),
			UnaryRecover(logger),
		),
		grpc.ChainStreamInterceptor(
			StreamRequestID(),
			StreamAccessLog(logger),
			StreamRecover(logger),
		),
	)
}

// RunGRPC は指定ポートで gRPC サーバを起動し、ctx のキャンセルでシャットダウンする。
//
// onShutdown は「配信を止める」フックで、シャットダウンの最初に一度だけ呼ばれる。
// nil を渡してよい（ストリーミングを持たない構成のための、意図的に省略可能な引数。
// 渡し忘れを許さない logger の nil チェック禁止規約とは別物）。
//
// grpc.Server.GracefulStop() は進行中のストリームが終わるまでブロックするため、
// echo.go の Run と同型に書くと SIGTERM でシャットダウンが完了しない。
// そこで次の三段構えを取る（設計の詳細は docs/testing/grpc-server.md）:
//
//  1. onShutdown を呼び、配信側に「もう送るな」と伝えて進行中のストリームを終わらせる
//  2. GracefulStop を goroutine で呼び、grpcShutdownTimeout まで待つ
//  3. 超過したら Stop で強制切断し、その事実を WARN で残す
func RunGRPC(ctx context.Context, srv *grpc.Server, port int, logger *slog.Logger, onShutdown func()) error {
	addr := fmt.Sprintf(":%d", port)
	// ListenConfig を使うのは ctx を渡せるようにするため（noctx）。ctx が効くのは
	// listen する瞬間だけで、リスナの寿命には影響しない。
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// 起動を非同期で行い、ctx キャンセルでシャットダウンする。
	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting grpc server", slog.String("addr", lis.Addr().String()))
		// ErrServerStopped は停止要求の結果なのでエラー扱いしない。
		if serveErr := srv.Serve(lis); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			errCh <- fmt.Errorf("failed to serve grpc: %w", serveErr)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownGRPC(srv, logger, onShutdown)
		return nil
	case serveErr := <-errCh:
		return serveErr
	}
}

// shutdownGRPC は RunGRPC の三段構えのシャットダウンを実行する。
//
// 強制切断してもエラーを返さないのは、ストリームを掴んだままのクライアントが
// 1つでもいると毎回のデプロイが異常終了扱いになり、本当の異常と区別できなくなるため。
// 強制切断した事実は WARN ログで観測する。
func shutdownGRPC(srv *grpc.Server, logger *slog.Logger, onShutdown func()) {
	logger.Info("shutting down grpc server")

	// (1) 配信側に停止を伝える。これをやらないと (2) は返らない。
	if onShutdown != nil {
		onShutdown()
	}

	// (2) GracefulStop は進行中のストリームが終わるまでブロックするので、
	// goroutine へ逃がして待ち時間に上限を付ける。
	stopped := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(stopped)
	}()

	timer := time.NewTimer(grpcShutdownTimeout)
	defer timer.Stop()

	select {
	case <-stopped:
		return
	case <-timer.C:
		// (3) 期限内に終わらなかった。残っている接続を強制的に切る。
		logger.Warn("graceful stop timed out, forcing shutdown",
			slog.Duration("timeout", grpcShutdownTimeout))
		srv.Stop()
		// Stop は GracefulStop の待ちも解くので、goroutine を回収してから返る。
		<-stopped
	}
}
