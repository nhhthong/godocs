package main

// Smoke / Integration Test Suite: Instantiates the authentic runtime stack (ephemeral SQLite database,
// isolated disk storage, real background worker pool, live middleware pipeline) behind an httptest.Server
// and invokes endpoints via standard HTTP clients. Unlike isolated unit tests with mock objects, this suite
// validates end-to-end wiring fidelity, SQL dialect compliance, session cookie persistence, and concurrency.
//
// Execute exclusively: go test ./cmd/api -run TestSmoke -v

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/you/godocs/internal/auth"
	"github.com/you/godocs/internal/cache"
	"github.com/you/godocs/internal/db"
	"github.com/you/godocs/internal/document"
	"github.com/you/godocs/internal/httpx"
	"github.com/you/godocs/internal/storage"
	"github.com/you/godocs/internal/worker"
)

// testOpts provides configuration knobs allowing individual tests to fine-tune rate limiting and CORS policies.
type testOpts struct {
	cors        []string
	globalRPS   float64
	globalBurst int
	authRPS     float64
	authBurst   int
}

// looseOpts yields permissive rate-limiting thresholds to prevent throttling during core lifecycle tests.
func looseOpts() testOpts {
	return testOpts{globalRPS: 1000, globalBurst: 1000, authRPS: 1000, authBurst: 1000}
}

// buildTestServer mirrors the initialization hierarchy of main.run() while avoiding real network port
// binding and operating system signal capture. Any disparity between production wiring and this fixture
// immediately yields test failures.
func buildTestServer(t *testing.T, o testOpts) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil)) // suppress log noise during automated test runs
	dir := t.TempDir()
	ctx := context.Background()

	pool, err := db.Open(ctx, "file:"+dir+"/t.db?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	if err := db.Migrate(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	blobs, err := storage.NewLocal(dir + "/blobs")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}

	docCache := cache.New[string, *document.Document](time.Minute)

	authRepo := db.NewAuthRepo(pool)
	authSvc := auth.NewService(authRepo, time.Hour)
	authHandler := auth.NewHandler(authSvc, false /* cookieSecure: disabled over plain HTTP test fixtures */, time.Hour)

	repo := db.NewDocumentRepo(pool)
	var svc *document.Service
	idx := worker.NewIndexer(nil, log, 64)
	svc = document.NewService(repo, blobAdapter{blobs}, docCache, idx, log, 25<<20)
	idx.SetService(svc)
	idx.Start(2)

	docHandler := document.NewHandler(svc, fileAdapter{blobs}, 25<<20)

	mux := http.NewServeMux()
	mux.Handle("/api/auth/", httpx.RateLimit(o.authRPS, o.authBurst)(authHandler.Routes()))
	protected := authSvc.RequireAuth(docHandler.Routes())
	mux.Handle("/api/documents", protected)
	mux.Handle("/api/documents/", protected)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	handler := httpx.Chain(mux,
		httpx.CORS(o.cors),
		httpx.WithRequestID,
		httpx.WithRecover(log),
		httpx.RateLimit(o.globalRPS, o.globalBurst),
		httpx.WithTimeout(30*time.Second),
	)

	srv := httptest.NewServer(handler)
	t.Cleanup(func() {
		srv.Close()
		sc, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = idx.Shutdown(sc)
		_ = pool.Close()
	})
	return srv
}

// --- HTTP Helpers ---

func doJSON(t *testing.T, c *http.Client, method, url string, payload any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s %s: response body is not valid JSON: %s", method, url, raw)
		}
	}
	return res.StatusCode, m
}

func doUpload(t *testing.T, c *http.Client, url, title, content string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("title", title)
	_ = mw.WriteField("summary", "test summary")
	fw, err := mw.CreateFormFile("file", "doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil { // Closing is mandatory to finalize multipart boundary delimiter
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return res.StatusCode, m
}

func toInt(v any) int { f, _ := v.(float64); return int(f) } // Converts unmarshalled JSON numbers (float64) to integer

func waitStatus(t *testing.T, c *http.Client, url, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, m := doJSON(t, c, "GET", url, nil)
		if m["status"] == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("document at %s failed to transition to status %q within %s", url, want, timeout)
}

// --- End-to-End Integration Flow: Register -> Login -> Upload -> CRUD -> Worker -> Logout ---

func TestSmokeFullFlow(t *testing.T) {
	srv := buildTestServer(t, looseOpts())
	base := srv.URL

	jar, _ := cookiejar.New(nil)
	user := &http.Client{Jar: jar} // maintains stateful session cookies
	anon := &http.Client{}         // unauthenticated client lacking cookies

	if code, _ := doJSON(t, anon, "GET", base+"/healthz", nil); code != http.StatusOK {
		t.Fatalf("healthz: %d", code)
	}

	// Unauthenticated upload must be rejected with 401 Unauthorized.
	if code, _ := doUpload(t, anon, base+"/api/documents", "x", "hi"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated upload: expected 401, got %d", code)
	}

	// User registration.
	code, body := doJSON(t, user, "POST", base+"/api/auth/register",
		map[string]string{"email": "u@ex.com", "password": "supersecret"})
	if code != http.StatusCreated {
		t.Fatalf("register: %d %v", code, body)
	}
	userID, _ := body["id"].(string)
	if userID == "" {
		t.Fatalf("register: missing id attribute in %v", body)
	}

	// User login -> Session cookie populated in cookie jar.
	if code, _ := doJSON(t, user, "POST", base+"/api/auth/login",
		map[string]string{"email": "u@ex.com", "password": "supersecret"}); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}

	// Invalid password authentication attempt -> 401 Unauthorized.
	if code, _ := doJSON(t, user, "POST", base+"/api/auth/login",
		map[string]string{"email": "u@ex.com", "password": "wrong-password"}); code != http.StatusUnauthorized {
		t.Fatalf("invalid credentials login: expected 401, got %d", code)
	}

	// Profile verification endpoint (/api/auth/me).
	if code, me := doJSON(t, user, "GET", base+"/api/auth/me", nil); code != http.StatusOK || me["email"] != "u@ex.com" {
		t.Fatalf("me: %d %v", code, me)
	}

	// Authenticated upload with valid session cookie -> 201 Created, created_by set to user ID, file_path unexposed.
	code, doc := doUpload(t, user, base+"/api/documents", "Q3 Report", "confidential document contents")
	if code != http.StatusCreated {
		t.Fatalf("upload: %d %v", code, doc)
	}
	if doc["created_by"] != userID {
		t.Fatalf("created_by = %v, expected %s", doc["created_by"], userID)
	}
	if _, leaked := doc["file_path"]; leaked {
		t.Fatalf("internal file_path leaked into public API response: %v", doc)
	}
	docID, _ := doc["id"].(string)

	// List documents.
	if code, list := doJSON(t, user, "GET", base+"/api/documents?limit=10", nil); code != http.StatusOK || toInt(list["total"]) < 1 {
		t.Fatalf("list: %d %v", code, list)
	}

	// Retrieve single document by ID.
	if code, _ := doJSON(t, user, "GET", base+"/api/documents/"+docID, nil); code != http.StatusOK {
		t.Fatalf("get: %d", code)
	}

	// Patch document title.
	if code, upd := doJSON(t, user, "PATCH", base+"/api/documents/"+docID,
		map[string]string{"title": "Updated Title"}); code != http.StatusOK || upd["title"] != "Updated Title" {
		t.Fatalf("patch: %d %v", code, upd)
	}

	// Patch with unknown field -> 400 Bad Request (enforced by DisallowUnknownFields).
	if code, _ := doJSON(t, user, "PATCH", base+"/api/documents/"+docID,
		map[string]int{"unexpected_field": 1}); code != http.StatusBadRequest {
		t.Fatalf("patch unknown field: expected 400, got %d", code)
	}

	// Background worker state transition: pending -> ready.
	waitStatus(t, user, base+"/api/documents/"+docID, "ready", 3*time.Second)

	// Binary download: verify body content and security headers.
	req, _ := http.NewRequest("GET", base+"/api/documents/"+docID+"/file", nil)
	res, err := user.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(raw) != "confidential document contents" {
		t.Fatalf("download: status %d body=%q", res.StatusCode, raw)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("download: missing security header X-Content-Type-Options: nosniff")
	}
	if !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: Content-Disposition = %q", res.Header.Get("Content-Disposition"))
	}

	// Deletion: 204 No Content, subsequent GET returns 404 Not Found.
	if code, _ := doJSON(t, user, "DELETE", base+"/api/documents/"+docID, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := doJSON(t, user, "GET", base+"/api/documents/"+docID, nil); code != http.StatusNotFound {
		t.Fatalf("get after delete: expected 404, got %d", code)
	}

	// Logout: session invalidated -> /api/auth/me yields 401 Unauthorized.
	if code, _ := doJSON(t, user, "POST", base+"/api/auth/logout", nil); code != http.StatusNoContent {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := doJSON(t, user, "GET", base+"/api/auth/me", nil); code != http.StatusUnauthorized {
		t.Fatalf("me after logout: expected 401, got %d", code)
	}
}

// --- Cross-Origin Resource Sharing (CORS) Verification ---

func TestSmokeCORS(t *testing.T) {
	o := looseOpts()
	o.cors = []string{"https://app.example.com"}
	srv := buildTestServer(t, o)
	c := &http.Client{}

	preflight := func(origin string) *http.Response {
		req, _ := http.NewRequest("OPTIONS", srv.URL+"/api/documents", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}

	// Authorized origin: 204 No Content with complete CORS headers including Allow-Credentials for cookies.
	ok := preflight("https://app.example.com")
	if ok.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight allowed: status %d", ok.StatusCode)
	}
	if ok.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("ACAO = %q", ok.Header.Get("Access-Control-Allow-Origin"))
	}
	if ok.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("missing header Access-Control-Allow-Credentials")
	}

	// Unauthorized origin: CORS headers withheld, leading browser user-agents to abort.
	bad := preflight("https://evil.example")
	if got := bad.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unauthorized origin returned unexpected ACAO: %q", got)
	}
}

// --- Global Rate Limiting Verification ---

func TestSmokeGlobalRateLimit(t *testing.T) {
	o := looseOpts()
	o.globalRPS, o.globalBurst = 5, 5
	srv := buildTestServer(t, o)
	c := &http.Client{}

	var ok, limited int
	for i := 0; i < 20; i++ {
		res, err := c.Get(srv.URL + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		switch res.StatusCode {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if ok < 5 {
		t.Fatalf("burst=5 but only %d requests succeeded", ok)
	}
	if limited == 0 {
		t.Fatalf("20 consecutive requests produced zero 429 responses (ok=%d)", ok)
	}
}

// --- Dedicated Authentication Rate Limiting (Brute-Force Mitigation) ---

func TestSmokeAuthRateLimit(t *testing.T) {
	o := looseOpts()
	o.authRPS, o.authBurst = 1, 3
	srv := buildTestServer(t, o)
	c := &http.Client{}

	// Query /api/auth/me (governed by the same rate-limiter as login, but bypassing expensive bcrypt hashing)
	// to ensure consistent test timing. Given burst=3, subsequent requests must be rejected with 429.
	var ok, limited int
	for i := 0; i < 12; i++ {
		res, err := c.Get(srv.URL + "/api/auth/me")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		switch res.StatusCode {
		case http.StatusUnauthorized: // Unauthenticated request yields 401, counting as processed traffic
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if ok < 3 {
		t.Fatalf("burst=3 but only %d requests passed through", ok)
	}
	if limited == 0 {
		t.Fatalf("12 consecutive requests failed to trigger auth rate-limiting (ok=%d)", ok)
	}
}
