package document

// handler.go implements the HTTP transport layer for documents: routing, multipart form parsing,
// request decoding, service invocation, and unified domain-to-HTTP error mapping.

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/you/godocs/internal/auth"
	"github.com/you/godocs/internal/httpx"
)

// FileOpener abstracts binary file retrieval for HTTP download streaming.
type FileOpener interface {
	Open(path string) (io.ReadSeekCloser, error)
}

// Handler serves HTTP endpoints governing the document lifecycle.
type Handler struct {
	svc     *Service
	files   FileOpener
	maxBody int64
}

// NewHandler initializes a document Handler with specified file retrieval and upload limits.
func NewHandler(svc *Service, files FileOpener, maxBody int64) *Handler {
	return &Handler{svc: svc, files: files, maxBody: maxBody}
}

// Routes configures endpoints utilizing Go 1.22+ method-qualified patterns and path wildcards.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/documents", h.create)
	mux.HandleFunc("GET /api/documents", h.list)
	mux.HandleFunc("GET /api/documents/{id}", h.get)
	mux.HandleFunc("GET /api/documents/{id}/file", h.download)
	mux.HandleFunc("PATCH /api/documents/{id}", h.update)
	mux.HandleFunc("DELETE /api/documents/{id}", h.delete)
	return mux
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	// Guard against oversized request bodies prior to parsing.
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)

	// Retains up to 10MB in memory; residual data spools to temporary disk files.
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			httpx.Error(w, http.StatusRequestEntityTooLarge, "body_too_large", "file size exceeds allowed limit", nil)
		} else {
			httpx.Error(w, http.StatusBadRequest, "invalid_form", "invalid multipart form data", nil)
		}
		return
	}
	defer r.MultipartForm.RemoveAll() // Ensure temporary spool files are purged

	file, hdr, err := r.FormFile("file")
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_input", "missing file payload", nil)
		return
	}
	defer file.Close()

	// Authenticated user context resolved from upstream auth.RequireAuth middleware.
	var createdBy string
	if u, ok := auth.UserFrom(r.Context()); ok {
		createdBy = u.ID
	}

	doc, err := h.svc.Create(r.Context(), CreateInput{
		Title:     r.FormValue("title"),
		Summary:   r.FormValue("summary"),
		FileName:  hdr.Filename,
		File:      file,
		CreatedBy: createdBy,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Location", "/api/documents/"+doc.ID)
	httpx.JSON(w, http.StatusCreated, doc)
}

// ownerID returns the authenticated user's ID. Document routes are always mounted
// behind auth.RequireAuth, so a user is guaranteed to be present.
func ownerID(r *http.Request) string {
	if u, ok := auth.UserFrom(r.Context()); ok {
		return u.ID
	}
	return ""
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	items, total, err := h.svc.List(r.Context(), ListFilter{Query: q.Get("q"), Owner: ownerID(r), Limit: limit, Offset: offset})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	doc, err := h.svc.Get(r.Context(), r.PathValue("id"), ownerID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, doc)
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	doc, err := h.svc.Get(r.Context(), r.PathValue("id"), ownerID(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	f, err := h.files.Open(doc.FilePath)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", doc.MimeType)
	// attachment with RFC 5987 encoded filename mitigates header injection and malicious browser execution
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEscape(doc.FileName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// http.ServeContent transparently negotiates Range requests, ETags, and If-Modified-Since headers
	http.ServeContent(w, r, doc.FileName, doc.UpdatedAt, f)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var in UpdateInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}
	doc, err := h.svc.Update(r.Context(), r.PathValue("id"), ownerID(r), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, doc)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(r.Context(), r.PathValue("id"), ownerID(r)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeErr maps domain error instances to standardized HTTP error responses.
func writeErr(w http.ResponseWriter, err error) {
	var ve ValidationErrors
	switch {
	case errors.As(err, &ve):
		httpx.Error(w, http.StatusUnprocessableEntity, "validation_failed", "validation failed", ve)
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not_found", "document not found", nil)
	case errors.Is(err, ErrMimeType):
		httpx.Error(w, http.StatusUnsupportedMediaType, "unsupported_type", err.Error(), nil)
	case errors.Is(err, ErrTooLarge):
		httpx.Error(w, http.StatusRequestEntityTooLarge, "file_too_large", err.Error(), nil)
	case errors.Is(err, ErrInvalid):
		httpx.Error(w, http.StatusBadRequest, "invalid_input", err.Error(), nil)
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal_error", "an internal error occurred", nil)
	}
}

func urlEscape(s string) string {
	const hexd = "0123456789ABCDEF"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '.' || c == '-' || c == '_' {
			out = append(out, c)
			continue
		}
		out = append(out, '%', hexd[c>>4], hexd[c&0x0f])
	}
	return string(out)
}
