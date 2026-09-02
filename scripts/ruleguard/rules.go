//go:build ruleguard

// Package gorules は golangci-lint の gocritic/ruleguard で評価される AST ルールを定義する。
//
// forbidigo は「呼び出しの関数名」しか見ないため、引数の内容まで条件に含める規約は
// ここに書く（determ. §3「可能な限り構文解析ベースで判定する」）。
//
// このファイルは `//go:build ruleguard` タグによりアプリのビルド対象から外れている。
// dsl パッケージは golangci-lint に同梱されており、go.mod への依存追加は不要。
package gorules

import "github.com/quasilyte/go-ruleguard/dsl"

// slogErrorAttr は AGENTS.md §2「エラーをログに含める際は slog.Any("error", err) を使用する」
// を強制する。slog.String("error", ...) の形だけを対象にし、
// slog.String("request_id", ...) のような正当な用途は対象外にする。
func slogErrorAttr(m dsl.Matcher) {
	m.Match(`slog.String("error", $*_)`).
		Report(`エラーは slog.Any("error", err) で記録する（AGENTS.md §2）。slog.String("error", err.Error()) は不可`)
}

// echoPreMiddleware は AGENTS.md §2「HTTP ミドルウェアの登録順」のうち、
// 「`echo.Echo.Pre` は使わない」を強制する。
//
// 【この規則が守るもの / 既知の実例】
// BodyLimit をアクセスログより外側に登録した実装で、413 で弾いたリクエストが
// アクセスログにも request_id にも残らない状態になった。登録「順序」そのものは
// TestNew_MiddlewareOrder が New の AST を読んで検証しているが、Pre で登録された
// ミドルウェアはルーティング前・アクセスログより外側で走るため、その検査を迂回する。
// 同じ失敗（拒否が観測できない）への、もう一方の入口をここで塞ぐ。
//
// 【なぜ順序そのものは ruleguard で書けないか】
// ruleguard は式単位のマッチで、文の並び（どの e.Use が先か）を条件にできない。
// 順序は AST を直接読むテスト側の担当（determ. §3）。ここは「呼び出しの有無」だけを見る。
//
// 【違反が出たときの直し方】
// //nolint で抜けない（determ. §2）。ルーティング前の書き換えが要るなら、
// アクセスログとの前後関係を含めて AGENTS.md §2 の規約自体を見直すこと。
func echoPreMiddleware(m dsl.Matcher) {
	// 型式でフルパスは書けないため、パッケージを登録して短縮名で参照する。
	// 型を条件に入れるのは、無関係な型の Pre メソッドを巻き込まないため。
	m.Import(`github.com/labstack/echo/v4`)

	m.Match(`$e.Pre($*_)`).
		Where(m["e"].Type.Is(`*echo.Echo`)).
		Report(`echo.Echo.Pre は使わない（AGENTS.md §2）。Pre はアクセスログより外側で走るため、そこで短絡したリクエストはアクセスログにも request_id にも残らない。e.Use でアクセスログの後ろに登録すること`)
}

// grpcSingleInterceptor は AGENTS.md §2「HTTP ミドルウェアの登録順」を gRPC へ写した
// 規約（観測系を最外側に固定する）のうち、単数形インターセプタの禁止を強制する。
//
// 【この規則が守るもの / 既知の実例】
// echo 側で BodyLimit をアクセスログより外側に登録した結果、413 で弾いたリクエストが
// アクセスログにも request_id にも残らなくなった事例と同じものを gRPC で防ぐ。
// gRPC の登録順は grpc.ChainUnaryInterceptor / ChainStreamInterceptor の引数の並びで
// 決まり、TestNewGRPC_UnaryInterceptorOrder / TestNewGRPC_StreamInterceptorOrder が
// NewGRPC の AST を読んでその並びを検証している。
// ところが単数形の grpc.UnaryInterceptor / grpc.StreamInterceptor を併用すると、
// grpc-go 側では単数形が別枠で保持され Chain 系と合成されるため、AST 検査が
// 見ていない場所にインターセプタを1本足せてしまう。recover をそこへ足せば、
// 検査を通ったまま「panic した RPC がアクセスログに残らない」状態を作れる。
// echo.Echo.Pre を禁止して TestNew_MiddlewareOrder の迂回を塞いだのと同じ構図。
//
// 【なぜ順序そのものは ruleguard で書けないか】
// ruleguard は式単位のマッチで、引数の並びが規約どおりかを条件にできない。
// 並びは AST を直接読むテスト側の担当（determ. §3）。ここは「呼び出しの有無」だけを見る。
//
// 【違反が出たときの直し方】
// //nolint で抜けない（determ. §2）。grpc.ChainUnaryInterceptor /
// grpc.ChainStreamInterceptor の引数へ、観測系より後ろの位置で追加すること。
func grpcSingleInterceptor(m dsl.Matcher) {
	m.Match(`grpc.UnaryInterceptor($*_)`, `grpc.StreamInterceptor($*_)`).
		Report(`単数形の grpc.UnaryInterceptor / grpc.StreamInterceptor は使わない（AGENTS.md §2）。Chain 系と併用すると登録順の AST 検査（TestNewGRPC_*InterceptorOrder）が見ない場所にインターセプタが増え、観測系より外側で短絡させられる。grpc.ChainUnaryInterceptor / grpc.ChainStreamInterceptor の引数として、観測系の後ろに登録すること`)
}

// testGlobalWrite は AGENTS.md §3 Flaky 防止「テストコードでパッケージスコープの変数を
// 書き換えない」を強制する。
//
// 【なぜ gochecknoglobals では足りないか】
// gochecknoglobals は「宣言」を見るため、読み取り専用フィクスチャ（テーブル駆動の
// 共有ケース配列など）まで落ちてしまう。そのため .golangci.yml で _test.go を除外して
// あり、結果としてテストの pkg-global 書換だけが機械検証されない穴になっていた。
// 本ルールは宣言ではなく「書換」を見るので、読み取り専用フィクスチャは対象外になる。
// この2つで役割が分かれる: 宣言 = gochecknoglobals（本番コード）／書換 = 本ルール（テスト）。
//
// 【t.Parallel() の有無で条件を分けない理由】
// ruleguard は式単位のマッチなので「同じ関数が t.Parallel() を呼んでいるか」を条件に
// できない。加えて、今 t.Parallel() が無くても後から付けた瞬間に競合が生まれるため、
// 「テストコードでは pkg-global を書き換えない」を一律の規則にするほうが安定する
// （determ. §2「判断基準を有限の規則セットに落とし込む」）。
//
// 【検出できないもの（既知の検出漏れ / determ. §3）】
//   - グローバルの**フィールド**書換（`globalCfg.Timeout = x`）: フィールドの types.Object は
//     パッケージスコープに属さないため IsGlobal() が false になる
//   - ポインタ経由の間接書換（`p := &global; *p = x`）
//   - グローバルを引数で渡した先での書換
//
// 【違反が出たときの直し方】
// //nolint で個別に抜け道を作らない（determ. §2）。次のいずれかを取る:
//   - フィクスチャを関数化してケースごとに新しい値を返す（推奨）
//   - 環境変数・作業ディレクトリなどプロセス global なら t.Setenv / t.Chdir を使う
//   - どうしても共有状態が要るなら t.Parallel() を外して serial に分離する
func testGlobalWrite(m dsl.Matcher) {
	// 変数そのものへの代入・複合代入・インクリメント
	m.Match(
		`$x = $_`,
		`$x += $_`, `$x -= $_`, `$x *= $_`, `$x /= $_`, `$x %= $_`,
		`$x |= $_`, `$x &= $_`, `$x ^= $_`, `$x <<= $_`, `$x >>= $_`,
		`$x++`, `$x--`,
	).
		Where(m.File().Name.Matches(`_test\.go$`) &&
			m["x"].Object.Is("Var") &&
			m["x"].Object.IsGlobal()).
		Report(`テストからパッケージスコープの変数 $x を書き換えている（AGENTS.md §3 Flaky 防止）。t.Parallel() 配下で他のテストへ汚染が波及する。フィクスチャを関数化するか、serial に分離すること`)

	// 要素への書込み（共有の map / slice を書き換えるケース）。
	// 変数自体は再代入されないが、共有状態の汚染としては同じ。
	m.Match(
		`$x[$_] = $_`,
		`$x[$_] += $_`, `$x[$_] -= $_`, `$x[$_] *= $_`, `$x[$_] /= $_`,
		`$x[$_]++`, `$x[$_]--`,
	).
		Where(m.File().Name.Matches(`_test\.go$`) &&
			m["x"].Object.Is("Var") &&
			m["x"].Object.IsGlobal()).
		Report(`テストからパッケージスコープの $x の要素を書き換えている（AGENTS.md §3 Flaky 防止）。共有の map / slice はテスト間で汚染が波及する。フィクスチャを関数化すること`)
}
