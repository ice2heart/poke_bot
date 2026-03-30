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

func TestCacheFilter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ttl := time.Second
	c := New[string, int](ctx)

	c.Set("a", 1, ttl)
	c.Set("b", 2, ttl)
	c.Set("c", 3, ttl)

	result := c.Filter(func(k string) bool { return k != "b" })
	if len(result) != 2 {
		t.Errorf("expected 2 results, got %d", len(result))
	}
	for _, v := range result {
		if v == 2 {
			t.Errorf("filtered key 'b' should not appear in results")
		}
	}
}

func TestCacheFilterExpired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shortTTL := 50 * time.Millisecond
	longTTL := time.Second
	c := New[string, int](ctx)

	c.Set("expired", 99, shortTTL)
	c.Set("live", 42, longTTL)

	time.Sleep(shortTTL * 2)

	result := c.Filter(func(k string) bool { return true })
	if len(result) != 1 || result[0] != 42 {
		t.Errorf("expected only live entry, got %v", result)
	}
}

func TestCacheDelete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := New[string, int](ctx)
	c.Set("a", 1, time.Second)
	c.Set("b", 2, time.Second)

	c.Delete("a")

	if _, ok := c.Get("a"); ok {
		t.Error("expected key 'a' to be deleted")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("expected key 'b' to remain, got ok=%v val=%d", ok, v)
	}
}

func TestCacheDeleteNoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := New[string, int](ctx)
	// deleting a non-existent key must not panic
	c.Delete("missing")
}

func TestCacheDeleteRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ttl := 50 * time.Millisecond
	c := New[string, int](ctx)

	var wg sync.WaitGroup
	const numOps = 500

	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			c.Set(strconv.Itoa(i), i, ttl)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			c.Delete(strconv.Itoa(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			c.Get(strconv.Itoa(i))
		}
	}()

	wg.Wait()
}

func TestCacheFilterRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ttl := 50 * time.Millisecond
	c := New[string, int](ctx)

	var wg sync.WaitGroup
	const numOps = 500

	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			c.Set(strconv.Itoa(i), i, ttl)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			c.Get(strconv.Itoa(i))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			c.Filter(func(k string) bool { return true })
		}
	}()

	wg.Wait()
}
