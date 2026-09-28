package webdavserver

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/state"
)

// recordedRequest captures everything a test needs to assert about a request
// the fake upstream received.
type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// fakeUpstream is a httptest WebDAV stand-in that records every request and
// lets a test script canned responses per path/method.
type fakeUpstream struct {
	mu       sync.Mutex
	requests []recordedRequest
	// respond, if set, overrides the default 201-with-empty-body response.
	respond func(rec recordedRequest) (status int, contentType, body string)
}

func newFakeUpstream(t *testing.T) (*httptest.Server, *fakeUpstream) {
	t.Helper()
	fu := &fakeUpstream{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec := recordedRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
		fu.mu.Lock()
		fu.requests = append(fu.requests, rec)
		fu.mu.Unlock()

		status, contentType, respBody := http.StatusCreated, "", ""
		if fu.respond != nil {
			status, contentType, respBody = fu.respond(rec)
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(ts.Close)
	return ts, fu
}

func (f *fakeUpstream) all() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

const testPrefix = "/remote.php/dav/files/anton"

func newTestUpstream(t *testing.T, tsURL, prefix string) *upstream {
	t.Helper()
	up, err := newUpstream(config.UpstreamConfig{
		URL:      tsURL,
		Prefix:   prefix,
		User:     "anton",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("newUpstream: %v", err)
	}
	return up
}

func newTestServer(t *testing.T, up *upstream, routes []config.Route) *Server {
	t.Helper()
	vaultDir := t.TempDir()
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.Open(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := &config.Config{
		VaultDir:       vaultDir,
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         routes,
	}
	imp := importer.New(cfg, store, nil)
	return &Server{cfg: cfg, imp: imp, up: up}
}

func basicAuthHeader(user, pass string) string {
	req, _ := http.NewRequest("GET", "http://x", nil)
	req.SetBasicAuth(user, pass)
	return req.Header.Get("Authorization")
}

func TestInterceptedPutImportsAndTeesToUpstreamPrefixedPath(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, []config.Route{{From: "onyx/Go103/Notebooks/Syncs/", Template: "meeting"}})

	req := httptest.NewRequest(http.MethodPut, "/onyx/Go103/Notebooks/Syncs/2026-05-06%20note.pdf", bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(srv.cfg.VaultDir, "notes", "2026-05-06 note.md")); err != nil {
		t.Fatalf("note not imported: %v", err)
	}

	reqs := fu.all()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one upstream request, got %d", len(reqs))
	}
	got := reqs[0]
	if got.Method != http.MethodPut {
		t.Errorf("method = %q", got.Method)
	}
	want := testPrefix + "/onyx/Go103/Notebooks/Syncs/2026-05-06 note.pdf"
	if got.Path != want {
		t.Errorf("path = %q, want %q", got.Path, want)
	}
	if string(got.Body) != "pdf-bytes" {
		t.Errorf("body = %q", got.Body)
	}
	if got.Header.Get("Authorization") != basicAuthHeader("anton", "secret") {
		t.Errorf("upstream request missing/incorrect basic auth")
	}
}

func TestInterceptedPutRetriesAfterMkcolOn409(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, []config.Route{{From: "onyx/Syncs/", Template: "meeting"}})

	putCount := 0
	fu.respond = func(rec recordedRequest) (int, string, string) {
		if rec.Method == http.MethodPut {
			putCount++
			if putCount == 1 {
				return http.StatusConflict, "", ""
			}
			return http.StatusCreated, "", ""
		}
		return http.StatusCreated, "", "" // MKCOL
	}

	req := httptest.NewRequest(http.MethodPut, "/onyx/Syncs/2026-05-06%20note.pdf", bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	reqs := fu.all()
	var mkcols []string
	var puts int
	for _, r := range reqs {
		if r.Method == "MKCOL" {
			mkcols = append(mkcols, r.Path)
		} else if r.Method == http.MethodPut {
			puts++
		}
	}
	if puts != 2 {
		t.Fatalf("expected 2 PUT attempts (initial + retry), got %d", puts)
	}
	wantMkcols := []string{
		testPrefix + "/onyx",
		testPrefix + "/onyx/Syncs",
	}
	if len(mkcols) != len(wantMkcols) {
		t.Fatalf("mkcols = %v, want %v", mkcols, wantMkcols)
	}
	for i, want := range wantMkcols {
		if mkcols[i] != want {
			t.Errorf("mkcol[%d] = %q, want %q", i, mkcols[i], want)
		}
	}
}

func TestInterceptedPutUpstream5xxReturns502(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, []config.Route{{From: "onyx/Syncs/", Template: "meeting"}})
	fu.respond = func(rec recordedRequest) (int, string, string) {
		return http.StatusInternalServerError, "", ""
	}

	req := httptest.NewRequest(http.MethodPut, "/onyx/Syncs/2026-05-06%20note.pdf", bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	// The local import must have happened despite the upstream failure —
	// that's what makes a BOOX retry of the same bytes safe (dedup skips
	// the importer on the retry and only the upstream PUT is attempted again).
	if _, err := os.Stat(filepath.Join(srv.cfg.VaultDir, "notes", "2026-05-06 note.md")); err != nil {
		t.Fatalf("note not imported despite upstream failure: %v", err)
	}
}

func TestUnmatchedPutIsProxiedWithoutImport(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, []config.Route{{From: "onyx/Syncs/", Template: "meeting"}})

	req := httptest.NewRequest(http.MethodPut, "/Books/somebook.epub", bytes.NewReader([]byte("epub-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	entries, _ := os.ReadDir(filepath.Join(srv.cfg.VaultDir, "notes"))
	if len(entries) != 0 {
		t.Fatalf("expected no notes written for unmatched/proxied PUT, got %v", entries)
	}
	reqs := fu.all()
	want := testPrefix + "/Books/somebook.epub"
	if len(reqs) != 1 || reqs[0].Path != want {
		t.Fatalf("unexpected upstream requests: %+v, want path %q", reqs, want)
	}
}

func TestGetIsProxiedToUpstreamPrefixPath(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, nil)
	fu.respond = func(rec recordedRequest) (int, string, string) {
		return http.StatusOK, "application/epub+zip", "epub-bytes"
	}

	req := httptest.NewRequest(http.MethodGet, "/Books/x.epub", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "epub-bytes" {
		t.Errorf("body = %q", rec.Body.String())
	}
	reqs := fu.all()
	want := testPrefix + "/Books/x.epub"
	if len(reqs) != 1 || reqs[0].Path != want {
		t.Fatalf("unexpected upstream requests: %+v, want path %q", reqs, want)
	}
	if reqs[0].Header.Get("Authorization") != basicAuthHeader("anton", "secret") {
		t.Errorf("upstream auth missing/incorrect")
	}
}

// nextcloudMultistatus builds a Nextcloud-flavoured PROPFIND response: one
// self-referencing entry as an absolute path, one child as an absolute URL
// with a percent-encoded (Cyrillic) filename — both under the test prefix.
func nextcloudMultistatus(selfHref, childHref string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:response>
    <d:href>%s</d:href>
    <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:response>
    <d:href>%s</d:href>
    <d:propstat><d:prop><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
</d:multistatus>`, selfHref, childHref)
}

func TestPropfindAtRootRewritesHrefsToStripPrefix(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, nil)
	fu.respond = func(rec recordedRequest) (int, string, string) {
		return http.StatusMultiStatus, "application/xml; charset=utf-8",
			nextcloudMultistatus(testPrefix+"/", testPrefix+"/Books/")
	}

	req := httptest.NewRequest("PROPFIND", "/", nil)
	req.Header.Set("Depth", "1")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<d:href>/</d:href>") {
		t.Errorf("self href not rewritten to /: %s", body)
	}
	if !strings.Contains(body, "<d:href>/Books/</d:href>") {
		t.Errorf("child href not rewritten to /Books/: %s", body)
	}
	if strings.Contains(body, testPrefix) {
		t.Errorf("prefix leaked into response body: %s", body)
	}
	wantLen := fmt.Sprintf("%d", len(body))
	if got := rec.Header().Get("Content-Length"); got != wantLen {
		t.Errorf("Content-Length = %q, want %q", got, wantLen)
	}
}

func TestPropfindAtBooksRewritesAbsoluteURLHref(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, nil)
	childAbsoluteURL := ts.URL + testPrefix + "/Books/%D0%9A%D0%BD%D0%B8%D0%B3%D0%B0.epub"
	fu.respond = func(rec recordedRequest) (int, string, string) {
		return http.StatusMultiStatus, "application/xml; charset=utf-8",
			nextcloudMultistatus(testPrefix+"/Books/", childAbsoluteURL)
	}

	req := httptest.NewRequest("PROPFIND", "/Books/", nil)
	req.Header.Set("Depth", "1")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<d:href>/Books/</d:href>") {
		t.Errorf("self href not rewritten to /Books/: %s", body)
	}
	if !strings.Contains(body, "<d:href>/Books/%D0%9A%D0%BD%D0%B8%D0%B3%D0%B0.epub</d:href>") {
		t.Errorf("absolute-URL child href not rewritten to a relative path: %s", body)
	}
	if strings.Contains(body, ts.URL) || strings.Contains(body, testPrefix) {
		t.Errorf("upstream origin/prefix leaked into response body: %s", body)
	}
}

func TestMoveRewritesDestinationToUpstreamPrefixed(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, nil)

	req := httptest.NewRequest("MOVE", "/Books/a.epub", nil)
	req.Header.Set("Destination", "http://inkflow.example/Books/b.epub")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	reqs := fu.all()
	if len(reqs) != 1 {
		t.Fatalf("expected one proxied request, got %d", len(reqs))
	}
	if reqs[0].Path != testPrefix+"/Books/a.epub" {
		t.Errorf("request path = %q", reqs[0].Path)
	}
	dest := reqs[0].Header.Get("Destination")
	want := ts.URL + testPrefix + "/Books/b.epub"
	if dest != want {
		t.Errorf("Destination = %q, want %q", dest, want)
	}
}

func TestUpstreamNeverHitOnBadInkflowAuth(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, nil)
	srv.cfg.WebDAVUser = "boox"
	srv.cfg.WebDAVPass = "boox-pass"

	req := httptest.NewRequest(http.MethodGet, "/Books/a.epub", nil)
	req.SetBasicAuth("wrong", "creds")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(fu.all()) != 0 {
		t.Fatalf("upstream must not be hit on bad inkflow auth, got %d requests", len(fu.all()))
	}
}
