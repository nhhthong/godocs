package db

// Integration tests in package db exercise repository logic against a real SQLite database
// (backed by a temporary file). This validates behaviors that in-memory stubs cannot replicate:
// column mapping, infrastructure-to-domain error translations, LIKE wildcard escaping,
// and foreign key/unique constraint enforcement.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/you/godocs/internal/auth"
	"github.com/you/godocs/internal/document"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	pool, err := Open(context.Background(), "file:"+dir+"/t.db?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(context.Background(), pool, "../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	return pool
}

func sampleDoc(id string) *document.Document {
	// Truncate to second precision because the database stores timestamps as Unix seconds.
	now := time.Now().UTC().Truncate(time.Second)
	return &document.Document{
		ID: id, Title: "Title " + id, Summary: "summary text", FileName: "f.txt",
		FilePath: id + "/f.txt", MimeType: "text/plain", SizeBytes: 3, Checksum: "abc",
		Status: document.StatusPending, CreatedBy: "user-1", CreatedAt: now, UpdatedAt: now,
	}
}

func TestDocumentRepoCRUD(t *testing.T) {
	repo := NewDocumentRepo(openTestDB(t))
	ctx := context.Background()

	d := sampleDoc("doc1")
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := repo.GetByID(ctx, "doc1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != d.Title || got.CreatedBy != "user-1" || !got.CreatedAt.Equal(d.CreatedAt) {
		t.Fatalf("unexpected document payload: %+v", got)
	}

	got.Title = "renamed title"
	got.UpdatedAt = got.UpdatedAt.Add(time.Minute)
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Status is written on its own path and must be untouched by a metadata Update.
	if err := repo.SetStatus(ctx, "doc1", document.StatusReady, got.UpdatedAt); err != nil {
		t.Fatalf("set status: %v", err)
	}
	after, _ := repo.GetByID(ctx, "doc1")
	if after.Title != "renamed title" || after.Status != document.StatusReady {
		t.Fatalf("update was not persisted: %+v", after)
	}

	if err := repo.SetStatus(ctx, "missing", document.StatusReady, got.UpdatedAt); !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("set status on missing doc: expected ErrNotFound, got %v", err)
	}

	if err := repo.Delete(ctx, "doc1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.GetByID(ctx, "doc1"); !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("get after delete: expected ErrNotFound, got %v", err)
	}
	if err := repo.Update(ctx, d); !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("update deleted document: expected ErrNotFound, got %v", err)
	}
	if err := repo.Delete(ctx, "doc1"); !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("repeat delete: expected ErrNotFound, got %v", err)
	}
}

func TestDocumentRepoListEscapesLikeWildcards(t *testing.T) {
	repo := NewDocumentRepo(openTestDB(t))
	ctx := context.Background()

	mk := func(id, title string) {
		d := sampleDoc(id)
		d.Title = title
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	mk("a", "discount 50% promo today")
	mk("b", "september quarterly newsletter")
	mk("c", "internal_notes draft")

	// '%' must be interpreted verbatim. Without sanitization, "50%" would match all records.
	if _, total, err := repo.List(ctx, document.ListFilter{Query: "50%", Limit: 10}); err != nil {
		t.Fatal(err)
	} else if total != 1 {
		t.Fatalf(`q="50%%": total = %d, expected 1`, total)
	}

	// '_' must be matched literally (matching only "internal_notes draft").
	if _, total, err := repo.List(ctx, document.ListFilter{Query: "internal_notes", Limit: 10}); err != nil {
		t.Fatal(err)
	} else if total != 1 {
		t.Fatalf(`q="internal_notes": total = %d, expected 1`, total)
	}

	// Without query filters, returns all records with pagination metadata.
	items, total, err := repo.List(ctx, document.ListFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("total = %d, expected 3 (total count must remain independent of LIMIT)", total)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, expected 2 (constrained by LIMIT)", len(items))
	}
}

// TestDocumentRepoListPaginationStable checks that paging through documents that
// all share one created_at value returns every row exactly once (the id tie-breaker).
func TestDocumentRepoListPaginationStable(t *testing.T) {
	repo := NewDocumentRepo(openTestDB(t))
	ctx := context.Background()

	const n = 25
	ts := time.Unix(1_700_000_000, 0).UTC() // identical for every row
	for i := 0; i < n; i++ {
		d := sampleDoc(fmt.Sprintf("doc-%02d", i))
		d.CreatedAt, d.UpdatedAt = ts, ts
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]int{}
	const page = 10
	for offset := 0; offset < n; offset += page {
		items, total, err := repo.List(ctx, document.ListFilter{Limit: page, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if total != n {
			t.Fatalf("total = %d, want %d", total, n)
		}
		for _, it := range items {
			seen[it.ID]++
		}
	}

	if len(seen) != n {
		t.Fatalf("saw %d distinct docs across all pages, want %d", len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("doc %s appeared %d times across pages (want 1)", id, c)
		}
	}
}

func TestMigrateRecordsAndSkips(t *testing.T) {
	pool := openTestDB(t) // already migrated once
	ctx := context.Background()

	var applied int
	if err := pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != 2 {
		t.Fatalf("schema_migrations rows = %d, want 2", applied)
	}

	// Re-running must be a no-op (no error, no duplicate rows).
	if err := Migrate(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Fatalf("after re-run schema_migrations rows = %d, want 2", applied)
	}
}

func TestDocumentRepoListFiltersByOwner(t *testing.T) {
	repo := NewDocumentRepo(openTestDB(t))
	ctx := context.Background()

	mk := func(id, owner string) {
		d := sampleDoc(id)
		d.CreatedBy = owner
		if err := repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	mk("a1", "alice")
	mk("a2", "alice")
	mk("b1", "bob")

	items, total, err := repo.List(ctx, document.ListFilter{Owner: "alice", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("owner=alice: total=%d items=%d, expected 2/2", total, len(items))
	}
	for _, it := range items {
		if it.CreatedBy != "alice" {
			t.Fatalf("leaked document owned by %q", it.CreatedBy)
		}
	}
}

func TestAuthRepoUsers(t *testing.T) {
	repo := NewAuthRepo(openTestDB(t))
	ctx := context.Background()

	u := &auth.User{ID: "u1", Email: "a@ex.com", CreatedAt: time.Now().UTC().Truncate(time.Second)}
	if err := repo.CreateUser(ctx, u, "hash-1"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Conflicting email: SQLite UNIQUE violation must translate into auth.ErrEmailTaken.
	dup := &auth.User{ID: "u2", Email: "a@ex.com", CreatedAt: u.CreatedAt}
	if err := repo.CreateUser(ctx, dup, "hash-2"); !errors.Is(err, auth.ErrEmailTaken) {
		t.Fatalf("duplicate email: expected ErrEmailTaken, got %v", err)
	}

	got, hash, err := repo.FindUserByEmail(ctx, "a@ex.com")
	if err != nil || got.ID != "u1" || hash != "hash-1" {
		t.Fatalf("find by email: user=%+v hash=%q err=%v", got, hash, err)
	}

	if _, _, err := repo.FindUserByEmail(ctx, "absent@ex.com"); !errors.Is(err, auth.ErrBadCredentials) {
		t.Fatalf("non-existent email: expected ErrBadCredentials, got %v", err)
	}
	if _, err := repo.FindUserByID(ctx, "absent-id"); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("non-existent id: expected ErrNoSession, got %v", err)
	}
}

func TestAuthRepoSessions(t *testing.T) {
	repo := NewAuthRepo(openTestDB(t))
	ctx := context.Background()

	if err := repo.CreateUser(ctx, &auth.User{ID: "u1", Email: "a@ex.com", CreatedAt: time.Now().UTC()}, "h"); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	live := &auth.Session{Token: "tok-live", UserID: "u1", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	dead := &auth.Session{Token: "tok-dead", UserID: "u1", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	if err := repo.CreateSession(ctx, live); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSession(ctx, dead); err != nil {
		t.Fatal(err)
	}

	got, err := repo.FindSession(ctx, "tok-live")
	if err != nil || got.UserID != "u1" || !got.ExpiresAt.Equal(live.ExpiresAt) {
		t.Fatalf("find session: %+v err=%v", got, err)
	}

	// PurgeExpired must only remove expired sessions.
	n, err := repo.DeleteExpiredSessions(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("purge: n=%d err=%v (expected n=1)", n, err)
	}
	if _, err := repo.FindSession(ctx, "tok-dead"); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("expired session after purge: expected ErrNoSession, got %v", err)
	}
	if _, err := repo.FindSession(ctx, "tok-live"); err != nil {
		t.Fatalf("active session unexpectedly purged: %v", err)
	}

	// DeleteSession (explicit logout) must render the session unresolvable.
	if err := repo.DeleteSession(ctx, "tok-live"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.FindSession(ctx, "tok-live"); !errors.Is(err, auth.ErrNoSession) {
		t.Fatalf("after DeleteSession: expected ErrNoSession, got %v", err)
	}
}
