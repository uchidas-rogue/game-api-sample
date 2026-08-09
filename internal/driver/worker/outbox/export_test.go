package outbox

import "context"

// ApplyPerEventForTest は applyPerEvent（イベント単位トランザクションのフォールバック経路）を
// 外部テストパッケージから直接呼ぶための seam。
// 戻り値は (listed, applied) —— listed は ListPending の取得件数、applied は処理済みマークまで
// 到達した件数。両者を分けて返すのが枯渇判定の前提なので、テストからも両方を検証する
// （docs/testing/outbox-worker.md §0-2）。
//
// この seam を置く理由:
// 「バッチ失敗 → フォールバックへ切り替わる」こと自体は Run を通した
// TestWorker_runOnce_バッチ失敗時はフォールバックへ切り替わり処理が前進する と
// TestWorker_applyBatch_各ステップの失敗でRedisに到達せずフォールバックする で検証している。
// 一方この経路の内部挙動（claim → applyEventInTx → MarkProcessed、失敗時の IncrementRetry、
// head-of-line blocking 回避、claim 不可のスキップ、並列処理）は分岐が多く、
// Run 経由だと毎ケースで主経路をわざと失敗させる前準備が必要になり検証の意図が埋もれる。
// 経路単体の振る舞いはここから直接呼び、決定的かつ簡潔に検証する。
func (w *Worker) ApplyPerEventForTest(ctx context.Context) (listed, applied int, err error) {
	return w.applyPerEvent(ctx)
}
