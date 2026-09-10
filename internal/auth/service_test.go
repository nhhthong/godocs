package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- In-Memory Repository Fake: validates service logic without requiring a physical database ---

type memRepo struct {
	users    map[string]*User  // id -> user
	byEmail  map[string]string // email -> id
	hashes   map[string]string // id -> password_hash
	sessions map[string]*Session
}

func newMemRepo() *memRepo {
	return &memRepo{
		users:    map[string]*User{},
		byEmail:  map[string]string{},
		hashes:   map[string]string{},
		sessions: map[string]*Session{},
	}
}

func (m *memRepo) CreateUser(_ context.Context, u *User, hash string) error {
	if _, ok := m.byEmail[u.Email]; ok {
		return ErrEmailTaken
	}
	cp := *u
	m.users[u.ID] = &cp
	m.byEmail[u.Email] = u.ID
	m.hashes[u.ID] = hash
	return nil
}

func (m *memRepo) FindUserByEmail(_ context.Context, email string) (*User, string, error) {
	id, ok := m.byEmail[email]
	if !ok {
		return nil, "", ErrBadCredentials
	}
	cp := *m.users[id]
	return &cp, m.hashes[id], nil
}

func (m *memRepo) FindUserByID(_ context.Context, id string) (*User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, ErrNoSession
	}
	cp := *u
	return &cp, nil
}

func (m *memRepo) CreateSession(_ context.Context, s *Session) error {
	cp := *s
	m.sessions[s.Token] = &cp
	return nil
}

func (m *memRepo) FindSession(_ context.Context, token string) (*Session, error) {
	s, ok := m.sessions[token]
	if !ok {
		return nil, ErrNoSession
	}
	cp := *s
	return &cp, nil
}

func (m *memRepo) DeleteSession(_ context.Context, token string) error {
	delete(m.sessions, token)
	return nil
}

func (m *memRepo) DeleteExpiredSessions(_ context.Context, now time.Time) (int64, error) {
	var n int64
	for tok, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			delete(m.sessions, tok)
			n++
		}
	}
	return n, nil
}

// --- Test Cases ---

func TestAuthRoundTrip(t *testing.T) {
	svc := NewService(newMemRepo(), time.Hour)
	ctx := context.Background()

	if _, err := svc.Register(ctx, "A@Example.com ", "supersecret"); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Re-registering with identical email (normalized to lowercase) must return ErrEmailTaken.
	if _, err := svc.Register(ctx, "a@example.com", "anotherpass"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("duplicate registration: expected ErrEmailTaken, got %v", err)
	}

	// Short password must return ErrWeakPassword.
	if _, err := svc.Register(ctx, "b@example.com", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password: expected ErrWeakPassword, got %v", err)
	}

	// Valid login yields session; Authenticate resolves user identity correctly.
	_, sess, err := svc.Login(ctx, "a@example.com", "supersecret")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	u, err := svc.Authenticate(ctx, sess.Token)
	if err != nil || u.Email != "a@example.com" {
		t.Fatalf("authenticate failed: err=%v user=%+v", err, u)
	}

	// Invalid password yields ErrBadCredentials.
	if _, _, err := svc.Login(ctx, "a@example.com", "wrong-pass"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("invalid password: expected ErrBadCredentials, got %v", err)
	}

	// Non-existent email also yields ErrBadCredentials (side-channel prevention).
	if _, _, err := svc.Login(ctx, "nobody@example.com", "whatever0"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("unregistered email: expected ErrBadCredentials, got %v", err)
	}

	// Post-logout: token must be invalidated.
	if err := svc.Logout(ctx, sess.Token); err != nil {
		t.Fatalf("logout failed: %v", err)
	}
	if _, err := svc.Authenticate(ctx, sess.Token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("post-logout: expected ErrNoSession, got %v", err)
	}
}

func TestAuthExpiredSession(t *testing.T) {
	// Negative TTL generates an immediately expired session.
	svc := NewService(newMemRepo(), -time.Minute)
	ctx := context.Background()

	if _, err := svc.Register(ctx, "c@example.com", "supersecret"); err != nil {
		t.Fatal(err)
	}
	_, sess, err := svc.Login(ctx, "c@example.com", "supersecret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, sess.Token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired session: expected ErrNoSession, got %v", err)
	}
}
