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
