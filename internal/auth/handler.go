package auth

// handler.go implements the HTTP transport layer for authentication: request payload parsing,
// service invocation, cookie manipulation, and domain-to-HTTP error mapping.

import (
	"errors"
	"net/http"
	"time"

	"github.com/you/godocs/internal/httpx"
)

// Handler serves HTTP endpoints governing authentication lifecycle.
type Handler struct {
	svc          *Service
	cookieSecure bool          // When true, cookie transmission is restricted to HTTPS connections
	ttl          time.Duration // Lifespan synchronized with cookie expiration
}

// NewHandler initializes an authentication Handler.
func NewHandler(svc *Service, cookieSecure bool, ttl time.Duration) *Handler {
	return &Handler{svc: svc, cookieSecure: cookieSecure, ttl: ttl}
}

// Routes constructs the HTTP router utilizing Go 1.22+ method-matching patterns.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/register", h.register)
	mux.HandleFunc("POST /api/auth/login", h.login)
	mux.HandleFunc("POST /api/auth/logout", h.logout)
	// /me reuses the same session gate as the document routes instead of re-checking the cookie by hand.
	mux.Handle("GET /api/auth/me", h.svc.RequireAuth(http.HandlerFunc(h.me)))
	return mux
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}
	u, err := h.svc.Register(r.Context(), in.Email, in.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, u)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}
	u, sess, err := h.svc.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	h.setSessionCookie(w, sess.Token)
	httpx.JSON(w, http.StatusOK, u)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = h.svc.Logout(r.Context(), c.Value)
	}
	// MaxAge < 0 triggers immediate cookie deletion in the client browser
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// me returns the profile of the authenticated caller.
// RequireAuth has already validated the session and put the user in the context.
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFrom(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "authentication required", nil)
		return
	}
	httpx.JSON(w, http.StatusOK, u)
}

// setSessionCookie configures security flags on the session cookie:
//   - HttpOnly: Prevents client-side scripts from reading the token (XSS mitigation).
//   - Secure: Restricts cookie transmission to TLS/HTTPS connections.
//   - SameSite=Lax: Mitigates cross-site request forgery (CSRF) on ambient navigation.
func (h *Handler) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(h.ttl),
		MaxAge:   int(h.ttl.Seconds()),
	})
}

// writeErr maps domain error values to standard HTTP status codes.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrBadCredentials):
		// Emits identical error descriptions for non-existent emails and incorrect passwords
		httpx.Error(w, http.StatusUnauthorized, "bad_credentials", "invalid email or password", nil)
	case errors.Is(err, ErrEmailTaken):
		httpx.Error(w, http.StatusConflict, "email_taken", "email is already registered", nil)
	case errors.Is(err, ErrWeakPassword):
		httpx.Error(w, http.StatusUnprocessableEntity, "weak_password", err.Error(), nil)
	case errors.Is(err, ErrInvalidInput):
		httpx.Error(w, http.StatusBadRequest, "invalid_input", err.Error(), nil)
	case errors.Is(err, ErrNoSession):
		httpx.Error(w, http.StatusUnauthorized, "unauthorized", "invalid or expired session", nil)
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal_error", "an internal error occurred", nil)
	}
}
