package main

// main.go serves as the service entrypoint. Its sole responsibility is dependency wiring:
// reading configuration, initializing the database pool, instantiating storage, cache, and
// worker components, assembling routers and middleware, starting the HTTP server, and
// orchestrating graceful shutdown upon signal interception. Domain logic is strictly encapsulated
// within internal/. Reading run() from top to bottom provides an exhaustive architectural diagram.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/you/godocs/internal/auth"
	"github.com/you/godocs/internal/cache"
	"github.com/you/godocs/internal/config"
	"github.com/you/godocs/internal/db"
	"github.com/you/godocs/internal/document"
	"github.com/you/godocs/internal/httpx"
	"github.com/you/godocs/internal/storage"
	"github.com/you/godocs/internal/worker"
)

func main() {
	// main delegates execution to run() and sets the process exit code accordingly.
	// Executable logic is deferred to run() to ensure proper invocation of defers and error propagation.
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log, closeLog, err := initLogger(cfg.LogDir)
	if err != nil {
		return err
	}
	defer closeLog()
	slog.SetDefault(log)

	// NotifyContext intercepts SIGINT and SIGTERM (Kubernetes termination signals),
	// cancelling ctx rather than abruptly terminating the process. This affords the runtime
	// sufficient time to drain in-flight requests and wind down workers gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- Infrastructure: Database Connection Pool & Storage ---
	pool, err := db.Open(ctx, cfg.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, "migrations"); err != nil { // executes migrations/*.sql sequentially
		return err
	}

	blobs, err := storage.NewLocal(cfg.StorageRoot)
	if err != nil {
		return err
	}

	// --- Document Cache (In-Memory Concurrent TTL) ---
	docCache := cache.New[string, *document.Document](cfg.CacheTTL)
	janitorStop := make(chan struct{})
	defer close(janitorStop)
	docCache.StartJanitor(time.Minute, janitorStop)

	// --- Authentication Subsystem ---
	// AuthRepo satisfies both UserRepo and SessionRepo; it is injected into auth.Service once.
	authRepo := db.NewAuthRepo(pool)
	authSvc := auth.NewService(authRepo, cfg.SessionTTL)
	authHandler := auth.NewHandler(authSvc, cfg.CookieSecure, cfg.SessionTTL)
	go sweepSessions(ctx, authSvc, log) // hourly purging of expired sessions

	// --- Document Subsystem ---
	repo := db.NewDocumentRepo(pool)

	// Explicit manual wiring (devoid of reflection-heavy DI containers) guarantees clear dependency topology.
	var svc *document.Service
	idx := worker.NewIndexer(nil, log, 128)
	svc = document.NewService(repo, blobAdapter{blobs}, docCache, idx, log, cfg.MaxUpload)
	idx.SetService(svc) // breaks the circular initialization between service and worker
	idx.Start(cfg.Workers)

	docHandler := document.NewHandler(svc, fileAdapter{blobs}, cfg.MaxUpload)

	// --- HTTP Routing ---
	mux := http.NewServeMux()

	// /api/auth/*: Unauthenticated endpoint group (invoked to acquire session cookies).
	// A stringent dedicated rate limit (1 req/s, burst 5) guards against brute-force attacks,
	// layered atop the global IP rate-limiting middleware downstream.
	mux.Handle("/api/auth/", httpx.RateLimit(1, 5)(authHandler.Routes()))

	// /api/documents*: Authenticated endpoints. RequireAuth guards the entire document subtree.
	// Note: Multitenancy ownership filtering is currently global; authenticated users share visibility.
	// To implement granular isolation, introduce an owner_id column and filter via auth.UserFrom(ctx).
	protected := authSvc.RequireAuth(docHandler.Routes())
	mux.Handle("/api/documents", protected)  // Go 1.22 mux requires exact pattern without trailing slash
	mux.Handle("/api/documents/", protected) // ...as well as trailing slash pattern for subtree matching

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.PingContext(r.Context()); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "db_down", "database unavailable", nil)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Note: Avoid registering "GET /" explicitly, as method-qualified root patterns conflict
	// with unqualified "/api/..." subtrees in Go 1.22 ServeMux. An unqualified "/" catch-all pattern
	// permits FileServer to handle 404/405 routing correctly.
	mux.Handle("/", http.FileServer(http.Dir("web")))

	// Middleware pipeline — evaluated from outer envelope to inner handler:
	//   CORS      : Injects cross-origin response headers and handles preflight OPTIONS queries.
	//   RequestID : Attaches unique X-Request-Id header for distributed request tracing.
	//   Logging   : Records method, path, status, latency, and payload size (including error statuses).
	//   Recover   : Intercepts unhandled panics, returning 500 without crashing the server process.
	//   RateLimit : Token-bucket rate limiting per client IP (governed by cfg.RateRPS and cfg.RateBurst).
	//   Timeout   : Imposes a 30-second deadline on the request context; handlers must honor ctx.Done().
	handler := httpx.Chain(mux,
		httpx.CORS(cfg.CORSOrigins),
		httpx.WithRequestID,
		httpx.WithLogging(log),
		httpx.WithRecover(log),
		httpx.RateLimit(cfg.RateRPS, cfg.RateBurst),
		httpx.WithTimeout(30*time.Second),
	)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,  // mitigates Slowloris Denial-of-Service vectors
		ReadTimeout:       60 * time.Second, // accommodates multi-megabyte multipart uploads
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	// Server runs concurrently; main goroutine blocks awaiting termination signal.
	errCh := make(chan error, 1) // buffered with capacity 1 to avoid goroutine leak upon exit
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Shutdown HTTP listeners first to cease accepting new ingress traffic while draining in-flight requests,
	// followed by worker pool termination. errors.Join aggregates failures from both stages.
	var errs []error
	if err := srv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("http shutdown: %w", err))
	}
	if err := idx.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("worker shutdown: %w", err))
	}
	log.Info("bye")
	return errors.Join(errs...)
}

// sweepSessions periodically purges expired authentication sessions until ctx is cancelled during shutdown.
func sweepSessions(ctx context.Context, a *auth.Service, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := a.PurgeExpired(context.Background())
			if err != nil {
				log.Error("session sweep", "err", err)
				continue
			}
			if n > 0 {
				log.Info("session sweep", "deleted", n)
			}
		}
	}
}

// --- Adapters: Bridge storage.Local to consumer-defined interfaces in package document ---
// The rationale for adapters: storage.Local returns storage.SavedFile, whereas document.BlobStore
// requires document.StoredBlob. Although structurally equivalent, cross-package structural isolation
// ensures package document remains completely agnostic of storage implementations.

type blobAdapter struct{ l *storage.Local }

func (b blobAdapter) Save(ctx context.Context, ext string, r io.Reader) (document.StoredBlob, error) {
	s, err := b.l.Save(ctx, ext, r)
	if err != nil {
		return document.StoredBlob{}, err
	}
	return document.StoredBlob{Path: s.Path, Size: s.Size, Checksum: s.Checksum}, nil
}

func (b blobAdapter) Delete(ctx context.Context, path string) error { return b.l.Delete(ctx, path) }

type fileAdapter struct{ l *storage.Local }

func (f fileAdapter) Open(path string) (io.ReadSeekCloser, error) {
	return f.l.Open(context.Background(), path)
}

// initLogger configures a structured slog.Logger streaming concurrently to standard output
// and a daily-rotated log file in logDir (e.g. log/2026-09-10.log).
// If logDir is empty or equals "stdout", the logger emits solely to the terminal.
func initLogger(logDir string) (*slog.Logger, func(), error) {
	writers := []io.Writer{os.Stdout}
	closeFn := func() {}

	if logDir != "" && logDir != "stdout" {
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return nil, nil, fmt.Errorf("failed to create log directory: %w", err)
		}

		// Naming convention follows standard ISO daily rotation: log/YYYY-MM-DD.log
		logPath := filepath.Join(logDir, time.Now().Format("2006-01-02")+".log")
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to open log file: %w", err)
		}

		writers = append(writers, f)
		closeFn = func() {
			_ = f.Sync()
			_ = f.Close()
		}
	}

	mw := io.MultiWriter(writers...)
	logger := slog.New(slog.NewJSONHandler(mw, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return logger, closeFn, nil
}
