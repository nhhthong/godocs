-- 002_auth.sql — users and stateful session storage.
--
-- Architecture:
-- Implements stateful server-side sessions: authenticated sessions are persisted
-- in SQLite, while clients retain an opaque cryptographic token via secure cookies
-- or authorization headers. Sign-in inserts a session record; sign-out deletes it.
-- This architecture provides immediate server-side revocation capability.

CREATE TABLE IF NOT EXISTS users (
    id            TEXT    PRIMARY KEY,
    email         TEXT    NOT NULL UNIQUE,   -- Enforces email uniqueness at the database layer
    password_hash TEXT    NOT NULL,          -- Argon2id / bcrypt hash; plaintext passwords are never stored
    created_at    INTEGER NOT NULL           -- Unix epoch seconds, consistent with the documents schema
);

CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT    PRIMARY KEY,          -- High-entropy 256-bit hexadecimal string transmitted by clients
    user_id    TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL              -- Expiration timestamp (seconds since Unix epoch)
);

-- Index to accelerate periodic cleanup of expired sessions.
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
