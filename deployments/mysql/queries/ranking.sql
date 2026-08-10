-- name: GetGuild :one
SELECT id, name, created_at, updated_at
FROM guilds
WHERE id = ?;

-- name: GetUser :one
SELECT id, name, gem_num, created_at, updated_at
FROM users
WHERE id = ?;

-- name: IncrementGuildScore :exec
INSERT INTO guild_scores (guild_id, score)
VALUES (?, ?)
ON DUPLICATE KEY UPDATE score = score + VALUES(score);

-- name: InsertGuildScoreHistory :exec
INSERT INTO guild_score_histories (guild_id, user_id, score)
VALUES (?, ?, ?);

-- name: GetUserGuildID :one
SELECT guild_id
FROM guild_members
WHERE user_id = ?
LIMIT 1;

-- name: ListGuildsByIDs :many
SELECT id, name, created_at, updated_at
FROM guilds
WHERE id IN (sqlc.slice('ids'));

-- name: ListAllGuildScores :many
SELECT guild_id, score, updated_at
FROM guild_scores
ORDER BY score DESC;

-- name: GetUserPoints :one
SELECT user_id, points, updated_at
FROM user_points
WHERE user_id = ?;

-- name: IncrementUserPoints :exec
INSERT INTO user_points (user_id, points)
VALUES (?, ?)
ON DUPLICATE KEY UPDATE points = points + VALUES(points);

-- name: InsertUserPointHistory :exec
INSERT INTO user_point_histories (user_id, points, reason)
VALUES (?, ?, ?);

-- name: ListUsersByIDs :many
SELECT id, name, gem_num, created_at, updated_at
FROM users
WHERE id IN (sqlc.slice('ids'));

-- name: ListAllUserPoints :many
SELECT user_id, points, updated_at
FROM user_points
ORDER BY points DESC;
