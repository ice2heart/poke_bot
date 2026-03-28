// Package cache provides a generic, TTL-based in-memory cache safe for
// concurrent use. Expired entries are evicted by a background goroutine that
// runs for the lifetime of the provided context.
package cache

import (
	"context"
	"sync"
	"time"
)

// cleanPeriod controls how often the background sweeper removes expired entries.
const cleanPeriod = 10 * time.Second

// entry holds a cached value together with its expiry deadline.
type entry[T any] struct {
	value     T
	expiresAt time.Time
}

// Cache is a generic TTL key-value store. The zero value is not usable;
// create instances with New.
type Cache[K comparable, V any] struct {
	ctx  context.Context
	mu   sync.Mutex
	data map[K]entry[V]
}

// New creates a Cache and starts the background expiry sweeper.
// The sweeper stops when ctx is cancelled.
func New[K comparable, V any](ctx context.Context) *Cache[K, V] {
	c := &Cache[K, V]{
		ctx:  ctx,
		data: make(map[K]entry[V]),
	}
	go c.sweep()
	return c
}

// Get returns the value stored under k and true if the entry exists and has
// not expired. Otherwise it returns the zero value and false.
func (c *Cache[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.data[k]
	if !ok || time.Now().After(e.expiresAt) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores v under k with the given TTL, overwriting any existing entry.
// Calling Set with the same key updates the value and resets the TTL.
func (c *Cache[K, V]) Set(k K, v V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[k] = entry[V]{
		value:     v,
		expiresAt: time.Now().Add(ttl),
	}
}

// Filter returns the values of all live entries whose key satisfies fn.
// Expired entries are excluded even if the sweeper has not yet removed them.
func (c *Cache[K, V]) Filter(fn func(K) bool) []V {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	var result []V
	for k, e := range c.data {
		if now.Before(e.expiresAt) && fn(k) {
			result = append(result, e.value)
		}
	}
	return result
}

// sweep periodically removes entries that have passed their expiry deadline.
// It runs until the cache's context is cancelled.
func (c *Cache[K, V]) sweep() {
	ticker := time.NewTicker(cleanPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			c.mu.Lock()
			for k, e := range c.data {
				if now.After(e.expiresAt) {
					delete(c.data, k)
				}
			}
			c.mu.Unlock()
		}
	}
}
