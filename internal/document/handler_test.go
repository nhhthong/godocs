package document

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

type nopOpener struct{}

func (nopOpener) Open(string) (io.ReadSeekCloser, error) { return nil, ErrNotFound }

func TestCreateEndpoint(t *testing.T) {
	svc, _, _ := newTestService()
	srv := httptest.NewServer(NewHandler(svc, nopOpener{}, 5<<20).Routes())
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", "Q3 Report")
	_ = mw.WriteField("summary", "brief summary")
	fw, err := mw.CreateFormFile("file", "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("file content payload"))
	mw.Close() // Mandatory to write closing multipart boundary delimiter

	resp, err := http.Post(srv.URL+"/api/documents", mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, expected 201; body=%s", resp.StatusCode, b)
	}
	raw, _ := io.ReadAll(resp.Body)
	var got Document
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "Q3 Report" || got.ID == "" {
		t.Fatalf("unexpected document payload: %+v", got)
	}
	if bytes.Contains(raw, []byte("file_path")) {
		t.Errorf("internal file_path unexpectedly leaked in API response: %s", raw)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	svc, _, _ := newTestService()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/documents/abc", nil)
	NewHandler(svc, nopOpener{}, 1<<20).Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status code = %d, expected 405 Method Not Allowed", rec.Code)
	}
}
