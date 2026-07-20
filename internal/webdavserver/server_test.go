package webdavserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"inkflow/internal/ai"
	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/observability"
	"inkflow/internal/state"
)

func TestHTTPServerUsesConfiguredTimeouts(t *testing.T) {
	cfg := &config.Config{ListenAddr: "127.0.0.1:8080", ReadHeaderTimeoutDuration: time.Second, ReadTimeoutDuration: 2 * time.Second, WriteTimeoutDuration: 3 * time.Second, IdleTimeoutDuration: 4 * time.Second}
	httpSrv := newHTTPServer(cfg, http.NotFoundHandler())
	if httpSrv.ReadHeaderTimeout != time.Second || httpSrv.ReadTimeout != 2*time.Second || httpSrv.WriteTimeout != 3*time.Second || httpSrv.IdleTimeout != 4*time.Second {
		t.Errorf("unexpected timeouts: %+v", httpSrv)
	}
}

func TestHealthDoesNotRequireAuthenticationAndChecksStore(t *testing.T) {
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{WebDAVUser: "user", WebDAVPass: "pass"}
	srv := &Server{cfg: cfg, store: store}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy status = %d", rec.Code)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code == http.StatusOK {
		t.Fatal("closed state store reported healthy")
	}
}

func TestMetricsRequireMainListenerAuthentication(t *testing.T) {
	cfg := &config.Config{WebDAVUser: "user", WebDAVPass: "pass", Observability: config.ObservabilityConfig{MetricsEnabled: true}}
	srv := &Server{cfg: cfg, metrics: observability.New()}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics status = %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.SetBasicAuth("user", "pass")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "inkflow_state_records") {
		t.Fatalf("metrics response = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMetricsDisabledIsNotExposed(t *testing.T) {
	srv := &Server{cfg: &config.Config{}, metrics: observability.New()}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("disabled metrics status = %d", rec.Code)
	}
}

func TestPropfindUsesVaultMetadataAndDepth(t *testing.T) {
	vault := t.TempDir()
	stamp := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	if err := os.Mkdir(filepath.Join(vault, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(vault, "docs", "note file.md")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(vault, "docs", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: &config.Config{VaultDir: vault}}

	depthZero := httptest.NewRequest("PROPFIND", "/docs/note%20file.md", nil)
	depthZero.Header.Set("Depth", "0")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, depthZero)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("Depth 0 status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.Count(rec.Body.String(), "<D:response>"); got != 1 {
		t.Fatalf("Depth 0 responses = %d: %s", got, rec.Body.String())
	}
	for _, want := range []string{"note%20file.md", "5", stamp.Format(http.TimeFormat)} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("Depth 0 missing %q: %s", want, rec.Body.String())
		}
	}

	depthOne := httptest.NewRequest("PROPFIND", "/docs/", nil)
	depthOne.Header.Set("Depth", "1")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, depthOne)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("Depth 1 status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"<D:href>/docs/</D:href>", "note%20file.md", "<D:href>/docs/child/</D:href>"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("Depth 1 missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestPropfindRejectsMissingAndTraversalTargets(t *testing.T) {
	vault := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(vault, "escape")); err != nil {
		t.Fatal(err)
	}
	srv := &Server{cfg: &config.Config{VaultDir: vault}}
	for _, target := range []string{"/missing", "/%2e%2e/secret", "/docs/%2e%2e/%2e%2e/secret", "/escape"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("PROPFIND", target, nil))
		if rec.Code == http.StatusMultiStatus {
			t.Fatalf("%s unexpectedly returned properties: %s", target, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PROPFIND", "/", nil)
	req.Header.Set("Depth", "infinity")
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("infinite Depth status = %d", rec.Code)
	}
}

func TestAuthorizeBasicAuth(t *testing.T) {
	srv := &Server{cfg: &config.Config{WebDAVUser: "user", WebDAVPass: "pass"}}
	for name, credentials := range map[string][2]string{"valid": {"user", "pass"}, "bad-user": {"wrong", "pass"}, "bad-pass": {"user", "wrong"}} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "/", nil)
			req.SetBasicAuth(credentials[0], credentials[1])
			rec := httptest.NewRecorder()
			allowed := srv.authorize(rec, req)
			if name == "valid" && !allowed {
				t.Fatal("valid credentials rejected")
			}
			if name != "valid" {
				if allowed || rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
					t.Fatalf("invalid credentials allowed or not challenged: code=%d", rec.Code)
				}
			}
		})
	}
}

func TestLoopbackListenAddressDetection(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:8080": true, "[::1]:8080": true, "localhost:8080": true, "0.0.0.0:8080": false, ":8080": false, "bad": false} {
		if got := isLoopbackListenAddr(addr); got != want {
			t.Errorf("isLoopbackListenAddr(%q) = %t, want %t", addr, got, want)
		}
	}
}

func TestUnauthenticatedNonLoopbackBindWarns(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	srv := &Server{logger: logger}
	if !isLoopbackListenAddr("0.0.0.0:8080") {
		srv.warn("UNSAFE WEBDAV CONFIGURATION: unauthenticated plaintext vault writes are reachable on a non-loopback address")
	}
	if !strings.Contains(logs.String(), "UNSAFE WEBDAV CONFIGURATION") {
		t.Fatalf("missing non-loopback warning: %s", logs.String())
	}
	logs.Reset()
	if !isLoopbackListenAddr("127.0.0.1:8080") {
		srv.warn("unexpected warning")
	}
	if logs.Len() != 0 {
		t.Fatalf("loopback emitted warning: %s", logs.String())
	}
}

func TestShutdownUsesBoundedContext(t *testing.T) {
	httpSrv := &http.Server{}
	var logs bytes.Buffer
	srv := &Server{logger: slog.New(slog.NewTextHandler(&logs, nil))}
	done := make(chan struct{})
	go func() {
		srv.shutdown(httpSrv)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bounded shutdown did not return promptly for an idle server")
	}
}

func TestPutRejectsOversizeUpload(t *testing.T) {
	vaultDir := t.TempDir()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := &config.Config{VaultDir: vaultDir, DefaultPDFDir: "pdfs", DefaultNoteDir: "notes", MaxUploadBytes: 3, Routes: []config.Route{{From: "Syncs/"}}}
	srv := &Server{cfg: cfg, imp: importer.New(cfg, store, nil, 0), logger: slog.Default()}
	req := httptest.NewRequest(http.MethodPut, "/Syncs/oversize.pdf", bytes.NewBufferString("abcd"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d: %s", rec.Code, rec.Body.String())
	}
	record, err := store.GetBySourcePath("Syncs/oversize.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Fatal("oversize upload persisted a state record")
	}

	req = httptest.NewRequest(http.MethodPut, "/Syncs/limit.pdf", bytes.NewBufferString("abc"))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("boundary status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPutImportsFileIntoVault(t *testing.T) {
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "meeting"}},
	}
	imp := importer.New(cfg, store, nil, 0)
	srv := &Server{cfg: cfg, imp: imp}

	req := httptest.NewRequest("PUT", "/Syncs/2026-05-06%20Processing%20service%20%5Bfinance%5D.pdf", bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 201 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "pdfs", "2026-05-06-processing-service.pdf")); err != nil {
		t.Fatalf("pdf missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "notes", "2026-05-06 Processing service.md")); err != nil {
		t.Fatalf("note missing: %v", err)
	}
}

type fakeAIClient struct {
	result ai.Result
	err    error
}

type blockingAIClient struct{ started chan struct{} }

func (f blockingAIClient) Process(ctx context.Context, _ io.Reader) (ai.Result, error) {
	close(f.started)
	<-ctx.Done()
	return ai.Result{}, ctx.Err()
}

func TestPutWithAIQueuesWithoutWaitingForProvider(t *testing.T) {
	vaultDir := t.TempDir()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg := &config.Config{VaultDir: vaultDir, DefaultPDFDir: "pdfs", DefaultNoteDir: "notes", Routes: []config.Route{{From: "Syncs/", AI: true}}}
	started := make(chan struct{})
	srv := &Server{cfg: cfg, imp: importer.New(cfg, store, blockingAIClient{started: started}, 0)}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/Syncs/2026-06-04%20queued.pdf", bytes.NewBufferString("pdf")))
		done <- rec
	}()
	select {
	case rec := <-done:
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d", rec.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("PUT waited for AI provider")
	}
	select {
	case <-started:
		t.Fatal("PUT called AI provider")
	default:
	}
	stored, err := store.GetBySourcePath("Syncs/2026-06-04 queued.pdf")
	if err != nil || stored == nil || stored.AIStatus != state.AIStatusPending {
		t.Fatalf("queued record = %+v, err=%v", stored, err)
	}
}

func (f fakeAIClient) Process(ctx context.Context, pdf io.Reader) (ai.Result, error) {
	_, _ = io.Copy(io.Discard, pdf)
	return f.result, f.err
}

func TestPutImportsFileWithAIBlocks(t *testing.T) {
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default", AI: true}},
	}
	imp := importer.New(cfg, store, fakeAIClient{
		result: ai.Result{OCR: "full transcript", Summary: []string{"alpha", "beta"}},
	}, 0)
	srv := &Server{cfg: cfg, imp: imp}

	req := httptest.NewRequest("PUT", "/Syncs/2026-06-04%20note.pdf", bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 201 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(filepath.Join(vaultDir, "notes", "2026-06-04 note.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, "## Summary") || !strings.Contains(s, "_AI processing queued._") {
		t.Fatalf("summary block missing:\n%s", s)
	}
	if !strings.Contains(s, "## OCR") || !strings.Contains(s, "_AI processing queued._") {
		t.Fatalf("ocr block missing:\n%s", s)
	}
}

func TestPutSurfacesAIErrorInBothBlocks(t *testing.T) {
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default", AI: true}},
	}
	imp := importer.New(cfg, store, fakeAIClient{
		err: errors.New("gemini 401: API key invalid"),
	}, 0)
	srv := &Server{cfg: cfg, imp: imp}

	req := httptest.NewRequest("PUT", "/Syncs/2026-06-04%20bad.pdf", bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(filepath.Join(vaultDir, "notes", "2026-06-04 bad.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, "_AI processing queued._") {
		t.Fatalf("expected queued AI marker in note:\n%s", s)
	}
	if strings.Count(s, "_AI processing queued._") != 2 {
		t.Fatalf("expected queued marker in both blocks:\n%s", s)
	}
}

// fakeAIClient with refusal — fails the test if Process is called.
type refuseAIClient struct {
	t *testing.T
}

func (f refuseAIClient) Process(ctx context.Context, pdf io.Reader) (ai.Result, error) {
	f.t.Fatal("ai.Provider.Process must not be called when route.AI is false")
	return ai.Result{}, nil
}

func TestPutWithoutRouteAIDoesNotCallProvider(t *testing.T) {
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default"}}, // AI defaults to false
	}
	imp := importer.New(cfg, store, refuseAIClient{t: t}, 0)
	srv := &Server{cfg: cfg, imp: imp}

	req := httptest.NewRequest("PUT", "/Syncs/2026-06-04%20skip.pdf", bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != 201 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(filepath.Join(vaultDir, "notes", "2026-06-04 skip.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "## Summary") || strings.Contains(s, "## OCR") {
		t.Fatalf("note contains AI sections even though route.AI=false:\n%s", s)
	}
}

func TestPutReUploadReplacesAIBlocks(t *testing.T) {
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default", AI: true}},
	}

	// First upload with one Result.
	imp1 := importer.New(cfg, store, fakeAIClient{
		result: ai.Result{OCR: "first ocr", Summary: []string{"first bullet"}},
	}, 0)
	srv := &Server{cfg: cfg, imp: imp1}
	req := httptest.NewRequest("PUT", "/Syncs/2026-06-04%20idem.pdf", bytes.NewReader([]byte("v1")))
	srv.ServeHTTP(httptest.NewRecorder(), req)

	// Second upload with a different Result — should replace marker bodies, not append.
	imp2 := importer.New(cfg, store, fakeAIClient{
		result: ai.Result{OCR: "second ocr", Summary: []string{"second bullet"}},
	}, 0)
	srv.imp = imp2
	req = httptest.NewRequest("PUT", "/Syncs/2026-06-04%20idem.pdf", bytes.NewReader([]byte("v2")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(filepath.Join(vaultDir, "notes", "2026-06-04 idem.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "first ocr") || strings.Contains(s, "first bullet") {
		t.Fatalf("first-upload content survived in note:\n%s", s)
	}
	if strings.Count(s, "_AI processing queued._") != 2 {
		t.Fatalf("queued content missing:\n%s", s)
	}
	if strings.Count(s, "## OCR") != 1 || strings.Count(s, "## Summary") != 1 {
		t.Fatalf("marker block appended instead of replaced:\n%s", s)
	}
}

func TestPutSurfacesEmptyAIResultInBothBlocks(t *testing.T) {
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default", AI: true}},
	}
	imp := importer.New(cfg, store, fakeAIClient{
		result: ai.Result{}, // both fields empty, no error
	}, 0)
	srv := &Server{cfg: cfg, imp: imp}

	req := httptest.NewRequest("PUT", "/Syncs/2026-06-04%20empty.pdf", bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body, err := os.ReadFile(filepath.Join(vaultDir, "notes", "2026-06-04 empty.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if !strings.Contains(s, "_AI processing queued._") {
		t.Fatalf("expected queued placeholder:\n%s", s)
	}
	if strings.Count(s, "_AI processing queued._") != 2 {
		t.Fatalf("expected queued placeholders:\n%s", s)
	}
}

// countingAIClient counts calls so a test can assert how many times the AI
// provider was invoked across multiple uploads.
type countingAIClient struct {
	result ai.Result
	calls  *int
}

func (c countingAIClient) Process(ctx context.Context, pdf io.Reader) (ai.Result, error) {
	*c.calls++
	_, _ = io.Copy(io.Discard, pdf)
	return c.result, nil
}

func TestPutDuplicateUploadSkipsAICall(t *testing.T) {
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default", AI: true}},
	}
	calls := 0
	imp := importer.New(cfg, store, countingAIClient{
		result: ai.Result{OCR: "transcript", Summary: []string{"bullet"}},
		calls:  &calls,
	}, 0)
	srv := &Server{cfg: cfg, imp: imp}

	body := []byte("identical-pdf-bytes")
	for i := range 2 {
		req := httptest.NewRequest("PUT", "/Syncs/2026-06-04%20dup.pdf", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 201 {
			t.Fatalf("attempt %d: status = %d body=%s", i, rec.Code, rec.Body.String())
		}
	}
	if calls != 0 {
		t.Errorf("expected queued upload not to call ai.Provider.Process, got %d", calls)
	}
}

func TestPutAISuccessStoresAIStatusSuccess(t *testing.T) {
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default", AI: true}},
	}
	imp := importer.New(cfg, store, fakeAIClient{
		result: ai.Result{OCR: "some text", Summary: []string{"bullet one"}},
	}, 0)
	srv := &Server{cfg: cfg, imp: imp}

	req := httptest.NewRequest("PUT", "/Syncs/2026-07-01%20success.pdf", bytes.NewReader([]byte("pdf-content")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	stored, err := store.GetBySourcePath("Syncs/2026-07-01 success.pdf")
	if err != nil {
		t.Fatalf("GetBySourcePath: %v", err)
	}
	if stored == nil {
		t.Fatal("no record stored")
	}
	if stored.AIStatus != state.AIStatusPending {
		t.Errorf("AIStatus = %q, want %q", stored.AIStatus, state.AIStatusPending)
	}
	if stored.AIRetryCount != 0 {
		t.Errorf("AIRetryCount = %d, want 0", stored.AIRetryCount)
	}
	if stored.AILastError != "" {
		t.Errorf("AILastError = %q, want empty string", stored.AILastError)
	}
	if !stored.AILastRetryAt.IsZero() {
		t.Error("AILastRetryAt is non-zero before worker processing")
	}
}

func TestPutAIFailureStoresAIStatusFailed(t *testing.T) {
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default", AI: true}},
	}
	imp := importer.New(cfg, store, fakeAIClient{
		err: errors.New("gemini 503: service unavailable"),
	}, 0)
	srv := &Server{cfg: cfg, imp: imp}

	req := httptest.NewRequest("PUT", "/Syncs/2026-07-01%20failure.pdf", bytes.NewReader([]byte("pdf-content")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	// Existing behaviour: error text is written into the note.
	body, err := os.ReadFile(filepath.Join(vaultDir, "notes", "2026-07-01 failure.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "_AI processing queued._") {
		t.Errorf("queued marker block missing from note:\n%s", string(body))
	}

	// New behaviour: AI status is persisted in the state record.
	stored, err := store.GetBySourcePath("Syncs/2026-07-01 failure.pdf")
	if err != nil {
		t.Fatalf("GetBySourcePath: %v", err)
	}
	if stored == nil {
		t.Fatal("no record stored")
	}
	if stored.AIStatus != state.AIStatusPending {
		t.Errorf("AIStatus = %q, want %q", stored.AIStatus, state.AIStatusPending)
	}
	if stored.AIRetryCount != 0 {
		t.Errorf("AIRetryCount = %d, want 0", stored.AIRetryCount)
	}
	if stored.AILastError != "" {
		t.Error("AILastError is non-empty before worker processing")
	}
	if !stored.AILastRetryAt.IsZero() {
		t.Error("AILastRetryAt is non-zero before worker processing")
	}
}
