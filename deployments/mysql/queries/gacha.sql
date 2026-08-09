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

