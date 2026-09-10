package document

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- In-Memory Fakes: Satisfy consumer contracts without heavyweight mocking libraries ---

type fakeRepo struct {
	mu   sync.Mutex
	data map[string]*Document
}

func newFakeRepo() *fakeRepo { return &fakeRepo{data: map[string]*Document{}} }

func (f *fakeRepo) Create(_ context.Context, d *Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *d
	f.data[d.ID] = &cp
	return nil
}

func (f *fakeRepo) GetByID(_ context.Context, id string) (*Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.data[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *d
	return &cp, nil
}

func (f *fakeRepo) List(_ context.Context, flt ListFilter) ([]Document, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Document
	for _, d := range f.data {
		if flt.Owner != "" && d.CreatedBy != flt.Owner {
			continue
		}
		if flt.Status != "" && d.Status != flt.Status {
			continue
		}
		out = append(out, *d)
	}
	return out, len(out), nil
}

func (f *fakeRepo) Update(_ context.Context, d *Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.data[d.ID]; !ok {
		return ErrNotFound
	}
	cp := *d
	f.data[d.ID] = &cp
	return nil
}

func (f *fakeRepo) SetStatus(_ context.Context, id string, st Status, updatedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.data[id]
	if !ok {
		return ErrNotFound
	}
	d.Status = st
	d.UpdatedAt = updatedAt
	return nil
}

func (f *fakeRepo) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, id)
	return nil
}

type fakeBlob struct {
	deleted  []string
	savedExt string
}

func (f *fakeBlob) Save(_ context.Context, ext string, r io.Reader) (StoredBlob, error) {
	f.savedExt = ext
	n, _ := io.Copy(io.Discard, r)
	return StoredBlob{Path: "x/y" + ext, Size: n, Checksum: "deadbeef"}, nil
}
func (f *fakeBlob) Delete(_ context.Context, p string) error {
	f.deleted = append(f.deleted, p)
	return nil
}

type noopCache struct{}

func (noopCache) Get(string) (*Document, bool) { return nil, false }
func (noopCache) Set(string, *Document)        {}
func (noopCache) Delete(string)                {}

type fakeIndexer struct{ ids []string }

func (f *fakeIndexer) Enqueue(id string) bool { f.ids = append(f.ids, id); return true }

func newTestService() (*Service, *fakeRepo, *fakeBlob) {
	repo, blob := newFakeRepo(), &fakeBlob{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(repo, blob, noopCache{}, &fakeIndexer{}, log, 1<<20), repo, blob
}

// --- Table-Driven Tests: Idiomatic Go testing pattern ---

func TestServiceCreate(t *testing.T) {
	tests := []struct {
		name    string
		in      CreateInput
		wantErr error
	}{
		{
			name: "valid input",
			in:   CreateInput{Title: "Contract 2026", Summary: "brief summary", FileName: "a.txt", File: strings.NewReader("hello world")},
		},
		{
			name:    "missing title",
			in:      CreateInput{Title: "  ", FileName: "a.txt", File: strings.NewReader("hi")},
			wantErr: ErrInvalid,
		},
		{
			name:    "missing file payload",
			in:      CreateInput{Title: "ok"},
			wantErr: ErrInvalid,
		},
		{
			name:    "unsupported MIME type",
			in:      CreateInput{Title: "ok", FileName: "a.bin", File: strings.NewReader("\x00\x01\x02\x03binary")},
			wantErr: ErrMimeType,
		},
	}

	for _, tc := range tests {
		tc := tc // Rebind loop variable for parallel execution safety
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := newTestService()
			doc, err := svc.Create(context.Background(), tc.in)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, expected %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if doc.Status != StatusPending {
				t.Errorf("status = %q, expected %q", doc.Status, StatusPending)
			}
			if doc.SizeBytes != 11 {
				t.Errorf("size = %d, expected 11", doc.SizeBytes)
			}
		})
	}
}

// TestServiceCreateExtensionFromMime verifies the stored extension is derived from the
// sniffed MIME type, ignoring a hostile client filename.
func TestServiceCreateExtensionFromMime(t *testing.T) {
	svc, _, blob := newTestService()
	pdf := "%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF"
	if _, err := svc.Create(context.Background(), CreateInput{
		Title: "invoice", FileName: "evil.php", File: strings.NewReader(pdf),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if blob.savedExt != ".pdf" {
		t.Fatalf("stored extension = %q, want .pdf (client filename must not leak through)", blob.savedExt)
	}
}

func TestServiceGetNotFound(t *testing.T) {
	svc, _, _ := newTestService()
	_, err := svc.Get(context.Background(), "non-existent-id", "")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, expected ErrNotFound", err)
	}
}

// TestServiceOwnership verifies a user cannot read, update, or delete another user's document.
func TestServiceOwnership(t *testing.T) {
	svc, repo, _ := newTestService()
	ctx := context.Background()
	repo.data["d1"] = &Document{ID: "d1", Title: "owned", CreatedBy: "alice"}

	if _, err := svc.Get(ctx, "d1", "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get as bob: err = %v, expected ErrNotFound", err)
	}
	if _, err := svc.Update(ctx, "d1", "bob", UpdateInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update as bob: err = %v, expected ErrNotFound", err)
	}
	if err := svc.Delete(ctx, "d1", "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete as bob: err = %v, expected ErrNotFound", err)
	}
	if _, err := svc.Get(ctx, "d1", "alice"); err != nil {
		t.Fatalf("Get as alice: unexpected err %v", err)
	}
}

// TestMarkStatusDoesNotClobberMetadata verifies MarkStatus touches only the status
// column, so it cannot overwrite a concurrent title/summary edit.
func TestMarkStatusDoesNotClobberMetadata(t *testing.T) {
	svc, repo, _ := newTestService()
	ctx := context.Background()
	repo.data["d1"] = &Document{ID: "d1", Title: "original", Summary: "keep me", CreatedBy: "alice", Status: StatusPending}

	if err := svc.MarkStatus(ctx, "d1", StatusReady); err != nil {
		t.Fatalf("MarkStatus: %v", err)
	}
	got := repo.data["d1"]
	if got.Status != StatusReady {
		t.Fatalf("status = %q, want ready", got.Status)
	}
	if got.Title != "original" || got.Summary != "keep me" {
		t.Fatalf("MarkStatus clobbered metadata: %+v", got)
	}

	if err := svc.MarkStatus(ctx, "missing", StatusReady); !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkStatus on missing doc: err = %v, want ErrNotFound", err)
	}
}

// TestRequeuePending enqueues exactly the documents still in StatusPending.
func TestRequeuePending(t *testing.T) {
	svc, repo, _ := newTestService()
	idx := svc.indexer.(*fakeIndexer)
	repo.data["p1"] = &Document{ID: "p1", Status: StatusPending}
	repo.data["p2"] = &Document{ID: "p2", Status: StatusPending}
	repo.data["r1"] = &Document{ID: "r1", Status: StatusReady}

	svc.RequeuePending(context.Background())

	if len(idx.ids) != 2 {
		t.Fatalf("enqueued %v, want the 2 pending IDs", idx.ids)
	}
	for _, id := range idx.ids {
		if id != "p1" && id != "p2" {
			t.Fatalf("unexpected enqueued id %q", id)
		}
	}
}
