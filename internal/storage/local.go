// Package storage provides blob storage abstractions and filesystem implementations.
//
// The Local implementation persists files directly to the host filesystem.
// Migrating to cloud object stores (e.g., AWS S3, Google Cloud Storage) requires
// implementing an equivalent set of methods (Save, Open, Delete)—callers remain
// entirely decoupled as they rely on domain interfaces (document.BlobStore and
// document.FileOpener) rather than concrete implementations.
//
// Key architectural mechanisms in this package:
//   - Atomic writes: streams content into a temporary staging file followed by
//     an atomic os.Rename within the same filesystem boundary, eliminating partial/corrupt files.
//   - In-flight hashing: leverages io.MultiWriter to calculate the SHA-256 digest
//     concurrently with disk writes in a single pass without buffering payloads into memory.
//   - Path traversal mitigation: resolve() enforces strict lexical boundaries to prevent
//     directory escape attacks (e.g., "../../etc/passwd").
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/you/godocs/internal/id"
)

// SavedFile encapsulates metadata resulting from a successful file persistence operation.
type SavedFile struct {
	Path     string
	Size     int64
	Checksum string
}

// Local manages persistent blob storage on the host filesystem.
type Local struct {
	Root string
}

// NewLocal initializes a filesystem storage provider rooted at the designated directory,
// creating the root directory tree with restrictive permissions (0750) if necessary.
func NewLocal(root string) (*Local, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("storage: mkdir root: %w", err)
	}
	return &Local{Root: root}, nil
}

// Save persists an arbitrary byte stream to disk using an atomic write strategy:
// data is written to an ephemeral staging file and atomically renamed into its
// destination upon successful sync, precluding partially written files during system crashes.
func (l *Local) Save(ctx context.Context, ext string, r io.Reader) (SavedFile, error) {
	if err := ctx.Err(); err != nil {
		return SavedFile{}, err
	}
	// Partition files into date-based subdirectories (YYYY/MM/DD) to circumvent
	// filesystem performance degradation caused by directories with excessive file counts.
	dir := filepath.Join(l.Root, time.Now().UTC().Format("2006/01/02"))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return SavedFile{}, fmt.Errorf("storage: mkdir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "upload-*.part")
	if err != nil {
		return SavedFile{}, fmt.Errorf("storage: temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Clean up the staging file on early returns; this becomes a no-op once renamed.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	h := sha256.New()
	// io.MultiWriter simultaneously writes to disk and calculates the SHA-256 digest
	// in a single streaming pass, maintaining constant O(1) memory overhead.
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return SavedFile{}, fmt.Errorf("storage: copy: %w", err)
	}
	// Synchronize in-memory buffers to physical persistent media prior to renaming.
	if err := tmp.Sync(); err != nil {
		return SavedFile{}, fmt.Errorf("storage: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return SavedFile{}, fmt.Errorf("storage: close: %w", err)
	}

	final := filepath.Join(dir, id.New()+ext)
	if err := os.Rename(tmpName, final); err != nil {
		return SavedFile{}, fmt.Errorf("storage: rename: %w", err)
	}
	rel, err := filepath.Rel(l.Root, final)
	if err != nil {
		return SavedFile{}, err
	}
	return SavedFile{Path: rel, Size: size, Checksum: hex.EncodeToString(h.Sum(nil))}, nil
}

// Open resolves a relative storage path and returns an active *os.File.
// The returned handle satisfies io.ReadSeekCloser, allowing http.ServeContent
// to handle HTTP range requests (e.g., video scrubbing, resumable downloads).
func (l *Local) Open(ctx context.Context, rel string) (*os.File, error) {
	full, err := l.resolve(rel)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

// Delete removes a stored file. It behaves idempotently: attempting to delete
// a non-existent file succeeds without error.
func (l *Local) Delete(ctx context.Context, rel string) error {
	full, err := l.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("storage: remove: %w", err)
	}
	return nil
}

// resolve sanitizes and validates a relative path, enforcing strict boundaries
// to prevent path traversal vulnerabilities.
func (l *Local) resolve(rel string) (string, error) {
	clean := filepath.Clean(filepath.Join(l.Root, rel))
	root := filepath.Clean(l.Root)
	if clean != root && !hasPrefixDir(clean, root) {
		return "", fmt.Errorf("storage: path escapes root: %q", rel)
	}
	return clean, nil
}

func hasPrefixDir(path, root string) bool {
	if len(path) <= len(root) {
		return false
	}
	return path[:len(root)] == root && path[len(root)] == filepath.Separator
}
