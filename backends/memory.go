package backends

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/mailgun/groupcache/v2"
	"github.com/adammakowskidev/caddy-cache-engine/pkg/helper"
)

type ctxKey string

// Custom error for missing precollect content in memory cache.
type NoPreCollectError struct {
	Content string
}

// Error returns the error message.
func (e NoPreCollectError) Error() string {
	return e.Content
}

// NewNoPreCollectError creates a new NoPreCollectError.
func NewNoPreCollectError(msg string) error {
	return NoPreCollectError{Content: msg}
}

const (
	getterCtxKey    ctxKey = "getter"
	getterTTLCtxKey ctxKey = "getterTTL"
)

var (
	groupName = "http_cache"
	groupch   *groupcache.Group
	pool      *groupcache.HTTPPool
	mu        sync.Mutex
	srv       *http.Server
)

// InMemoryBackend saves the content into memory with groupcache.
type InMemoryBackend struct {
	Ctx              context.Context
	Key              string
	expiration       time.Time
	content          bytes.Buffer
	isContentWritten bool
	cachedBytes      []byte
}

// GetGroupCachePool returns the groupcache's HTTP pool.
func GetGroupCachePool() *groupcache.HTTPPool {
	return pool
}

// ReleaseGroupCacheRes releases the resources the memory backend collects.
func ReleaseGroupCacheRes() error {
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			return err
		}
	}
	return nil
}

// InitGroupCacheRes initializes the resources for groupcache. This should be called during the handler provision stage.
func InitGroupCacheRes(maxSize int) error {
	mu.Lock()
	defer mu.Unlock()

	if pool == nil || groupch == nil {
		poolOptions := &groupcache.HTTPPoolOptions{}

		ip, err := helper.IPAddr()
		if err != nil {
			return err
		}

		self := "http://" + ip.String()
		if pool == nil {
			pool = groupcache.NewHTTPPoolOpts(self, poolOptions)
		}

		if groupch == nil {
			groupch = groupcache.NewGroup(groupName, int64(maxSize), groupcache.GetterFunc(getter))
		}

		mux := http.NewServeMux()
		mux.Handle("/_groupcache/", pool)
		srv = &http.Server{
			Addr:    ":http",
			Handler: mux,
		}

		errChan := make(chan error, 1)

		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				errChan <- err
			}
		}()

		errChan <- nil

		return <-errChan
}

// getter fetches the cached content based on the provided key.
func getter(ctx context.Context, key string, dest groupcache.Sink) error {
	p, ok := ctx.Value(getterCtxKey).([]byte)
	if !ok {
		return NewNoPreCollectError("no precollect content")
	}

	ttl, ok := ctx.Value(getterTTLCtxKey).(time.Time)
	if !ok {
		return errors.New("no TTL provided")
	}

	return dest.SetBytes(p, ttl)
}

// NewInMemoryBackend creates a new instance of InMemoryBackend.
func NewInMemoryBackend(ctx context.Context, key string, expiration time.Time) (*InMemoryBackend, error) {
	return &InMemoryBackend{
		Ctx:        ctx,
		Key:        key,
		expiration: expiration,
	}, nil
}

// Write adds the response content to the context for the groupcache's setter function.
func (i *InMemoryBackend) Write(p []byte) (int, error) {
	i.isContentWritten = true
	return i.content.Write(p)
}

// Flush does nothing here but is required to implement some interfaces.
func (i *InMemoryBackend) Flush() error {
	return nil
}

// Clean purges the storage.
func (i *InMemoryBackend) Clean() error {
	return groupch.Remove(i.Ctx, i.Key)
}

// Close writes the temporary buffer's content to the groupcache.
func (i *InMemoryBackend) Close() error {
	if i.isContentWritten {
		i.Ctx = context.WithValue(i.Ctx, getterCtxKey, i.content.Bytes())
		i.Ctx = context.WithValue(i.Ctx, getterTTLCtxKey, i.expiration)
		err := groupch.Get(i.Ctx, i.Key, groupcache.AllocatingByteSliceSink(&i.cachedBytes))
		if err != nil {
			caddy.Log().Named("backend:memory").Error(err.Error())
			return err
		}
	}
	return nil
}

// Length returns the length of the cached content.
func (i *InMemoryBackend) Length() int {
	if i.cachedBytes != nil {
		return len(i.cachedBytes)
	}
	return 0
}

// GetReader returns a reader for the cached response content.
func (i *InMemoryBackend) GetReader() (io.ReadCloser, error) {
	if len(i.cachedBytes) == 0 {
		err := groupch.Get(i.Ctx, i.Key, groupcache.AllocatingByteSliceSink(&i.cachedBytes))
		if err != nil {
			caddy.Log().Named("backend:memory").Warn(err.Error())
			return nil, err
		}
	}
	return io.NopCloser(bytes.NewReader(i.cachedBytes)), nil
}
