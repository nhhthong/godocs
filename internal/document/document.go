// Package document encapsulates the core business domain, data models, and validation rules for documents.
//
// Structure:
//   - document.go: Domain entities (Document, Status), sentinel errors, and input validation.
//   - service.go : Core business lifecycle (Create/Get/List/Update/Delete) and consumer-defined interface contracts.
//   - handler.go : HTTP transport layer (routing, multipart streaming, and error mapping).
//
// This package constitutes the architectural core. Infrastructure adapters (db, storage, worker)
// depend upon the abstractions declared herein; package document does not import external adapters.
package document

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors representing domain-specific failures.
// Callers must compare using errors.Is rather than string matching.
var (
	ErrNotFound = errors.New("document: not found")
	ErrInvalid  = errors.New("document: invalid input")
	ErrMimeType = errors.New("document: unsupported mime type")
	ErrTooLarge = errors.New("document: file size exceeds allowed limit")
)

// Status represents the background processing state of a document.
type Status string

const (
	StatusPending Status = "pending"
	StatusReady   Status = "ready"
	StatusFailed  Status = "failed"
)

// Document represents the aggregate root entity for managed documents.
type Document struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Summary   string    `json:"summary"`
	FileName  string    `json:"file_name"`
	FilePath  string    `json:"-"` // Internal disk path; excluded from JSON serialization
	MimeType  string    `json:"mime_type"`
	SizeBytes int64     `json:"size_bytes"`
	Checksum  string    `json:"checksum"`
	Status    Status    `json:"status"`
	CreatedBy string    `json:"created_by"` // User identifier resolved from active session
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FieldError represents a field-level validation failure.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationErrors collects multiple FieldError instances.
type ValidationErrors []FieldError

func (v ValidationErrors) Error() string {
	parts := make([]string, 0, len(v))
	for _, e := range v {
		parts = append(parts, fmt.Sprintf("%s: %s", e.Field, e.Message))
	}
	return strings.Join(parts, "; ")
}

// Unwrap enables errors.Is(err, ErrInvalid) checks across ValidationErrors collections.
func (v ValidationErrors) Unwrap() error { return ErrInvalid }

func validateMeta(title, summary string) ValidationErrors {
	var errs ValidationErrors
	title = strings.TrimSpace(title)
	switch {
	case title == "":
		errs = append(errs, FieldError{"title", "title is required"})
	case len([]rune(title)) > 200:
		errs = append(errs, FieldError{"title", "title must not exceed 200 characters"})
	}
	if len([]rune(summary)) > 5000 {
		errs = append(errs, FieldError{"summary", "summary must not exceed 5000 characters"})
	}
	return errs
}
