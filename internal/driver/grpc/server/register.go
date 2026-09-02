// Package server は gRPC サービスの登録を集約する。
//
// HTTP delivery の internal/driver/http/router と対になる位置づけで、
// コンポジションルート（internal/di）が組み立てたハンドラを登録する。
package server

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc"

	rankingv1 "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/gen/rankingv1"
	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/grpc/ranking"
)

// ErrMissingService は Services に nil のフィールドがあることを表す。
// DI（コンポジションルート）の組み立て漏れなので、起動を中止する種類のエラー。
var ErrMissingService = errors.New("grpc server: service is not assembled")

// Services は登録に必要な全ハンドラを束ねる構造体。
// 機能追加時はこの構造体にハンドラを追加していく。
//
// 全フィールドが埋まっていることを前提とする。部分的に nil のまま登録する構成は
// サポートしない（コンポジションルート internal/di が常に全ハンドラを組み立てる）。
type Services struct {
	Ranking *rankinghandler.Handler
}

// Register は ServiceRegistrar に全 gRPC サービスを登録する。
// ハンドラが1つでも nil なら、サービスを登録せずエラーを返す。
//
// nil チェックが要るのは、組み立て漏れを**読める形**にするため。型付き nil の
// *Handler をそのまま RegisterRankingServiceServer へ渡すと、生成コードが埋め込み検査
// (testEmbeddedByValue) を呼ぶ時点で nil 参照の panic になる（実測）。起動は止まるが、
// 出るのは runtime のスタックトレースだけで、どのサービスが欠けているかは読み取れない。
// しかもこの挙動は生成コードの実装詳細（UnimplementedXxx を値で埋め込む形）に依存して
// いて、形が変われば「登録もサーバ起動も成功し、panic は最初の RPC まで遅延する」方へ
// 倒れる（RegisterXxxServiceServer は interface を満たすかしか見ないため）。
// どちらに転んでも運用側から原因が見えないので、明示的に検査する。
//
// 引数を *grpc.Server ではなく grpc.ServiceRegistrar にしているのは、登録先を
// 差し替えられるほうがテストしやすいため（本番の配線は *grpc.Server をそのまま渡す）。
func Register(s grpc.ServiceRegistrar, svc Services) error {
	if err := svc.validate(); err != nil {
		return err
	}

	rankingv1.RegisterRankingServiceServer(s, svc.Ranking)

	return nil
}

// validate は全ハンドラが組み立てられていることを検査する。
// 欠けたフィールド名をすべて挙げる（1つずつ直して再起動する往復を避けるため）。
// フィールドを追加したらここにも追加する。
func (s Services) validate() error {
	fields := []struct {
		name     string
		assigned bool
	}{
		{"Ranking", s.Ranking != nil},
	}

	var missing []string
	for _, f := range fields {
		if !f.assigned {
			missing = append(missing, f.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrMissingService, strings.Join(missing, ", "))
}
