-- Outbox イベント。リクエスト経路で同一トランザクション内に記録し、
-- outbox-worker が非同期にポーリングして外部副作用（Redis 反映等）を実行する。
CREATE TABLE outbox_events (
    `id` BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    `event_type` VARCHAR(64) NOT NULL,
    `payload` JSON NOT NULL,
    `created_at` DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    `processed_at` DATETIME(6) DEFAULT NULL,
    `retry_count` INT UNSIGNED NOT NULL DEFAULT 0,
    `last_error` TEXT DEFAULT NULL,
    INDEX `idx_outbox_events_pending` (`processed_at`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
