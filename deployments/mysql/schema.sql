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
