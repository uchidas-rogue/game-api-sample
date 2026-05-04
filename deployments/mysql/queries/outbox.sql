-- name: InsertOutboxEvent :execlastid
INSERT INTO outbox_events (event_type, payload)
VALUES (?, ?);

-- name: ListPendingOutboxEvents :many
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
