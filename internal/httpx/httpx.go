// Package httpx provides shared HTTP infrastructure utilities independent of domain logic.
//
// It encompasses response serialization helpers (JSON, Error, DecodeJSON), composable middleware
// (WithRequestID, WithLogging, WithRecover, WithTimeout, CORS, RateLimit), and pipeline chaining (Chain).
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = iota // unexported context key preventing collision across packages

// RequestID extracts the request tracing identifier from the context, if present.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// Middleware defines a composable HTTP middleware function wrapper.
type Middleware func(http.Handler) http.Handler

// Chain aggregates a sequence of middleware into a single composite handler.
// Middleware are applied in reverse order, ensuring the first argument wraps outermost:
// Chain(h, a, b, c) evaluates as a(b(c(h))).
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// WithRequestID ensures every ingress request possesses an X-Request-Id header,
// generating an 8-byte cryptographically random hex identifier if none is supplied.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// statusRecorder captures HTTP response status codes and byte counts for structured telemetry.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush implements http.Flusher, facilitating real-time streaming such as Server-Sent Events.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom implements io.ReaderFrom, allowing http.ServeContent to optimize transfers via kernel sendfile(2).
func (s *statusRecorder) ReadFrom(r io.Reader) (int64, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if rf, ok := s.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(r)
		s.bytes += int(n)
		return n, err
	}
	n, err := io.Copy(s.ResponseWriter, r)
	s.bytes += int(n)
	return n, err
}

// Unwrap exposes the underlying http.ResponseWriter for reflection-based interface checks.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// WithLogging produces structured access log entries recording method, path, status, latency, and payload size.
func WithLogging(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			log.InfoContext(r.Context(), "http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"dur_ms", time.Since(start).Milliseconds(),
				"req_id", RequestID(r.Context()),
			)
		})
	}
}

// WithRecover intercepts unexpected runtime panics within downstream handlers,
// logging the error with stack context and returning a 500 Internal Server Error without terminating the process.
func WithRecover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// http.ErrAbortHandler indicates intentional request termination; rethrow unchanged.
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					log.Error("panic", "err", rec, "path", r.URL.Path, "req_id", RequestID(r.Context()))
					Error(w, http.StatusInternalServerError, "internal_error", "an internal error occurred", nil)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// WithTimeout enforces an execution deadline across the request context.
func WithTimeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// DecodeJSON decodes a JSON request payload into the destination struct.
// It imposes a strict 1MB size limit via io.LimitReader and rejects unmapped attributes with DisallowUnknownFields.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// JSON serializes the provided value into standard JSON, setting Content-Type and HTTP status headers.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// ErrorBody represents the standardized JSON error envelope.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fields  any    `json:"fields,omitempty"`
}

// Error serializes an ErrorBody envelope into a formatted JSON error response.
func Error(w http.ResponseWriter, status int, code, msg string, fields any) {
	JSON(w, status, map[string]any{"error": ErrorBody{Code: code, Message: msg, Fields: fields}})
}
