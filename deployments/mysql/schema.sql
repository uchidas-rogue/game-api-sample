-- このファイルは make db/schema/dump で自動生成されます。直接編集しないでください

-- from 000001_create_gacha_tables.up.sql
-- ガチャ機能に必要なテーブル群を作成する。
-- 1. ユーザーテーブル：ガチャを引くための「石（gems）」を持たせる
CREATE TABLE users (
    `id` BIGINT PRIMARY KEY,
    `name` VARCHAR(255) NOT NULL,
    `gem_num` INT NOT NULL DEFAULT 0 COMMENT 'ガチャ石の残高',
    `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
    `updated_at` DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 2. アイテムマスタ：ガチャから排出されるアイテム群
CREATE TABLE items (
    `id` BIGINT PRIMARY KEY,
    `name` VARCHAR(255) NOT NULL,
    `rarity` INT NOT NULL COMMENT 'レアリティ(1:N, 2:R, 3:SR, 4:SSR)',
    `weight` INT NOT NULL COMMENT '排出確率計算用の重み',
    `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 3. ユーザー所持アイテム（インベントリ）
CREATE TABLE user_items (
    `user_id` BIGINT NOT NULL,
    `item_id` BIGINT NOT NULL,
    `num` INT NOT NULL DEFAULT 0 COMMENT '所持数',
    `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
    `updated_at` DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (`user_id`, `item_id`),
    CONSTRAINT `fk_user_items_user_id` FOREIGN KEY (`user_id`) REFERENCES users(`id`),
    CONSTRAINT `fk_user_items_item_id` FOREIGN KEY (`item_id`) REFERENCES items(`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 4. ガチャ履歴テーブル（書き込み負荷増大用）
CREATE TABLE gacha_histories (
    `id` BIGINT AUTO_INCREMENT PRIMARY KEY,
    `user_id` BIGINT NOT NULL,
    `item_id` BIGINT NOT NULL,
    `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT `fk_gacha_histories_user_id` FOREIGN KEY (`user_id`) REFERENCES users(`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- from 000002_create_ranking_tables.up.sql
-- ギルドマスタ
CREATE TABLE guilds (
    `id` BIGINT PRIMARY KEY,
    `name` VARCHAR(255) NOT NULL,
    `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
    `updated_at` DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ギルドメンバー（ユーザーとギルドの関連）
CREATE TABLE guild_members (
    `guild_id` BIGINT NOT NULL,
    `user_id` BIGINT NOT NULL,
    `joined_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (`guild_id`, `user_id`),
    CONSTRAINT `fk_guild_members_guild_id` FOREIGN KEY (`guild_id`) REFERENCES guilds(`id`),
    CONSTRAINT `fk_guild_members_user_id` FOREIGN KEY (`user_id`) REFERENCES users(`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ギルドスコア（正データ、キャッシュミス時のフォールバック）
CREATE TABLE guild_scores (
    `guild_id` BIGINT PRIMARY KEY,
    `score` BIGINT NOT NULL DEFAULT 0,
    `updated_at` DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT `fk_guild_scores_guild_id` FOREIGN KEY (`guild_id`) REFERENCES guilds(`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- ギルドスコア履歴（監査用）
CREATE TABLE guild_score_histories (
    `id` BIGINT AUTO_INCREMENT PRIMARY KEY,
    `guild_id` BIGINT NOT NULL,
    `user_id` BIGINT NOT NULL,
    `score` BIGINT NOT NULL,
    `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT `fk_guild_score_histories_guild_id` FOREIGN KEY (`guild_id`) REFERENCES guilds(`id`),
    CONSTRAINT `fk_guild_score_histories_user_id` FOREIGN KEY (`user_id`) REFERENCES users(`id`),
    INDEX `idx_guild_score_histories_guild_id` (`guild_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 個人ポイント（正データ、キャッシュミス時のフォールバック）
CREATE TABLE user_points (
    `user_id` BIGINT PRIMARY KEY,
    `points` BIGINT NOT NULL DEFAULT 0,
    `updated_at` DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT `fk_user_points_user_id` FOREIGN KEY (`user_id`) REFERENCES users(`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 個人ポイント履歴（監査用）
CREATE TABLE user_point_histories (
    `id` BIGINT AUTO_INCREMENT PRIMARY KEY,
    `user_id` BIGINT NOT NULL,
    `points` BIGINT NOT NULL,
    `reason` VARCHAR(255) NOT NULL,
    `created_at` DATETIME DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT `fk_user_point_histories_user_id` FOREIGN KEY (`user_id`) REFERENCES users(`id`),
    INDEX `idx_user_point_histories_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- from 000003_create_outbox_table.up.sql
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
