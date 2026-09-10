[← Back to Master Flow Catalog](../FLOW.md)

# Flow: Background Maintenance Janitors & Sweepers

This document details the periodic background janitor goroutines responsible for automated data cleanup and RAM management in **`godocs`**.

---

## 1. Subsystems Overview

Long-running Go applications must actively manage in-memory structures and storage tables to prevent memory leaks and database bloat:

1. **Session Sweeper** ([`cmd/api/main.go`](file:///home/vnjdev/projects/godocs/cmd/api/main.go) / [`internal/auth`](file:///home/vnjdev/projects/godocs/internal/auth)): Periodic goroutine purging expired login sessions from SQLite.
2. **Cache Janitor** ([`internal/cache/ttl.go`](file:///home/vnjdev/projects/godocs/internal/cache/ttl.go)): Background routine pruning expired items from the in-memory generic cache.

---

## 2. Session Sweeper Lifecycle

```mermaid
sequenceDiagram
    autonumber
    actor System as cmd/api (go sweepSessions)
    participant Ticker as time.NewTicker(1 Hour)
    participant AuthSvc as auth.Service
    participant Repo as db.AuthRepo
    participant DB as SQLite DB

    Note over System,DB: Hourly Background Routine
    loop Every 1 Hour until ctx.Done()
        Ticker->>System: Tick event
        System->>AuthSvc: PurgeExpired(ctx)
        AuthSvc->>Repo: DeleteExpiredSessions(ctx)
        Repo->>DB: DELETE FROM sessions WHERE expires_at < CURRENT_TIMESTAMP
        DB-->>Repo: n rows deleted
        Repo-->>AuthSvc: n
        AuthSvc-->>System: n
        alt n > 0
            System->>System: log.Info("session sweep", "deleted", n)
        end
    end
    Note over System: Terminates cleanly when server context is cancelled
```

---

## 3. Cache Janitor Lifecycle

```mermaid
sequenceDiagram
    autonumber
    actor Cache as cache.TTL (StartJanitor)
    participant Ticker as time.NewTicker(1 Minute)
    participant Store as items map[K]item[V]
    participant Mutex as sync.RWMutex

    Note over Cache,Mutex: Periodic Cache Eviction
    loop Every 1 Minute until stop chan closed
        Ticker->>Cache: Tick event
        Cache->>Mutex: Lock() (Exclusive write access)
        Note over Cache,Store: Scan & Prune Expired Entries
        loop For each (key, item) in items map
            alt time.Now() > item.expiresAt
                Cache->>Store: delete(items, key)
            end
        end
        Cache->>Mutex: Unlock()
    end
    Note over Cache: Exits immediately when close(stop) is called in main.go
```

---

## 4. Key Guarantees

- **No Goroutine Leaks**: Both routines listen to termination signals (`<-ctx.Done()` or `<-stop`) and stop their respective `time.Ticker` instances with `defer t.Stop()`.
- **Bounded Write Lock Duration**: The cache janitor executes a simple dictionary sweep in memory, releasing the `RWMutex` in sub-millisecond time to ensure minimal reader latency.
- **Resource Reclaim**: Dead sessions are deleted from SQLite, enabling SQLite's auto-vacuum or page reuse to keep database file sizes compact.
