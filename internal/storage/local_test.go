package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalSaveThenOpen(t *testing.T) {
	l, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	content := "test file content payload"
	saved, err := l.Save(ctx, ".txt", strings.NewReader(content))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Size != int64(len(content)) {
		t.Fatalf("size = %d, expected %d", saved.Size, len(content))
	}

	// Checksum must match the authentic SHA-256 digest of the payload.
	want := sha256.Sum256([]byte(content))
	if saved.Checksum != hex.EncodeToString(want[:]) {
		t.Fatalf("unexpected checksum: %s", saved.Checksum)
	}

	// Paths must remain strictly relative to the storage root to prevent leaking absolute server paths.
	if filepath.IsAbs(saved.Path) {
		t.Fatalf("path must be relative, got %q", saved.Path)
	}

	// Reading back the file via Open must yield the identical original payload.
	f, err := l.Open(ctx, saved.Path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	got, _ := os.ReadFile(filepath.Join(l.Root, saved.Path))
	if string(got) != content {
		t.Fatalf("read back = %q, expected %q", got, content)
	}
}

// Security invariant: resolve must reject any path traversal sequence that attempts to escape Root.
func TestLocalRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	l, _ := NewLocal(root)
	ctx := context.Background()

	// Provision a canary secret outside the storage root to assert containment.
	secret := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(secret) })

	// Note: Leading slash paths such as "/etc/passwd" are treated as relative by filepath.Join,
	// resolving safely to "<root>/etc/passwd". Actual traversal vectors utilize "../" parent traversals.
	bad := []string{
		"../secret.txt",
		"../../etc/passwd",
		"a/b/../../../secret.txt",
		"..",
	}
	for _, p := range bad {
		if _, err := l.Open(ctx, p); err == nil {
			t.Errorf("Open(%q): expected path traversal rejection, got nil error", p)
		}
		if err := l.Delete(ctx, p); err == nil {
			t.Errorf("Delete(%q): expected path traversal rejection, got nil error", p)
		}
	}
}

func TestLocalDeleteIdempotent(t *testing.T) {
	l, _ := NewLocal(t.TempDir())
	ctx := context.Background()

	saved, err := l.Save(ctx, ".bin", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Delete(ctx, saved.Path); err != nil {
		t.Fatalf("initial delete: %v", err)
	}
	// Deleting a non-existent file must complete idempotently without error.
	if err := l.Delete(ctx, saved.Path); err != nil {
		t.Fatalf("secondary idempotent delete: %v (expected nil)", err)
	}
}
