package db

// repo.go implements DocumentRepo for the documents table. It translates infrastructure errors
// (e.g. sql.ErrNoRows) into domain sentinel errors (e.g. document.ErrNotFound) while mapping domain entities.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/you/godocs/internal/document"
)

// DocumentRepo implements document.Repository using SQLite.
// In Go, structural typing implicitly satisfies interface contracts without explicit keywords.
type DocumentRepo struct{ db *sql.DB }

// NewDocumentRepo initializes a DocumentRepo bound to the shared database pool.
func NewDocumentRepo(db *sql.DB) *DocumentRepo { return &DocumentRepo{db: db} }

// cols specifies column projection ordering synchronized with scanInto().
const cols = `id, title, summary, file_name, file_path, mime_type, size_bytes, checksum, status, created_at, updated_at, created_by`

func (r *DocumentRepo) Create(ctx context.Context, d *document.Document) error {
	const q = `INSERT INTO documents (` + cols + `) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	_, err := r.db.ExecContext(ctx, q,
		d.ID, d.Title, d.Summary, d.FileName, d.FilePath, d.MimeType,
		d.SizeBytes, d.Checksum, string(d.Status),
		d.CreatedAt.Unix(), d.UpdatedAt.Unix(), d.CreatedBy,
	)
	if err != nil {
		return fmt.Errorf("repo: create: %w", err)
	}
	return nil
}

func (r *DocumentRepo) GetByID(ctx context.Context, id string) (*document.Document, error) {
	const q = `SELECT ` + cols + ` FROM documents WHERE id = ?`
	d, err := scanOne(r.db.QueryRowContext(ctx, q, id))
	if errors.Is(err, sql.ErrNoRows) {
		// Translate infrastructure error to domain sentinel error
		return nil, fmt.Errorf("%w: id=%s", document.ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("repo: get: %w", err)
	}
	return d, nil
}

func (r *DocumentRepo) List(ctx context.Context, f document.ListFilter) ([]document.Document, int, error) {
	where, args := "", []any{}
	if q := strings.TrimSpace(f.Query); q != "" {
		// ESCAPE '\' treats '%' and '_' wildcards as literal characters to prevent query injections
		where = ` WHERE (title LIKE ? ESCAPE '\' OR summary LIKE ? ESCAPE '\')`
		like := "%" + escapeLike(q) + "%"
		args = append(args, like, like)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("repo: count: %w", err)
	}

	q := `SELECT ` + cols + ` FROM documents` + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, q, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("repo: list: %w", err)
	}
	defer rows.Close() // Mandatory: unclosed rows cause connection pool leaks

	out := make([]document.Document, 0, f.Limit)
	for rows.Next() {
		var d document.Document
		if err := scanInto(rows, &d); err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	// Inspection of rows.Err() is mandatory to catch iteration stream errors
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("repo: rows: %w", err)
	}
	return out, total, nil
}

func (r *DocumentRepo) Update(ctx context.Context, d *document.Document) error {
	const q = `UPDATE documents SET title=?, summary=?, status=?, updated_at=? WHERE id=?`
	res, err := r.db.ExecContext(ctx, q, d.Title, d.Summary, string(d.Status), d.UpdatedAt.Unix(), d.ID)
	if err != nil {
		return fmt.Errorf("repo: update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: id=%s", document.ErrNotFound, d.ID)
	}
	return nil
}

func (r *DocumentRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM documents WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("repo: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: id=%s", document.ErrNotFound, id)
	}
	return nil
}

// escapeLike sanitizes SQL LIKE wildcards ('\', '%', '_') ensuring input terms are evaluated verbatim.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

type scanner interface{ Scan(dest ...any) error }

func scanInto(s scanner, d *document.Document) error {
	var status string
	var created, updated int64
	err := s.Scan(&d.ID, &d.Title, &d.Summary, &d.FileName, &d.FilePath, &d.MimeType,
		&d.SizeBytes, &d.Checksum, &status, &created, &updated, &d.CreatedBy)
	if err != nil {
		return err
	}
	d.Status = document.Status(status)
	d.CreatedAt = time.Unix(created, 0).UTC()
	d.UpdatedAt = time.Unix(updated, 0).UTC()
	return nil
}

func scanOne(row *sql.Row) (*document.Document, error) {
	var d document.Document
	if err := scanInto(row, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
