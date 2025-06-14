package cache

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestCacheCleanerParallel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ttl := 100 * time.Millisecond
	cache := New[string, int](ctx)

	cache.Set("key1", 42, ttl)
	if val, ok := cache.Get("key1"); ok && val != 42 {
		t.Errorf("Expected 42, got %d", val)
	}

	var wg sync.WaitGroup
	const numWorkers = 100

	var keys []string

	wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		keys = append(keys, strconv.Itoa(i))
		go func(i int) {
			defer wg.Done()
			key := strconv.Itoa(i)
			cache.Set(key, i, ttl)
			val, _ := cache.Get(key)
			if val != i {
				t.Errorf("Expected %d for key %s, got %d", i, key, val)
			}
		}(i)
	}

	wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go func(i int) {
			defer wg.Done()
			key := strconv.Itoa(i)
			cache.Get(key)
		}(i)
	}

	wg.Wait()

	time.Sleep(ttl * 2)
	empty := true
	for _, key := range keys {
		_, ok := cache.Get(key)
		if ok {
			empty = false
		}
	}
	if !empty {
		t.Errorf("Expected cache to be empty after expiration, got %d items", len(cache.data))
	}

	cancel()
	time.Sleep(ttl)
}

func TestCacheCleanerRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ttl := 50 * time.Millisecond
	cache := New[string, int](ctx)

	var wg sync.WaitGroup
	const numOps = 1000

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			cache.Set("key", i, ttl)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			cache.Get("key")
		}
	}()

	wg.Wait()
}
