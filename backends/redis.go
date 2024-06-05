package backends

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	client *redis.Client
	l      sync.Mutex // Add this line to define the mutex
)

// RedisBackend saves the content into Redis.
type RedisBackend struct {
	Ctx        context.Context
	Key        string
	content    bytes.Buffer
	expiration time.Time
}

// ParseRedisConfig parses the connection settings string from the Caddyfile.
func ParseRedisConfig(connSetting string) (*redis.Options, error) {
	var err error
	args := strings.Split(connSetting, " ")
	addr, password, db := args[0], "", 0
	length := len(args)

	if length > 1 {
		db, err = strconv.Atoi(args[1])
		if err != nil {
			return nil, err
		}
	}

	if length > 2 {
		password = args[2]
	}

	return &redis.Options{
		Addr:     addr,
		DB:       db,
		Password: password,
	}, nil
}

// InitRedisClient initializes the client for Redis.
func InitRedisClient(addr, password string, db int) error {
	l.Lock()
	defer l.Unlock()

	if client == nil { // Ensure the client is only initialized once
		client = redis.NewClient(&redis.Options{
			Addr:     addr,
			Password: password,
			DB:       db,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if _, err := client.Ping(ctx).Result(); err != nil {
			return err
		}
	}

	return nil
}

// NewRedisBackend creates a new Redis backend for cache storage.
func NewRedisBackend(ctx context.Context, key string, expiration time.Time) (*RedisBackend, error) {
	return &RedisBackend{
		Ctx:        ctx,
		Key:        key,
		expiration: expiration,
	}, nil
}

// Write writes the response content to a temporary buffer.
func (r *RedisBackend) Write(p []byte) (int, error) {
	return r.content.Write(p)
}

// Flush does nothing here.
func (r *RedisBackend) Flush() error {
	return nil
}

// Length returns the length of the cache content.
func (r *RedisBackend) Length() int {
	return r.content.Len()
}

// Close writes the temporary buffer's content to Redis.
func (r *RedisBackend) Close() error {
	expiration := time.Until(r.expiration)
	if expiration <= 0 {
		expiration = 1 * time.Second // Ensure the key is set with a minimal expiration time
	}

	_, err := client.Set(r.Ctx, r.Key, r.content.Bytes(), expiration).Result()
	return err
}

// Clean performs the purge storage.
func (r *RedisBackend) Clean() error {
	_, err := client.Del(r.Ctx, r.Key).Result()
	return err
}

// GetReader returns a reader for the cached response.
func (r *RedisBackend) GetReader() (io.ReadCloser, error) {
	content, err := client.Get(r.Ctx, r.Key).Result()
	if err != nil {
		return nil, err
	}

	rc := io.NopCloser(strings.NewReader(content))
	return rc, nil
}
