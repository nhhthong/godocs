package httpx

// ratelimit.go implements IP-based token-bucket rate limiting to mitigate denial-of-service
// and brute-force vectors. Bucket state resides concurrently in process memory.

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimit constructs a middleware enforcing requests-per-second constraints per client IP.
//
// Algorithm: Token Bucket
//   - Each client IP is allocated a bucket retaining up to burst tokens.
//   - Tokens replenish continuously at rate rps (capped at burst).
//   - Each inbound request consumes exactly one token. Token exhaustion triggers 429 Too Many Requests.
//
// Unlike fixed-window counters, the token-bucket algorithm smoothly accommodates brief legitimate bursts
// while strictly curtailing sustained traffic floods. State is preserved in process memory.
func RateLimit(rps float64, burst int) Middleware {
	l := &limiter{
		visitors: make(map[string]*bucket),
		rps:      rps,
		burst:    float64(burst),
	}
	go l.cleanupLoop() // background janitor routine reclaiming memory for inactive IP entries

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.allow(clientIP(r)) {
				w.Header().Set("Retry-After", "1")
				Error(w, http.StatusTooManyRequests, "rate_limited",
					"too many requests, please slow down", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type bucket struct {
	tokens float64   // residual tokens available (fractional due to continuous time-based replenishment)
	last   time.Time // timestamp of the previous replenishment computation
}

type limiter struct {
	mu       sync.Mutex
	visitors map[string]*bucket
	rps      float64
	burst    float64
}

func (l *limiter) allow(ip string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.visitors[ip]
	if !ok {
		// New client IP: allocate fresh bucket at full capacity, deducting 1 token for the current request.
		l.visitors[ip] = &bucket{tokens: l.burst - 1, last: now}
		return true
	}

	// Replenish tokens proportional to elapsed duration: tokens = elapsed_seconds * rps.
	b.tokens += now.Sub(b.last).Seconds() * l.rps
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false // token exhausted; reject request
	}
	b.tokens--
	return true
}

// cleanupLoop periodically sweeps client entries inactive for > 3 minutes to prevent unbounded memory growth.
func (l *limiter) cleanupLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-3 * time.Minute)
		l.mu.Lock()
		for ip, b := range l.visitors {
			if b.last.Before(cutoff) {
				delete(l.visitors, ip)
			}
		}
		l.mu.Unlock()
	}
}

// clientIP extracts the raw host IP from RemoteAddr ("1.2.3.4:5678" -> "1.2.3.4").
//
// Security note: Untrusted X-Forwarded-For or X-Real-IP headers are intentionally ignored here
// to prevent client spoofing. When operating behind trusted reverse proxies (e.g. AWS ALB, Nginx),
// configure proxy headers verification accordingly.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
