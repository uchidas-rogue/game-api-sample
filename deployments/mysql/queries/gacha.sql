-- name: GetUserForUpdate :one
-- ユーザー行を排他ロックで取得する。10連ガチャの整合性確保用。
-- 必ずトランザクション内から呼び出すこと。デッドロック誘発検証のため意図的に FOR UPDATE。
SELECT id, name, gem_num, created_at, updated_at
FROM users
WHERE id = ?
FOR UPDATE;

-- name: UpdateUserGems :exec
-- 指定ユーザーの石残高を更新する。トランザクション内から呼び出す前提。
UPDATE users
SET gem_num = ?
WHERE id = ?;

-- name: ListItems :many
-- アイテムマスタ全件を取得する（排出抽選用）。
SELECT id, name, rarity, weight, created_at
FROM items;

-- name: UpsertUserItem :exec
-- ユーザー所持アイテムを追加。既に存在する場合は所持数を加算する。
INSERT INTO user_items (user_id, item_id, num)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE num = num + VALUES(num);

-- name: InsertGachaHistory :exec
-- ガチャ履歴を1件追加する。
INSERT INTO gacha_histories (user_id, item_id)
VALUES (?, ?);
