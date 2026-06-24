package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-redis/redis/v8"
)

// RedisCache is a distributed cache implementation using Redis.
type RedisCache struct {
	client *redis.Client
	ctx    context.Context
}

// NewRedisCache creates a new RedisCache instance.
func NewRedisCache(addr, password string, db int) *RedisCache {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	return &RedisCache{
		client: rdb,
		ctx:    context.Background(),
	}
}

// Get retrieves a value from Redis. Since Redis stores strings/bytes,
// we return the raw bytes. The caller must unmarshal it.
func (c *RedisCache) Get(key string) (interface{}, bool) {
	val, err := c.client.Get(c.ctx, key).Bytes()
	if err == redis.Nil || err != nil {
		return nil, false
	}
	return val, true
}

// Set stores a JSON-marshalled value in Redis.
func (c *RedisCache) Set(key string, value interface{}) {
	bytes, err := json.Marshal(value)
	if err == nil {
		c.client.Set(c.ctx, key, bytes, 24*time.Hour) // 24h default TTL
	}
}

// Delete explicitly removes an element from Redis.
func (c *RedisCache) Delete(key string) {
	c.client.Del(c.ctx, key)
}

// Clear completely wipes the current Redis database.
func (c *RedisCache) Clear() {
	c.client.FlushDB(c.ctx)
}
