// Package cache wraps the Redis client and owns all cache-key patterns.
// Callers must never build key strings inline (P-023).
package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client is a thin wrapper over go-redis.
type Client struct {
	rdb *redis.Client
}

// Connect parses the Redis URL and pings the server with a bounded timeout.
func Connect(ctx context.Context, redisURL string) (*Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("cache: parse url: %w", err)
	}
	rdb := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("cache: ping: %w", err)
	}
	return &Client{rdb: rdb}, nil
}

// New wraps an existing go-redis client (tests).
func New(rdb *redis.Client) *Client { return &Client{rdb: rdb} }

// R exposes the underlying client for advanced operations.
func (c *Client) R() *redis.Client { return c.rdb }

// Close releases the connection.
func (c *Client) Close() error { return c.rdb.Close() }
