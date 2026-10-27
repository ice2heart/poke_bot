package bot

import (
	"context"
	"sync"
	"time"

	"github.com/ice2heart/poke_bot/internal/cache"
)

// banInfoCache remembers, per chat, which users were recently banned.
type banInfoCache struct {
	// ctx is the parent of every per-chat cache.
	ctx context.Context
	// mu guards chats. It is a leaf lock: nothing else is acquired while holding it.
	mu    sync.Mutex
	chats map[int64]*cacheEntity[int64, struct{}]
}

func newBanInfoCache(ctx context.Context) *banInfoCache {
	return &banInfoCache{ctx: ctx, chats: make(map[int64]*cacheEntity[int64, struct{}])}
}

type cacheEntity[K comparable, V any] struct {
	cache  cache.Cache[K, V]
	cancel context.CancelFunc
}

func (c *banInfoCache) cacheBanInfo(chatID int64, userID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	chatCache, ok := c.chats[chatID]
	if !ok {
		chatCache = newCache(c.ctx)
		c.chats[chatID] = chatCache
	}

	chatCache.cache.Set(userID, struct{}{}, time.Minute*30)
}

func (c *banInfoCache) getCachedBanInfo(chatID int64, userID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	chatCache, ok := c.chats[chatID]
	if !ok {
		return false
	}

	_, ok = chatCache.cache.Get(userID)
	return ok
}

func (c *banInfoCache) drop(chatID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if chatCache, ok := c.chats[chatID]; ok {
		chatCache.cancel()
		delete(c.chats, chatID)
	}
}

func newCache(parent context.Context) *cacheEntity[int64, struct{}] {
	ctx, cancel := context.WithCancel(parent)
	return &cacheEntity[int64, struct{}]{
		cancel: cancel,
		cache:  *cache.New[int64, struct{}](ctx),
	}
}
