package auth

// middleware.go provides authentication middleware (RequireAuth) to restrict endpoints
// to requests bearing a valid session token, injecting the resolved *User into the request context.
// Helper function UserFrom retrieves the authenticated identity within downstream handlers.

import (
	"context"
	"net/http"

	"github.com/you/godocs/internal/httpx"
)

// cookieName specifies the HTTP cookie identifier bearing the session token.
const cookieName = "godocs_session"

// Package-private context key preventing cross-package collision in request contexts.
type ctxKey int

const userKey ctxKey = iota

// RequireAuth wraps an http.Handler, verifying session validity prior to execution.
// Missing or expired sessions return 401 Unauthorized. Valid sessions inject *User into the context.
//
// Usage:
//
//	protected := authSvc.RequireAuth(docHandler.Routes())
//	mux.Handle("/api/documents/", protected)
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil { // http.ErrNoCookie
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "authentication required", nil)
			return
		}

		u, err := s.Authenticate(r.Context(), c.Value)
		if err != nil {
			// Clear invalid or expired cookie on the client
			http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1})
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "invalid or expired session", nil)
			return
		}

		ctx := context.WithValue(r.Context(), userKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFrom extracts the authenticated User from the request context.
// Returns false if invoked outside a handler protected by RequireAuth.
func UserFrom(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(userKey).(*User)
	return u, ok
}
