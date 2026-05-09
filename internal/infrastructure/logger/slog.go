// Package logger は構造化ログ（slog）の初期化を提供する。
package logger

import (
	"log/slog"
	"os"
)

// New はJSONハンドラ付きのslog.Loggerを生成する。
// 出力先は常に stdout。ファイル保存が必要なときは呼び出し側でシェルリダイレクトする
// （例: Makefile の run/debug で `2>&1 | tee logs/<date>.log`）。
func New(level slog.Level) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}
