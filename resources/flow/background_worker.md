[← Back to Master Flow Catalog](../FLOW.md)

# Flow: Asynchronous Document Indexing Worker Pool

This document details the concurrent background worker pool subsystem in **`godocs`** implemented in [`internal/worker/indexer.go`](file:///home/vnjdev/projects/godocs/internal/worker/indexer.go).

---

## 1. Subsystem Architecture

The background indexing pipeline allows time-consuming document processing (text extraction, summary analysis, thumbnail generation) to occur asynchronously without stalling the HTTP upload response.

### Concurrency Primitives & Synchronization
- **Bounded FIFO Queue**: `jobs chan string` with capacity `128`.
- **Worker Goroutines**: $N$ concurrent workers configured via `APP_WORKERS` (default: 4).
- **Graceful Drain Coordination**: `sync.WaitGroup` monitors active workers.
- **Race Guard for Shutdown**: `sync.RWMutex` serializes task submission against channel closure to prevent runtime panics (`send on closed channel`).

---

## 2. Sequence Diagram

```mermaid
sequenceDiagram
    autonumber
    actor Handler as Document Service (HTTP Thread)
    participant Indexer as worker.Indexer
    participant Queue as jobs chan string (cap 128)
    participant Workers as Worker Goroutines (Pool of N)
    participant DB as SQLite DB

    Note over Handler,Queue: Task Submission (Enqueue)
    Handler->>Indexer: Enqueue(docID)
    Indexer->>Indexer: mu.RLock() (Guards against closed channel)
    alt Channel is closed
        Indexer-->>Handler: false
    else Channel is open
        alt Queue has space (Capacity < 128)
            Indexer->>Queue: jobs <- docID
            Indexer-->>Handler: true (Accepted)
        else Queue is full (Capacity == 128)
            Note over Indexer: Non-blocking default branch
            Indexer-->>Handler: false (Backpressure exerted)
        end
    end
    Indexer->>Indexer: mu.RUnlock()

    Note over Queue,Workers: Concurrent Worker Consumption
    loop Worker Loop (N goroutines)
        Workers->>Queue: id := <-jobs
        Note over Workers: Decoupled Context (30s deadline)
        Workers->>Workers: ctx, cancel = context.WithTimeout(Background(), 30s)
        Workers->>Workers: process(ctx, id) (Simulate text analysis / indexing)
        alt Processing Succeeded
            Workers->>DB: MarkStatus(ctx, id, StatusReady)
        else Processing Failed or Timed Out
            Workers->>DB: MarkStatus(ctx, id, StatusFailed)
        end
        Workers->>Workers: cancel() (Free context resources immediately)
    end
```

---

## 3. Graceful Worker Pool Termination

During service shutdown (`SIGINT`/`SIGTERM`), the worker pool guarantees that in-progress tasks finish completely without abrupt termination:

```mermaid
sequenceDiagram
    autonumber
    actor Main as cmd/api (Shutdown)
    participant Indexer as worker.Indexer
    participant Queue as jobs chan string
    participant Workers as Worker Goroutines
    participant WG as sync.WaitGroup

    Main->>Indexer: Shutdown(shutdownCtx)
    Indexer->>Indexer: mu.Lock() (Acquire exclusive lock)
    Indexer->>Indexer: closed = true
    Indexer->>Queue: close(jobs)
    Indexer->>Indexer: mu.Unlock()

    Note over Workers: Drain Remaining Queued Tasks
    Workers->>Queue: Drain remaining buffered jobs until channel exhausted
    Workers->>WG: wg.Done() (Each worker terminates)

    Indexer->>WG: wg.Wait()
    alt All workers finished within timeout
        WG-->>Indexer: Success
        Indexer-->>Main: nil
    else shutdownCtx expired (20s)
        Indexer-->>Main: context.DeadlineExceeded
    end
```

---

## 4. Key Invariants & Design Principles

1. **Non-Blocking Backpressure**:
   The `select` with `default` construct prevents the HTTP request thread from hanging when background workers are overwhelmed.
2. **Independent Execution Context**:
   Workers create an independent `context.WithTimeout(context.Background(), 30*time.Second)`. If the originating HTTP client disconnects or aborts, the background worker continues to completion unhindered.
3. **No Panic on Closed Channel**:
   The `RWMutex` allows multiple concurrent `Enqueue()` calls (read lock) while reserving exclusive write access for `Shutdown()` (write lock), preventing data races.
