package slogtest

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// NewRecordingLogger は t.Log への転送に加えて、出力テキストを Recorder にも溜める
// *slog.Logger を返す。level に nil を渡した場合は Debug 以上を全て出力する。
//
// 用途は「特定のログが出た / 出ていない」こと自体が仕様になっている箇所の検証
// （例: outbox worker の dead-letter 通知）。目視確認だけで足りる場合は NewLogger を使う。
//
// 捕捉するのは slog.TextHandler が整形した後の1行テキストなので、属性の検証も
// `event_id=1` のような key=value の部分一致で行う。
func NewRecordingLogger(t *testing.T, level slog.Leveler) (*slog.Logger, *Recorder) {
	t.Helper()
	if level == nil {
		level = slog.LevelDebug
	}
	rec := &Recorder{}
	w := io.MultiWriter(newWriter(t), rec)
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})), rec
}

// Recorder は書き込まれたログ行を保持する io.Writer。
// worker のように複数 goroutine からログが出る対象でも使えるよう mutex で保護する。
type Recorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write は io.Writer を満たす。
func (r *Recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

// Lines は捕捉したログ行を返す（空行は除く）。
func (r *Recorder) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lines []string
	for _, l := range strings.Split(r.buf.String(), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// Count は substr を全て含む行の数を返す。substr は AND 条件で評価する。
// 「出た回数」まで見るのは、遷移の瞬間だけ出すべきログが繰り返し出ていないかを
// 検証するため。
func (r *Recorder) Count(substrs ...string) int {
	n := 0
	for _, line := range r.Lines() {
		matched := true
		for _, s := range substrs {
			if !strings.Contains(line, s) {
				matched = false
				break
			}
		}
		if matched {
			n++
		}
	}
	return n
}
