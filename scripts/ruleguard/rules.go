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
