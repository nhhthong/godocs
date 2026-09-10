// Package auth implements email and password authentication, password hashing via bcrypt,
// and stateful database-backed session management.
//
// In idiomatic Go fashion, persistence interfaces (UserRepo, SessionRepo) are declared directly
// within this consuming package rather than downstream in the database package. This decouples
// authentication business rules from specific SQL implementations and allows test suites to leverage
// clean in-memory fakes without third-party mocking frameworks.
package auth

import (
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Sentinel errors representing domain-specific authentication failures.
// Callers must inspect errors using errors.Is rather than string matching.
var (
	ErrInvalidInput   = errors.New("auth: invalid input")
	ErrWeakPassword   = errors.New("auth: password must be 8 to 72 bytes")
	ErrEmailTaken     = errors.New("auth: email already registered")
	ErrBadCredentials = errors.New("auth: invalid email or password")
	ErrNoSession      = errors.New("auth: unauthorized or session expired")
)

// User represents public user identity attributes.
// The password hash is strictly unexported and confined to the database layer.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// Session represents an authenticated user session indexed by a cryptographic token.
type Session struct {
	Token     string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// --- Cryptographic Password Operations ---

// hashPassword computes a bcrypt hash of the plaintext password using DefaultCost (10).
// Salt generation is handled internally by bcrypt and encoded directly into the resulting string.
func hashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

// checkPassword compares a bcrypt hash against a candidate plaintext password in constant time.
func checkPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// dummyHash precomputes a fixed bcrypt hash to protect against timing-based user enumeration attacks.
// When an unrecognised email is submitted during authentication, checkPassword executes against dummyHash,
// equalizing computational latency between non-existent accounts and invalid password submissions.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("timing-attack-shield"), bcrypt.DefaultCost)
