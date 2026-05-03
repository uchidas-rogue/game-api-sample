// Package redis は Redis 接続まわりのインフラを提供する。
package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Client は Redis クライアントのラッパー。
type Client struct {
	rdb *redis.Client
}

// NewClient は Redis クライアントを生成する。
func NewClient(addr string) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
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
