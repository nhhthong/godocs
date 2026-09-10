// Package worker provides asynchronous task processing via an in-memory goroutine worker pool.
//
// Architecture:
// Enqueue dispatches document identifiers to a buffered channel serving as an in-process FIFO queue.
// A pool of N concurrent worker goroutines ranges over this channel, consuming and processing tasks.
// During graceful termination, closing the channel instructs workers to drain remaining jobs before
// exiting; a sync.WaitGroup synchronizes and ensures all workers terminate cleanly.
//
// Concurrency & Backpressure:
// Enqueue employs non-blocking channel semantics. When the queue reaches capacity, it immediately
// returns false (exerting backpressure) rather than blocking the calling HTTP request handler.
//
// Synchronization Invariants:
// A sync.RWMutex coordinates Enqueue and Shutdown, precluding concurrent sends during or after
// channel closure—which would otherwise trigger race detector diagnostics or runtime panics.
package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/you/godocs/internal/document"
)

// Indexer coordinates asynchronous document processing across a bounded worker pool.
type Indexer struct {
	jobs chan string    // Buffered channel serving as an in-process queue
	wg   sync.WaitGroup // Tracks active worker goroutines for graceful termination
	svc  *document.Service
	log  *slog.Logger

	// mu synchronizes task submissions against channel closure.
	// While the Go runtime synchronizes concurrent send/send and send/receive operations,
	// initiating a close concurrently with a send yields undefined behavior and race warnings.
	// RWMutex allows parallel Enqueue operations (RLock) while reserving exclusive
	// access for Shutdown (Lock).
	mu     sync.RWMutex
	closed bool
}

// NewIndexer instantiates an Indexer with a bounded task buffer.
func NewIndexer(svc *document.Service, log *slog.Logger, queueSize int) *Indexer {
	return &Indexer{jobs: make(chan string, queueSize), svc: svc, log: log}
}

// Enqueue submits a document ID for background processing using non-blocking semantics.
// If the buffer is saturated, it immediately returns false to exert backpressure.
// It safely synchronizes with concurrent Shutdown invocations: RLock ensures the channel
// remains open during submission, returning false if already closed.
func (ix *Indexer) Enqueue(id string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if ix.closed {
		return false
	}
	select {
	case ix.jobs <- id:
		return true
	default:
		return false // Queue capacity reached
	}
}

// Start spawns n concurrent worker goroutines consuming from the job queue.
// SetService must be invoked prior to Start; failing to configure the service triggers
// an immediate fail-fast panic rather than deferred nil-pointer panics inside worker goroutines.
func (ix *Indexer) Start(n int) {
	if ix.svc == nil {
		panic("worker: Start() invoked before SetService() — service dependency is unassigned")
	}
	for i := 0; i < n; i++ {
		ix.wg.Add(1) // Add must precede goroutine invocation to prevent race conditions
		go func(id int) {
			defer ix.wg.Done()
			ix.loop(id)
		}(i)
	}
}

// loop processes queued tasks sequentially for a single worker until the channel is closed and drained.
func (ix *Indexer) loop(workerID int) {
	for id := range ix.jobs {
		// Provide an independent execution context with a 30-second deadline to decouple
		// background processing from the originating HTTP request lifecycle.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := ix.process(ctx, id); err != nil {
			ix.log.Error("index failed", "worker", workerID, "doc", id, "err", err)
			// process() often fails because ctx itself timed out; reusing it for the
			// status write would fail too and leave the doc stuck in "pending".
			fctx, fcancel := context.WithTimeout(context.Background(), 5*time.Second)
			if merr := ix.svc.MarkStatus(fctx, id, document.StatusFailed); merr != nil {
				ix.log.Error("mark failed status", "worker", workerID, "doc", id, "err", merr)
			}
			fcancel()
		}
		cancel() // Release context resources immediately; avoid defer within an unbounded loop
	}
}

func (ix *Indexer) process(ctx context.Context, id string) error {
	// Simulation placeholder: text extraction, thumbnail generation, virus scanning, etc.
	select {
	case <-time.After(200 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err() // Propagate cancellation or timeout
	}
	return ix.svc.MarkStatus(ctx, id, document.StatusReady)
}

// Shutdown initiates a graceful termination sequence: it halts new admissions,
// closes the job channel so workers drain remaining tasks, and waits for all
// goroutines to exit or until the context deadline expires.
// Shutdown is idempotent and safe to invoke concurrently.
func (ix *Indexer) Shutdown(ctx context.Context) error {
	ix.mu.Lock() // Await in-flight Enqueue completions, then reject subsequent submissions
	if !ix.closed {
		ix.closed = true
		close(ix.jobs)
	}
	ix.mu.Unlock()

	done := make(chan struct{})
	go func() {
		ix.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetService injects the document service dependency, resolving circular dependency
// requirements during application dependency injection wiring.
func (ix *Indexer) SetService(s *document.Service) { ix.svc = s }
