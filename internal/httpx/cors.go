package httpx

// cors.go implements Cross-Origin Resource Sharing (CORS) middleware, enabling authorized
// external origins (differing scheme, host, or port) to invoke the API securely.

import (
	"net/http"
	"strings"
)

// CORS constructs a middleware enforcing cross-origin access control policies.
//
// Because the service relies on cookie-based session authentication:
//   - Access-Control-Allow-Credentials must be set to "true".
//   - Wildcard origins ("*") are strictly prohibited by browser security specifications when credentials are exchanged;
//     the response must echo the explicit requesting origin provided it exists in the allowlist.
//
// If allowed is empty, no CORS headers are emitted, restricting ingress to same-origin requests.
func CORS(allowed []string) Middleware {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		if o = strings.TrimSpace(o); o != "" {
			set[o] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			allowedOrigin := origin != "" && set[origin]

			// Emit Vary: Origin on every request presenting an Origin header (even rejected ones)
			// to ensure downstream caching proxies do not serve cached CORS responses across disparate origins.
			if origin != "" {
				w.Header().Add("Vary", "Origin")
			}

			if allowedOrigin {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
			}

			// Handle preflight OPTIONS requests: respond with 204 No Content and abort downstream execution.
			if r.Method == http.MethodOptions {
				if allowedOrigin {
					h := w.Header()
					h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
					h.Set("Access-Control-Allow-Headers", "Content-Type")
					h.Set("Access-Control-Max-Age", "600") // cache preflight decisions for 10 minutes
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
