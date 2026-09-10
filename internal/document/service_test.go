package document

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
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

func (f *fakeRepo) List(context.Context, ListFilter) ([]Document, int, error) { return nil, 0, nil }

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

func (f *fakeRepo) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, id)
	return nil
}

type fakeBlob struct{ deleted []string }

func (f *fakeBlob) Save(_ context.Context, _ string, r io.Reader) (StoredBlob, error) {
	n, _ := io.Copy(io.Discard, r)
	return StoredBlob{Path: "x/y.bin", Size: n, Checksum: "deadbeef"}, nil
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

func TestServiceGetNotFound(t *testing.T) {
	svc, _, _ := newTestService()
	_, err := svc.Get(context.Background(), "non-existent-id")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, expected ErrNotFound", err)
	}
}
