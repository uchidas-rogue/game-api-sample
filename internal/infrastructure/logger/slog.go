// Package logger は構造化ログ（slog）の初期化を提供する。
package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"
)

const (
	// logsDir はログファイルを格納するディレクトリ。
	logsDir = "logs"
)

// New はJSONハンドラ付きのslog.Loggerを生成する。
// levelがDebugのとき、stdoutに加えてlogs/YYYY-MM-DD.logにも同時出力する。
// ログファイルはOpenAppend（追記）モードで開く。
func New(level slog.Level) *slog.Logger {
	w, closer := buildWriter(level)

	// アプリ終了時のファイルクローズはmain側のdeferで行わず、
	// プロセスが終了すればOSが回収するため意図的にcloserを返さない。
	// ただし将来graceful shutdown時にFlushしたい場合はcloserを公開する。
	_ = closer

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

// buildWriter はレベルに応じたio.Writerとクローズ関数を返す。
// debugレベルのみファイルも追加する。
func buildWriter(level slog.Level) (io.Writer, func() error) {
	if level != slog.LevelDebug {
		return os.Stdout, func() error { return nil }
	}

	path := fmt.Sprintf("%s/%s.log", logsDir, time.Now().Format(time.DateOnly))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// ファイルが開けなくても標準出力だけで継続する（起動を止めない）
		slog.Warn("failed to open log file, falling back to stdout only",
			slog.String("path", path),
			slog.String("error", err.Error()),
		)
		return os.Stdout, func() error { return nil }
	}

	return io.MultiWriter(os.Stdout, f), f.Close
}
