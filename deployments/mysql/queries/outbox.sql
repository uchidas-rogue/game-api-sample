-- name: InsertOutboxEvent :execlastid
INSERT INTO outbox_events (event_type, payload)
VALUES (?, ?);

-- name: ListPendingOutboxEvents :many
-- 複数 worker をスケールアウトしたときの競合回避で skip locked を付与する。
SELECT id, event_type, payload, retry_count
FROM outbox_events
WHERE processed_at IS NULL
ORDER BY id ASC
LIMIT ?
FOR UPDATE SKIP LOCKED;

-- name: ClaimPendingOutboxEventByID :one
-- 指定 ID の未処理イベントを FOR UPDATE SKIP LOCKED で確保（claim）する。
-- worker は ListPending で得た候補を1件ずつ本クエリで claim し、handleEvent（MySQL 副作用）と
-- MarkProcessed を同一 tx でコミットして exactly-once を担保する。
-- ID 指定にすることで、先頭イベントが恒久失敗しても後続を処理でき（head-of-line blocking 回避）、
-- SKIP LOCKED と processed_at IS NULL 条件により複数 worker が同一イベントを二重処理しない。
-- 既に処理済み or 他 worker がロック中は sql.ErrNoRows（該当なし）。
SELECT id, event_type, payload, retry_count
FROM outbox_events
WHERE id = ? AND processed_at IS NULL
FOR UPDATE SKIP LOCKED;

-- name: MarkOutboxEventProcessed :exec
UPDATE outbox_events
SET processed_at = NOW(6)
WHERE id = ?;

-- name: IncrementOutboxEventRetry :exec
UPDATE outbox_events
SET retry_count = retry_count + 1,
    last_error  = ?
WHERE id = ?;

-- name: DeleteProcessedOutboxEventsBefore :execrows
-- 処理済みイベントのうち、保持期間（秒）より古いものを最大 LIMIT 件削除する。
--
-- 基準時刻は Go 側から渡さず SQL 側の NOW(6) で取る。アプリ側で現在時刻を取得すると
-- AGENTS.md §2 の Clock DI 規約に抵触し、時刻依存でテストが不安定になるため。
--
-- LIMIT で分割するのは、1文で全削除すると undo ログとロック保持が肥大して
-- 同じテーブルへの INSERT（リクエスト経路の outbox 記録）を阻害するため。
-- ORDER BY を idx_outbox_events_pending の先頭列に合わせ、古い順に消していく。
--
-- processed_at IS NULL（未処理）は対象外。恒久失敗イベントの始末は
-- max retry / DLQ の責務であり、GC が消すとイベントが黙って失われる。
DELETE FROM outbox_events
WHERE processed_at IS NOT NULL
  AND processed_at < NOW(6) - INTERVAL sqlc.arg(retention_seconds) SECOND
ORDER BY processed_at
LIMIT ?;

-- name: MarkOutboxEventsProcessedByIDs :exec
UPDATE outbox_events
SET processed_at = NOW(6)
WHERE id IN (sqlc.slice('ids'));
