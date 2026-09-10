package cache

import (
	"sync"
	"testing"
	"time"
)

func TestTTLGetSetDelete(t *testing.T) {
	c := New[string, int](time.Minute)

	if _, ok := c.Get("x"); ok {
		t.Fatal("retrieving unset key returned ok=true")
	}
	c.Set("x", 42)
	if v, ok := c.Get("x"); !ok || v != 42 {
		t.Fatalf("Get = (%d, %v), expected (42, true)", v, ok)
	}
	c.Delete("x")
	if _, ok := c.Get("x"); ok {
		t.Fatal("key remains accessible after Delete")
	}
}

func TestTTLExpires(t *testing.T) {
	c := New[string, string](20 * time.Millisecond)
	c.Set("k", "v")
	if _, ok := c.Get("k"); !ok {
		t.Fatal("freshly inserted entry reported expired")
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expired entry still returned ok=true")
	}
}

func TestTTLJanitorRemovesExpired(t *testing.T) {
	c := New[string, int](10 * time.Millisecond)
	c.Set("a", 1)

	stop := make(chan struct{})
	defer close(stop)
	c.StartJanitor(5*time.Millisecond, stop)

	time.Sleep(50 * time.Millisecond)

	// The janitor goroutine must actively purge expired entries from the underlying map
	// rather than solely relying on lazy eviction upon Get.
	c.mu.RLock()
	n := len(c.items)
	c.mu.RUnlock()
	if n != 0 {
		t.Fatalf("janitor did not purge expired items: %d remaining", n)
	}
}

// Running with -race verifies concurrent thread-safety of Get, Set, and Delete across multiple goroutines.
func TestTTLConcurrent(t *testing.T) {
	c := New[int, int](time.Minute)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				k := base*500 + i
				c.Set(k, k)
				c.Get(k)
				if i%3 == 0 {
					c.Delete(k)
				}
			}
		}(g)
	}
	wg.Wait()
}
