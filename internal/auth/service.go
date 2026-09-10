package auth

// service.go orchestrates authentication business logic: registration, credential validation,
// session establishment, identity verification, and expired session garbage collection.
// The domain is wholly insulated from HTTP and SQL concerns, communicating strictly via Repo contracts.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/you/godocs/internal/id"
)

// UserRepo specifies the persistence contract required for user entities.
// Structural typing enables database implementations to satisfy this contract implicitly.
type UserRepo interface {
	CreateUser(ctx context.Context, u *User, passwordHash string) error
	// FindUserByEmail retrieves the user along with their password hash for verification.
	// Non-existent records yield ErrBadCredentials to prevent account enumeration.
	FindUserByEmail(ctx context.Context, email string) (u *User, passwordHash string, err error)
	FindUserByID(ctx context.Context, id string) (*User, error)
}

// SessionRepo specifies the persistence contract required for session lifecycle management.
type SessionRepo interface {
	CreateSession(ctx context.Context, s *Session) error
	FindSession(ctx context.Context, token string) (*Session, error) // Returns ErrNoSession if not found
	DeleteSession(ctx context.Context, token string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)
}

// Repo aggregates UserRepo and SessionRepo contracts for components supporting both interfaces.
type Repo interface {
	UserRepo
	SessionRepo
}

// Service coordinates user lifecycle operations and session validation rules.
type Service struct {
	repo Repo
	ttl  time.Duration // Session validity lifespan computed from authentication timestamp
}

// NewService instantiates a Service bound to the provided repository and session duration.
func NewService(repo Repo, ttl time.Duration) *Service {
	return &Service{repo: repo, ttl: ttl}
}

// Register provisions a new user account with normalized email and bcrypt-hashed credentials.
func (s *Service) Register(ctx context.Context, email, password string) (*User, error) {
	email = normalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("%w: invalid email address", ErrInvalidInput)
	}
	if len([]rune(password)) < 8 {
		return nil, ErrWeakPassword
	}

	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}

	u := &User{ID: id.New(), Email: email, CreatedAt: time.Now().UTC()}
	if err := s.repo.CreateUser(ctx, u, hash); err != nil {
		return nil, err // Translates SQL UNIQUE constraint violations into ErrEmailTaken
	}
	return u, nil
}

// Login validates user credentials and establishes a new stateful session.
func (s *Service) Login(ctx context.Context, email, password string) (*User, *Session, error) {
	email = normalizeEmail(email)

	u, hash, err := s.repo.FindUserByEmail(ctx, email)
	if errors.Is(err, ErrBadCredentials) {
		// Non-existent account: execute dummy bcrypt verification to neutralize timing side-channels.
		checkPassword(string(dummyHash), password)
		return nil, nil, ErrBadCredentials
	}
	if err != nil {
		// A genuine infrastructure failure must surface as 500, not as "bad credentials".
		return nil, nil, err
	}
	if !checkPassword(hash, password) {
		return nil, nil, ErrBadCredentials
	}

	now := time.Now().UTC()
	sess := &Session{
		Token:     id.Token(),
		UserID:    u.ID,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
	if err := s.repo.CreateSession(ctx, sess); err != nil {
		return nil, nil, err
	}
	return u, sess, nil
}

// Logout terminates a user session. Deletion is idempotent; non-existent tokens do not trigger errors.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.repo.DeleteSession(ctx, token)
}

// Authenticate resolves an active User from a provided session token.
// Middleware RequireAuth delegates to this method for protected endpoints.
func (s *Service) Authenticate(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	sess, err := s.repo.FindSession(ctx, token)
	if err != nil {
		return nil, err // ErrNoSession
	}
	if time.Now().After(sess.ExpiresAt) {
		// Opportunistically purge the expired session record; ignore deletion errors.
		_ = s.repo.DeleteSession(ctx, token)
		return nil, ErrNoSession
	}
	u, err := s.repo.FindUserByID(ctx, sess.UserID)
	if err != nil {
		return nil, ErrNoSession // Covers cases where a user was purged but session rows lingered
	}
	return u, nil
}

// PurgeExpired deletes all session rows whose expiration timestamp precedes the current moment.
func (s *Service) PurgeExpired(ctx context.Context) (int64, error) {
	return s.repo.DeleteExpiredSessions(ctx, time.Now().UTC())
}

func normalizeEmail(e string) string {
	return strings.ToLower(strings.TrimSpace(e))
}
