package db

// auth_repo.go implements AuthRepo, providing SQLite persistence for users and sessions.
// It encapsulates SQL interactions and translates database-specific conditions—such
// as UNIQUE constraint violations and sql.ErrNoRows—into domain sentinel errors.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/you/godocs/internal/auth"
)

// AuthRepo implements auth.UserRepo and auth.SessionRepo (collectively auth.Repo).
// In accordance with domain-driven boundaries, this concrete implementation resides
// in package db while the consumer interfaces remain in package auth.
type AuthRepo struct{ db *sql.DB }

// NewAuthRepo constructs an AuthRepo backed by the provided SQL connection pool.
func NewAuthRepo(db *sql.DB) *AuthRepo { return &AuthRepo{db: db} }

// --- Users ---

// CreateUser persists a new user record and their argon2id password hash.
// If the email address already exists, it maps the SQLite UNIQUE constraint violation
// to auth.ErrEmailTaken.
func (r *AuthRepo) CreateUser(ctx context.Context, u *auth.User, passwordHash string) error {
	const q = `INSERT INTO users (id, email, password_hash, created_at) VALUES (?,?,?,?)`
	_, err := r.db.ExecContext(ctx, q, u.ID, u.Email, passwordHash, u.CreatedAt.Unix())
	if err != nil {
		// SQLite does not expose a dedicated typed error for constraint violations;
		// inspect the error message string for standard SQLite violation phrasing.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return auth.ErrEmailTaken
		}
		return fmt.Errorf("repo: create user: %w", err)
	}
	return nil
}

// FindUserByEmail retrieves a user and their hashed credentials by email.
// When no record matches, it returns auth.ErrBadCredentials to prevent account enumeration.
func (r *AuthRepo) FindUserByEmail(ctx context.Context, email string) (*auth.User, string, error) {
	const q = `SELECT id, email, password_hash, created_at FROM users WHERE email = ?`
	var (
		u       auth.User
		hash    string
		created int64
	)
	err := r.db.QueryRowContext(ctx, q, email).Scan(&u.ID, &u.Email, &hash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", auth.ErrBadCredentials
	}
	if err != nil {
		return nil, "", fmt.Errorf("repo: find user by email: %w", err)
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	return &u, hash, nil
}

// FindUserByID retrieves a user record by primary key identifier.
// Returns auth.ErrNoSession when the user does not exist.
func (r *AuthRepo) FindUserByID(ctx context.Context, id string) (*auth.User, error) {
	const q = `SELECT id, email, created_at FROM users WHERE id = ?`
	var (
		u       auth.User
		created int64
	)
	err := r.db.QueryRowContext(ctx, q, id).Scan(&u.ID, &u.Email, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, auth.ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("repo: find user by id: %w", err)
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	return &u, nil
}

// --- Sessions ---

// CreateSession persists a new session token mapped to a specific user.
func (r *AuthRepo) CreateSession(ctx context.Context, s *auth.Session) error {
	const q = `INSERT INTO sessions (token, user_id, created_at, expires_at) VALUES (?,?,?,?)`
	_, err := r.db.ExecContext(ctx, q, s.Token, s.UserID, s.CreatedAt.Unix(), s.ExpiresAt.Unix())
	if err != nil {
		return fmt.Errorf("repo: create session: %w", err)
	}
	return nil
}

// FindSession retrieves a session record by its bearer token.
// Returns auth.ErrNoSession if the token does not exist in the database.
func (r *AuthRepo) FindSession(ctx context.Context, token string) (*auth.Session, error) {
	const q = `SELECT token, user_id, created_at, expires_at FROM sessions WHERE token = ?`
	var (
		s                auth.Session
		created, expires int64
	)
	err := r.db.QueryRowContext(ctx, q, token).Scan(&s.Token, &s.UserID, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, auth.ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("repo: find session: %w", err)
	}
	s.CreatedAt = time.Unix(created, 0).UTC()
	s.ExpiresAt = time.Unix(expires, 0).UTC()
	return &s, nil
}

// DeleteSession invalidates a session token upon explicit user sign-out.
func (r *AuthRepo) DeleteSession(ctx context.Context, token string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token); err != nil {
		return fmt.Errorf("repo: delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions purges all sessions whose expiration precedes cutoff.
// It returns the total count of removed session records.
func (r *AuthRepo) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("repo: purge sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
