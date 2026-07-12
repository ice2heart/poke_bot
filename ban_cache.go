package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ice2heart/poke_bot/cache"
)

var banCache map[int64]*cacheEntity[int64, struct{}] = make(map[int64]*cacheEntity[int64, struct{}])

type cacheEntity[K comparable, V any] struct {
	cache  cache.Cache[K, V]
	cancel context.CancelFunc
}

func cacheBanInfo(chatID int64, userID int64) {
	chatCache, ok := banCache[chatID]
	if !ok {
		chatCache = newCache()

		banCache[chatID] = chatCache
	}

	chatCache.cache.Set(userID, struct{}{}, time.Minute*30)
}

func getCachedBanInfo(chatID int64, userID int64) bool {
	chatCache, ok := banCache[chatID]
	if !ok {
		chatCache = newCache()

		banCache[chatID] = chatCache
		return false
	}

	_, ok = chatCache.cache.Get(userID)
	return ok
}

func newCache() *cacheEntity[int64, struct{}] {
	ctx, cancel := context.WithCancel(context.Background())
	chatCache := &cacheEntity[int64, struct{}]{
		cancel: cancel,
		cache:  *cache.New[int64, struct{}](ctx),
	}

	go func() {
		intCh := make(chan os.Signal, 1)
		signal.Notify(intCh, os.Interrupt, os.Kill, syscall.SIGTERM)

		select {
		case <-intCh:
			cancel()
		case <-ctx.Done():
			cancel()
		}
	}()

	return chatCache
}
