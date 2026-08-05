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

-- name: DeleteProcessedOutboxEventsBefore :exec
DELETE FROM outbox_events
WHERE processed_at IS NOT NULL
  AND processed_at < ?;

-- name: MarkOutboxEventsProcessedByIDs :exec
UPDATE outbox_events
SET processed_at = NOW(6)
WHERE id IN (sqlc.slice('ids'));
