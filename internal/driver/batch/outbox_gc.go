package batch

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	outboxusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// outboxGCChunkSize は1トランザクションで削除する行数の上限。
// この層固有の運用値なので batch パッケージで定義する（AGENTS.md §2）。
// 環境ごとに変える必要が出たら configs へ移す。
//
// 1文で全削除しないのは、undo ログとロック保持が肥大して同じテーブルへの
// INSERT（リクエスト経路の outbox 記録）を阻害するため。
const outboxGCChunkSize = 1000

// OutboxGC は処理済みの outbox_events を保持期間経過後に削除するバッチ。
//
// outbox-worker は処理済みイベントに processed_at を立てるだけで行を消さないため、
// これが無いとテーブルが単調増加する（負荷試験の実測 約400 events/sec で
// 1日あたり約3,400万行）。
//
// 設計方針:
//   - 1チャンク = 1トランザクションで削除し、対象が尽きるまで繰り返す。
//     長いトランザクションでテーブルを占有しないため。
//   - 削除の基準時刻は SQL 側の NOW() で取る。アプリ側で現在時刻を取得しない
//     （AGENTS.md §2 の Clock 規約）ため、repository には時刻ではなく保持期間を渡す。
//   - 未処理イベント（processed_at IS NULL）は削除しない。恒久失敗イベントの始末は
//     max retry / DLQ の責務であり、ここで消すとイベントが黙って失われる。
//   - トランザクションは READ COMMITTED で開始する（理由は Run のコメント）。
type OutboxGC struct {
	repo      outboxusecase.Repository
	tx        shared.Transactor
	retention time.Duration
	logger    *slog.Logger
}

// NewOutboxGC は OutboxGC を生成する。
func NewOutboxGC(
	repo outboxusecase.Repository,
	tx shared.Transactor,
	retention time.Duration,
	logger *slog.Logger,
) *OutboxGC {
	return &OutboxGC{
		repo:      repo,
		tx:        tx,
		retention: retention,
		logger:    logger,
	}
}

// Run は保持期間を過ぎた処理済みイベントを、対象が尽きるまでチャンク単位で削除する。
// 途中で失敗した場合はその時点で打ち切ってエラーを返す（残りは次回の実行で消せる）。
//
// 各チャンクのトランザクションは READ COMMITTED で開始する。
// idx_outbox_events_pending は (processed_at, id) で、NULL がインデックス先頭に並ぶ。
// 新規 INSERT は processed_at = NULL かつ id 最大なので「NULL ブロックの末尾」、
// すなわち最初の非 NULL レコードの直前のギャップに着地する。一方 DELETE の
// processed_at IS NOT NULL 条件はその非 NULL レコードから走査を始めるため、
// 既定の REPEATABLE READ では最初の next-key ロックが同じギャップを掴み、
// API 側の InsertOutboxEvent を INSERT_INTENTION 待ちでブロックする。
// outbox worker が RC で回っているのと同じ理由（docs/testing/transaction-boundary.md）。
// GC は同一トランザクション内の読み取り一貫性に依存しないため RC で問題ない。
func (g *OutboxGC) Run(ctx context.Context) error {
	g.logger.InfoContext(ctx, "starting outbox gc",
		slog.Duration("retention", g.retention),
		slog.Int("chunk_size", outboxGCChunkSize),
	)

	var total int64
	for {
		var deleted int64
		if err := g.tx.DoInTx(ctx, func(tx shared.Tx) error {
			d, derr := g.repo.DeleteProcessedBefore(ctx, tx, g.retention, outboxGCChunkSize)
			if derr != nil {
				return fmt.Errorf("delete processed outbox events: %w", derr)
			}
			deleted = d
			return nil
		}, shared.WithIsolation(shared.IsolationReadCommitted)); err != nil {
			return fmt.Errorf("outbox gc tx (deleted=%d): %w", total, err)
		}
		total += deleted

		// チャンクが埋まらなかった = 対象が尽きた。0 件でなくても終了してよい。
		if deleted < outboxGCChunkSize {
			break
		}
		// 削除し続けている間に停止要求が来たら打ち切る。次の DoInTx でも失敗するが、
		// 無駄なトランザクションを1本張らずに済む（worker のドレインループと同じ歯止め）。
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("outbox gc interrupted (deleted=%d): %w", total, err)
		}
	}

	g.logger.InfoContext(ctx, "outbox gc completed", slog.Int64("deleted", total))
	return nil
}
