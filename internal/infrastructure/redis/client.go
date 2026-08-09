// Package redis は Redis 接続まわりのインフラを提供する。
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// pingTimeout は起動時の疎通確認1回ぶんの上限。
// go-redis の DialTimeout 既定に任せても有限にはなるが、上限が設定として見えず、
// MySQL 側（configs.DBPingTimeout）と非対称になる。この層固有の運用値として明示する。
// 環境ごとに変える必要が出たら configs へ移す。
const pingTimeout = 5 * time.Second

// Client は Redis クライアントのラッパー。
type Client struct {
	rdb *redis.Client
}

// NewClient は Redis クライアントを生成し、起動時の疎通確認まで行う。
// ctx は呼び出し元のシャットダウン ctx を渡すこと（疎通確認を中断できるようにするため）。
// 疎通確認には pingTimeout の期限を被せ、未到達を有限時間で確定エラーにする（fail fast）。
func NewClient(ctx context.Context, addr string) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
	})

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("failed to connect redis: %w", err)
	}

	return &Client{rdb: rdb}, nil
}

// Close は Redis 接続を閉じる。
func (c *Client) Close() error {
	return c.rdb.Close()
}

// Raw は内部の *redis.Client を返す（infrastructure 層内部用）。
func (c *Client) Raw() *redis.Client {
	return c.rdb
}
