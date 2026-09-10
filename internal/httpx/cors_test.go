package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func corsHandler(allowed []string) http.Handler {
	return CORS(allowed)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func TestCORSAllowedOrigin(t *testing.T) {
	h := corsHandler([]string{"https://app.example.com"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://app.example.com")
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("ACAO = %q, expected https://app.example.com", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("ACAC = %q (cookie authentication necessitates true)", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, expected Origin", got)
	}
}

func TestCORSDisallowedOrigin(t *testing.T) {
	h := corsHandler([]string{"https://app.example.com"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin unexpectedly received ACAO = %q", got)
	}
	// Vary: Origin must persist to ensure intermediary caches isolate responses across origins.
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, expected Origin even upon rejection", got)
	}
}

func TestCORSPreflightShortCircuits(t *testing.T) {
	called := false
	h := CORS([]string{"https://app.example.com"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/documents", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, expected 204", rec.Code)
	}
	if called {
		t.Fatal("preflight request must not execute target business handler")
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("preflight response missing Access-Control-Allow-Methods header")
	}
}
