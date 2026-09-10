package document

// service.go implements document domain operations: Create, Get, List, Update, Delete,
// and MarkStatus. Interface abstractions (Repository, BlobStore, Cache, Indexer) are defined
// here by the consumer, insulating domain operations from concrete database and filesystem drivers.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/you/godocs/internal/id"
)

// Repository specifies persistence operations required by the document service.
type Repository interface {
	Create(ctx context.Context, d *Document) error
	GetByID(ctx context.Context, id string) (*Document, error)
	List(ctx context.Context, f ListFilter) ([]Document, int, error)
	Update(ctx context.Context, d *Document) error
	SetStatus(ctx context.Context, id string, st Status, updatedAt time.Time) error
	Delete(ctx context.Context, id string) error
}

// BlobStore encapsulates binary stream storage operations.
type BlobStore interface {
	Save(ctx context.Context, ext string, r io.Reader) (StoredBlob, error)
	Delete(ctx context.Context, path string) error
}

// StoredBlob represents the metadata returned upon successful disk persistence.
type StoredBlob struct {
	Path     string
	Size     int64
	Checksum string
}

// Cache defines the minimal key-value caching contract required by the service.
type Cache interface {
	Get(k string) (*Document, bool)
	Set(k string, v *Document)
	Delete(k string)
}

// Indexer dispatches asynchronous background processing tasks (text extraction, thumbnailing).
type Indexer interface {
	Enqueue(id string) bool
}

// ListFilter specifies query parameters for paginated document searches.
type ListFilter struct {
	Query  string
	Owner  string // Restricts results to documents created by this user ID; empty means no restriction.
	Status Status // Restricts results to this processing status; empty means any.
	Limit  int
	Offset int
}

// owns reports whether owner is allowed to access d.
// An empty owner (e.g. the background worker) bypasses the check.
func owns(d *Document, owner string) bool { return owner == "" || d.CreatedBy == owner }

// Service coordinates document domain logic, storage validation, and cache synchronization.
type Service struct {
	repo    Repository
	blob    BlobStore
	cache   Cache
	indexer Indexer
	log     *slog.Logger

	maxSize int64
}

// extByMime maps every accepted (sniffed) MIME type to the extension used for the
// stored file. It is the single source of truth for which uploads are allowed:
// a type absent from this map is rejected.
var extByMime = map[string]string{
	"application/pdf": ".pdf",
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"text/plain":      ".txt",
}

// NewService instantiates a document Service with default allowed MIME types and upload size limits.
func NewService(repo Repository, blob BlobStore, c Cache, idx Indexer, log *slog.Logger, maxSize int64) *Service {
	return &Service{
		repo: repo, blob: blob, cache: c, indexer: idx, log: log,
		maxSize: maxSize,
	}
}

// CreateInput encapsulates parameters for uploading and creating a new document.
type CreateInput struct {
	Title     string
	Summary   string
	FileName  string
	File      io.Reader
	CreatedBy string // Resolved user identity from active session
}

// Create validates metadata, sniffs binary MIME headers, stores the payload atomically,
// persists metadata to the database, and enqueues the document for background processing.
func (s *Service) Create(ctx context.Context, in CreateInput) (*Document, error) {
	errs := validateMeta(in.Title, in.Summary)
	if strings.TrimSpace(in.FileName) == "" || in.File == nil {
		errs = append(errs, FieldError{"file", "file payload is required"})
	}
	if len(errs) > 0 {
		return nil, errs
	}

	// Sniff the initial 512 bytes rather than trusting untrusted client Content-Type headers.
	head := make([]byte, 512)
	n, err := io.ReadFull(in.File, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read head: %w", err)
	}
	head = head[:n]
	mimeType := http.DetectContentType(head)
	if i := strings.IndexByte(mimeType, ';'); i >= 0 { // Strip parameters (e.g. "text/plain; charset=utf-8")
		mimeType = strings.TrimSpace(mimeType[:i])
	}
	ext, ok := extByMime[mimeType]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrMimeType, mimeType)
	}

	// Reconstruct stream by concatenating sniffed prefix with remaining body.
	// LimitReader(maxSize - n + 1) serves as a secondary defense after HTTP MaxBytesReader,
	// reading 1 byte beyond the ceiling to explicitly detect overflows rather than truncating silently.
	remain := s.maxSize - int64(n) + 1
	if remain < 1 {
		remain = 1
	}
	body := io.MultiReader(bytes.NewReader(head), io.LimitReader(in.File, remain))

	// Extension comes from the sniffed MIME type, never the client filename, so an
	// upload cannot place an arbitrary extension (".php", ".html", ...) on disk.
	saved, err := s.blob.Save(ctx, ext, body)
	if err != nil {
		return nil, fmt.Errorf("save blob: %w", err)
	}
	if saved.Size > s.maxSize {
		// Payload exceeds ceiling: purge written blob and emit ErrTooLarge
		if derr := s.blob.Delete(context.WithoutCancel(ctx), saved.Path); derr != nil {
			s.log.Error("orphan blob cleanup failed", "path", saved.Path, "err", derr)
		}
		return nil, fmt.Errorf("%w: %d bytes (maximum %d)", ErrTooLarge, saved.Size, s.maxSize)
	}

	now := time.Now().UTC()
	doc := &Document{
		ID:        id.New(),
		Title:     strings.TrimSpace(in.Title),
		Summary:   strings.TrimSpace(in.Summary),
		FileName:  filepath.Base(in.FileName),
		FilePath:  saved.Path,
		MimeType:  mimeType,
		SizeBytes: saved.Size,
		Checksum:  saved.Checksum,
		Status:    StatusPending,
		CreatedBy: in.CreatedBy,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.Create(ctx, doc); err != nil {
		// Compensating transaction: purge disk blob if database insert fails
		if derr := s.blob.Delete(context.WithoutCancel(ctx), saved.Path); derr != nil {
			s.log.Error("orphan blob cleanup failed", "path", saved.Path, "err", derr)
		}
		return nil, err
	}

	if !s.indexer.Enqueue(doc.ID) {
		s.log.Warn("index queue full", "id", doc.ID)
	}
	return doc, nil
}

// Get retrieves a document by ID utilizing a cache-aside pattern.
// A non-empty owner restricts access to that user's own documents; a mismatch
// is reported as ErrNotFound so callers cannot probe for others' document IDs.
func (s *Service) Get(ctx context.Context, id, owner string) (*Document, error) {
	d, ok := s.cache.Get(id)
	if !ok {
		var err error
		d, err = s.repo.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		s.cache.Set(id, d)
	}
	if !owns(d, owner) {
		return nil, fmt.Errorf("%w: id=%s", ErrNotFound, id)
	}
	return d, nil
}

// List queries a paginated slice of documents matching the specified filter.
func (s *Service) List(ctx context.Context, f ListFilter) ([]Document, int, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 20
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return s.repo.List(ctx, f)
}

// UpdateInput defines nullable fields for partial PATCH modifications.
type UpdateInput struct {
	Title   *string `json:"title"` // Pointer distinguishes omitted fields from explicit empty strings
	Summary *string `json:"summary"`
}

// Update applies partial modifications to a document entity and invalidates cache entries.
func (s *Service) Update(ctx context.Context, id, owner string, in UpdateInput) (*Document, error) {
	d, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !owns(d, owner) {
		return nil, fmt.Errorf("%w: id=%s", ErrNotFound, id)
	}
	if in.Title != nil {
		d.Title = strings.TrimSpace(*in.Title)
	}
	if in.Summary != nil {
		d.Summary = strings.TrimSpace(*in.Summary)
	}
	if errs := validateMeta(d.Title, d.Summary); len(errs) > 0 {
		return nil, errs
	}
	d.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, d); err != nil {
		return nil, err
	}
	s.cache.Delete(id) // Invalidate rather than set to prevent stale writes across distributed instances
	return d, nil
}

// Delete removes the document record and unlinks its associated binary disk blob.
func (s *Service) Delete(ctx context.Context, id, owner string) error {
	d, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if !owns(d, owner) {
		return fmt.Errorf("%w: id=%s", ErrNotFound, id)
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.cache.Delete(id)
	if err := s.blob.Delete(ctx, d.FilePath); err != nil {
		s.log.Error("delete blob failed", "id", id, "err", err) // Logged as warning; does not fail request
	}
	return nil
}

// MarkStatus updates the document processing status upon background worker completion.
// It writes the status column directly (no read-modify-write) so it cannot race a
// concurrent metadata PATCH.
func (s *Service) MarkStatus(ctx context.Context, id string, st Status) error {
	if err := s.repo.SetStatus(ctx, id, st, time.Now().UTC()); err != nil {
		return err
	}
	s.cache.Delete(id)
	return nil
}

// RequeuePending re-enqueues every document still in StatusPending. Call it on
// startup so uploads whose indexing was dropped (queue full, or a crash before
// processing) are not stranded. Best-effort: enqueue failures are only logged.
//
// It pages through the whole backlog rather than the first N rows — a burst that
// overflowed the queue can leave far more than one page of pending documents, and
// the oldest ones must be recovered too.
func (s *Service) RequeuePending(ctx context.Context) {
	const batch = 500
	total := 0
	for offset := 0; ; offset += batch {
		docs, _, err := s.repo.List(ctx, ListFilter{Status: StatusPending, Limit: batch, Offset: offset})
		if err != nil {
			s.log.Error("requeue pending: list failed", "err", err)
			return
		}
		for i := range docs {
			if !s.indexer.Enqueue(docs[i].ID) {
				s.log.Warn("requeue pending: queue full", "id", docs[i].ID)
			}
		}
		total += len(docs)
		if len(docs) < batch {
			break // last page
		}
	}
	if total > 0 {
		s.log.Info("requeued pending documents", "count", total)
	}
}
