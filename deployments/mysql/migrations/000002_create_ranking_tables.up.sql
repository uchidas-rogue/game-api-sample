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
