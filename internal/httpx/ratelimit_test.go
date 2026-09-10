package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Verifies token-bucket burst semantics: with burst=3, the initial 3 requests pass immediately,
// whereas subsequent requests are throttled with 429 Too Many Requests.
func TestRateLimitBurst(t *testing.T) {
	h := RateLimit(1, 3)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var ok, limited int
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		h.ServeHTTP(rec, req)

		switch rec.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unexpected status code: %d", rec.Code)
		}
	}

	if ok != 3 || limited != 2 {
		t.Fatalf("ok=%d limited=%d; expected ok=3 limited=2", ok, limited)
	}
}

// Verifies per-IP bucket isolation: distinct client IPs maintain independent rate-limiting buckets.
func TestRateLimitPerIP(t *testing.T) {
	h := RateLimit(1, 1)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	call := func(ip string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip + ":1000"
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if got := call("1.1.1.1"); got != http.StatusOK {
		t.Fatalf("IP1 request 1 failed: %d", got)
	}
	if got := call("1.1.1.1"); got != http.StatusTooManyRequests {
		t.Fatalf("IP1 request 2 expected 429 rate limit, got %d", got)
	}
	if got := call("2.2.2.2"); got != http.StatusOK {
		t.Fatalf("IP2 request 1 failed despite independent bucket: %d", got)
	}
}
