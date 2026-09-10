CREATE TABLE IF NOT EXISTS documents (
    id          TEXT PRIMARY KEY,
    title       TEXT    NOT NULL,
    summary     TEXT    NOT NULL DEFAULT '',
    file_name   TEXT    NOT NULL,
    file_path   TEXT    NOT NULL,
    mime_type   TEXT    NOT NULL,
    size_bytes  INTEGER NOT NULL,
    checksum    TEXT    NOT NULL,
    status      TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    created_by  TEXT    NOT NULL DEFAULT ''   -- Identifier of the user who authored the document (see 002_auth.sql)
);
CREATE INDEX IF NOT EXISTS idx_documents_created_at ON documents(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_documents_checksum   ON documents(checksum);
CREATE INDEX IF NOT EXISTS idx_documents_created_by ON documents(created_by);
