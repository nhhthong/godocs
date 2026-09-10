package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// When the queue reaches capacity, Enqueue must return false immediately (backpressure) without blocking.
func TestEnqueueBackpressure(t *testing.T) {
	ix := NewIndexer(nil, testLog(), 2) // Queue configured with capacity 2

	if !ix.Enqueue("a") || !ix.Enqueue("b") {
		t.Fatal("initial two enqueue operations must succeed")
	}
	if ix.Enqueue("c") {
		t.Fatal("third enqueue operation must be rejected due to queue saturation")
	}
}

// Enqueue invocations executed concurrently with or subsequent to Shutdown must not panic (e.g. sending on a closed channel).
// Omitting Start avoids requiring a real service instance; with wg=0, Shutdown returns immediately.
func TestEnqueueAfterShutdownNoPanic(t *testing.T) {
	ix := NewIndexer(nil, testLog(), 4)
	if err := ix.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if ix.Enqueue("x") {
		t.Fatal("enqueue following shutdown must return false")
	}

	// Induce aggressive race conditions: execute parallel Enqueue calls while Shutdown is in progress.
	ix2 := NewIndexer(nil, testLog(), 4)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); ix2.Enqueue("y") }()
	}
	_ = ix2.Shutdown(context.Background())
	wg.Wait() // Any unhandled panic triggers an immediate test crash here
}

// Shutdown must behave idempotently across multiple invocations and respect context deadlines.
func TestShutdownIdempotent(t *testing.T) {
	ix := NewIndexer(nil, testLog(), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := ix.Shutdown(ctx); err != nil {
		t.Fatalf("initial shutdown: %v", err)
	}
	if err := ix.Shutdown(context.Background()); err != nil {
		t.Fatalf("subsequent idempotent shutdown: %v", err)
	}
}

// Invoking Start prior to SetService must trigger an explicit fail-fast panic rather than an unhandled nil pointer dereference.
func TestStartWithoutServicePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Start() invoked when svc is nil must panic")
		}
	}()
	NewIndexer(nil, testLog(), 1).Start(1)
}
