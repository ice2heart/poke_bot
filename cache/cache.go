package cache

import (
	"context"
	"sync"
	"time"
)

const (
	cacheCleanPeriod = 10 * time.Second
)

type cacheEntity[T any] struct {
	data T
	time time.Time
}

type Cache[K comparable, V any] struct {
	ctx context.Context

	mux  sync.Mutex
	data map[K]cacheEntity[V]
}

func New[K comparable, V any](ctx context.Context) *Cache[K, V] {
	cache := &Cache[K, V]{
		data: make(map[K]cacheEntity[V]),
		ctx:  ctx,
	}

	go cache.cleanCache()

	return cache
}

func (c *Cache[K, V]) Get(k K) (V, bool) {
	c.mux.Lock()
	defer c.mux.Unlock()

	var val V

	cacheEntity, ok := c.data[k]
	if !ok {
		return val, false
	}

	if cacheEntity.time.Before(time.Now()) {
		return val, false
	}

	val = cacheEntity.data

	return val, true
}

func (c *Cache[K, V]) Set(k K, v V, ttl time.Duration) {
	c.mux.Lock()
	defer c.mux.Unlock()
	c.data[k] = cacheEntity[V]{
		data: v,
		time: time.Now().Add(ttl),
	}
}

// cleanCache инвалидирует данные в кеше, если они устарели
// Работает по схеме crawler
func (c *Cache[K, T]) cleanCache() {
	ticker := time.NewTicker(cacheCleanPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mux.Lock()
			for key, entity := range c.data {
				if time.Now().After(entity.time) {
					delete(c.data, key)
				}
			}
			c.mux.Unlock()
		}
	}
}
