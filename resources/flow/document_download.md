# Flow: Binary File Download & HTTP Range Streaming (`GET /api/documents/{id}/file`)

This document describes how binary files are securely retrieved, streamed, and partially served via HTTP Range requests in **`godocs`**.

---

## 1. Overview & Endpoint Contract

- **HTTP Method & Path**: `GET /api/documents/{id}/file`
- **Authentication Required**: Yes (`session_id` cookie via `auth.RequireAuth`)
- **Headers Supported**:
  - `Range: bytes=start-end` (for streaming media and resuming downloads)
  - `If-Modified-Since` (for HTTP cache validation)
- **Response Headers**:
  - `Content-Type`: MIME type of the document (e.g. `application/pdf`)
  - `Content-Disposition`: `attachment; filename*=UTF-8''<encoded-filename>` (RFC 5987)
  - `X-Content-Type-Options`: `nosniff`
  - `Accept-Ranges`: `bytes`

---

## 2. Sequence Diagram

```mermaid
sequenceDiagram
    autonumber
    actor Client as Client / Video Player / PDF Viewer
    participant Auth as auth.RequireAuth
    participant Handler as document.Handler.download
    participant Svc as document.Service
    participant Storage as storage.Local
    participant OS as File Descriptor

    Client->>Auth: GET /api/documents/{id}/file (Range: bytes=0-1048575)
    Auth->>Auth: Validate session cookie
    Auth->>Handler: Forward request

    Handler->>Svc: Get(ctx, id)
    Svc-->>Handler: Document Metadata (FilePath, MimeType, FileName, UpdatedAt)

    Handler->>Storage: Open(doc.FilePath)

    Note over Storage: Path Traversal Protection
    Storage->>Storage: resolve(path) ensures canonical path is inside Storage.Root
    alt Traversal Attempt (e.g. ../../etc/passwd)
        Storage-->>Handler: ErrInvalidPath
        Handler-->>Client: 400 Bad Request
    end

    Storage->>OS: os.Open(canonicalPath)
    OS-->>Storage: *os.File (implements io.ReadSeekCloser)
    Storage-->>Handler: io.ReadSeekCloser

    Note over Handler,Client: http.ServeContent Protocol Handling
    Handler->>Handler: Set Content-Type, Content-Disposition, X-Content-Type-Options: nosniff
    Handler->>Client: http.ServeContent(w, r, doc.FileName, doc.UpdatedAt, file)

    alt Range Request (bytes=0-1048575)
        OS->>OS: Seek(0, io.SeekStart)
        Client<<--Handler: 206 Partial Content (Content-Range: bytes 0-1048575/2048576)
    else Full Download Request
        Client<<--Handler: 200 OK (Complete Byte Stream)
    else Client Cache Valid (If-Modified-Since matches)
        Client<<--Handler: 304 Not Modified (0 bytes transferred)
    end
```

---

## 3. Step-by-Step Processing Pipeline

1. **Metadata Resolution**:
   The handler looks up the document to acquire the absolute storage path, original filename, and last modification timestamp.
2. **Path Traversal Shield**:
   [`storage.Local.resolve()`](file:///home/vnjdev/projects/godocs/internal/storage/local.go) verifies that:
   ```go
   clean := filepath.Clean(filepath.Join(l.Root, path))
   if !strings.HasPrefix(clean, filepath.Clean(l.Root)+string(filepath.Separator)) {
       return "", ErrInvalidPath
   }
   ```
   This prevents directory traversal attacks even if compromised database records attempt to point to sensitive system files.
3. **RFC 5987 Header Encoding**:
   The `Content-Disposition` header uses `filename*=UTF-8''...` encoding:
   ```http
   Content-Disposition: attachment; filename*=UTF-8''Financial%20Report%202026.pdf
   ```
   This allows Unicode characters in filenames while preventing HTTP header injection attacks.
4. **MIME Sniffing Block (`nosniff`)**:
   `X-Content-Type-Options: nosniff` forces browsers to respect the declared MIME type, preventing malicious files from being executed in browser contexts.
5. **Seeking & Range Support**:
   Because `*os.File` satisfies `io.ReadSeekCloser`, standard library `http.ServeContent` handles seek offsets automatically, providing `206 Partial Content` responses for PDF viewing and video streaming.
