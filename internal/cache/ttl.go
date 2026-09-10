// Package cache provides a concurrent, in-memory generic cache with time-to-live (TTL) expiration.
//
// The implementation leverages Go generics (TTL[K comparable, V any]) to provide type-safe
// caching for arbitrary key-value pairs without runtime interface{} boxing overhead.
//
// Typical usage:
//
//	c := cache.New[string, *Document](60 * time.Second)
//	c.StartJanitor(time.Minute, stopCh) // Spawns a background cleaner for expired entries
//	c.Set("id", doc)
//	v, ok := c.Get("id")
//
// Architectural note for distributed deployments:
// This cache resides strictly within local process memory. In multi-instance topologies,
// each instance maintains an independent cache; mutation handlers should favor explicit
// invalidation (Delete) across instances or rely on a distributed cache (e.g. Redis).
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	val       V
	expiresAt time.Time
}

// TTL represents a thread-safe, generic in-memory cache with uniform expiration policies.
// Key type K must satisfy comparable for map indexing; value type V accommodates any type.
type TTL[K comparable, V any] struct {
	mu    sync.RWMutex // Synchronizes read-heavy parallel access while ensuring exclusive write mutations
	items map[K]entry[V]
	ttl   time.Duration
}

// New constructs a generic TTL cache instance with the specified entry expiration duration.
func New[K comparable, V any](ttl time.Duration) *TTL[K, V] {
	return &TTL[K, V]{items: make(map[K]entry[V]), ttl: ttl}
}

// Get retrieves a cached value by key. It returns the value and true if present and unexpired;
// otherwise, it returns the zero value of V and false.
func (c *TTL[K, V]) Get(k K) (V, bool) {
	c.mu.RLock()
	e, ok := c.items[k]
	c.mu.RUnlock() // Avoid defer on the critical hot path to minimize lock contention duration

	var zero V
	if !ok || time.Now().After(e.expiresAt) {
		return zero, false
	}
	return e.val, true
}

// Set inserts or updates an entry, establishing its expiration timestamp relative to the current time.
func (c *TTL[K, V]) Set(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[k] = entry[V]{val: v, expiresAt: time.Now().Add(c.ttl)}
}

// Delete removes a key and its associated value from the cache.
func (c *TTL[K, V]) Delete(k K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, k)
}

// Purge removes all entries from the cache while preserving allocated map capacity.
func (c *TTL[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.items) // Go 1.21+ builtin for zero-allocation map clearing
}

// StartJanitor launches a background goroutine that periodically evicts expired entries.
// The goroutine exits cleanly when the stop channel is closed to prevent resource leaks.
func (c *TTL[K, V]) StartJanitor(every time.Duration, stop <-chan struct{}) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				now := time.Now()
				c.mu.Lock()
				for k, e := range c.items {
					if now.After(e.expiresAt) {
						delete(c.items, k)
					}
				}
				c.mu.Unlock()
			case <-stop:
				return // Prevent goroutine leakage upon cessation signal
			}
		}
	}()
}
