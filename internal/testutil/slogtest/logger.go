// Package slogtest はテスト中の slog 出力を testing.T のログに流すヘルパを提供する。
//
// 用途:
//   - DI 経由で *slog.Logger を受け取るコンポーネント（usecase 等）のテストで
//     プロダクトコード側のログを目視確認したい場合に使う。
//   - slog.SetDefault によるグローバル差し替えは行わないため、t.Parallel() と
//     並行テストの相互干渉を避けられる。
package slogtest

import (
	"io"
	"log/slog"
	"testing"
)

// NewLogger は t.Log にテキスト形式で書き込む *slog.Logger を返す。
// level に nil を渡した場合は Debug 以上を全て出力する。
func NewLogger(t *testing.T, level slog.Leveler) *slog.Logger {
	t.Helper()
	if level == nil {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(newWriter(t), &slog.HandlerOptions{Level: level})
	return slog.New(h)
}

// newWriter は *testing.T へ書き込む io.Writer を返す。
func newWriter(t *testing.T) io.Writer {
	return &tWriter{t: t}
}

// tWriter は io.Writer を満たし、書き込み内容を t.Log へ転送する。
// 末尾の改行は t.Log 側で付与されるためトリムする。
type tWriter struct {
	t *testing.T
}

// Write は p を t.Log に転送する。常に len(p) と nil を返す。
func (w *tWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	// 末尾改行を1つだけ取り除いて t.Log の改行と二重化しないようにする。
	msg := p
	if n := len(msg); n > 0 && msg[n-1] == '\n' {
		msg = msg[:n-1]
	}
	w.t.Log(string(msg))
	return len(p), nil
}
