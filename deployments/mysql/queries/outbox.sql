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

-- name: MarkOutboxEventProcessed :exec
UPDATE outbox_events
SET processed_at = NOW(6)
WHERE id = ?;

-- name: IncrementOutboxEventRetry :exec
UPDATE outbox_events
SET retry_count = retry_count + 1,
    last_error  = ?
WHERE id = ?;

-- name: DeleteProcessedOutboxEventsBefore :exec
DELETE FROM outbox_events
WHERE processed_at IS NOT NULL
  AND processed_at < ?;

-- name: GetMaxOutboxEventID :one
-- バッチが「DB を読んだ時点までに COMMIT 済みの outbox イベント」を ID 上限として取得するために使用する。
-- 空テーブルでも 0 を返すよう COALESCE する。
SELECT CAST(COALESCE(MAX(id), 0) AS UNSIGNED) AS max_id
FROM outbox_events;

-- name: MarkOutboxEventsProcessedUpTo :execrows
-- 指定 ID 以下の pending イベントを一括で処理済みにマークする。
-- ranking 同期バッチがスナップショット境界 (max_id) までのイベントを processed として確定させるために使用する。
UPDATE outbox_events
SET processed_at = NOW(6)
WHERE processed_at IS NULL
  AND id <= ?;
