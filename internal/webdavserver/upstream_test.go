package webdavserver

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/state"
)

type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

type fakeUpstream struct {
	mu       sync.Mutex
	requests []recordedRequest
	respond  func(rec recordedRequest) (status int, contentType, body string)
	// gzipIfAccepted mimics a Caddy `encode gzip` directive; opt-in per test.
	gzipIfAccepted bool
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
		payload := []byte(respBody)
		if fu.gzipIfAccepted && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			_, _ = gz.Write(payload)
			_ = gz.Close()
			payload = buf.Bytes()
			w.Header().Set("Content-Encoding", "gzip")
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, _ = w.Write(payload)
		}
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

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func newTestUpstream(t *testing.T, tsURL, prefix string) *upstream {
	t.Helper()
	return newTestUpstreamWithLogger(t, tsURL, prefix, testLogger)
}

func newTestUpstreamWithLogger(t *testing.T, tsURL, prefix string, logger *slog.Logger) *upstream {
	t.Helper()
	up, err := newUpstream(config.UpstreamConfig{
		URL:      tsURL,
		Prefix:   prefix,
		User:     "anton",
		Password: "secret",
	}, logger)
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
	// Import must have happened despite the upstream failure — makes a BOOX retry safe (importer dedups by hash+destination).
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

func TestPropfindDecompressesGzipBeforeRewritingHrefs(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	fu.gzipIfAccepted = true
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, nil)
	fu.respond = func(rec recordedRequest) (int, string, string) {
		return http.StatusMultiStatus, "application/xml; charset=utf-8",
			nextcloudMultistatus(testPrefix+"/", testPrefix+"/Books/")
	}

	req := httptest.NewRequest("PROPFIND", "/", nil)
	req.Header.Set("Depth", "1")
	req.Header.Set("Accept-Encoding", "gzip") // what OkHttp/BOOX sends
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("Content-Encoding leaked to client: %q", rec.Header().Get("Content-Encoding"))
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<d:href>/</d:href>") || !strings.Contains(body, "<d:href>/Books/</d:href>") {
		t.Fatalf("hrefs not rewritten after gzip round-trip: %s", body)
	}
}

func TestRewriteResponseSkipsBodyWhenContentEncodingPresent(t *testing.T) {
	up := newTestUpstream(t, "http://upstream.invalid", testPrefix)
	const original = `<d:multistatus xmlns:d="DAV:"><d:response><d:href>` + testPrefix + `/Books/</d:href></d:response></d:multistatus>`
	req := httptest.NewRequest("PROPFIND", "/Books/", nil)
	resp := &http.Response{
		StatusCode: http.StatusMultiStatus,
		Request:    req,
		Header: http.Header{
			"Content-Type":     {"application/xml"},
			"Content-Encoding": {"br"},
		},
		Body: io.NopCloser(strings.NewReader(original)),
	}
	if err := up.proxy.ModifyResponse(resp); err != nil {
		t.Fatalf("ModifyResponse: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != original {
		t.Errorf("body changed despite Content-Encoding: got %q, want %q", got, original)
	}
}

func TestInterceptedPutHandlesSpecialCharactersInFilename(t *testing.T) {
	names := []string{
		"note #1.pdf",
		"what?.pdf",
		"100%.pdf",
		"Книга.pdf",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ts, fu := newFakeUpstream(t)
			up := newTestUpstream(t, ts.URL, testPrefix)
			srv := newTestServer(t, up, []config.Route{{From: "onyx/Syncs/", Template: "meeting"}})

			target := "/onyx/Syncs/" + url.PathEscape(name)
			req := httptest.NewRequest(http.MethodPut, target, bytes.NewReader([]byte("pdf-bytes")))
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			reqs := fu.all()
			if len(reqs) != 1 {
				t.Fatalf("expected one upstream request, got %d", len(reqs))
			}
			want := testPrefix + "/onyx/Syncs/" + name
			if reqs[0].Path != want {
				t.Errorf("upstream path = %q, want %q", reqs[0].Path, want)
			}
		})
	}
}

func TestUpstreamURLWithTrailingSlashDoesNotDoubleSlash(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL+"/", testPrefix)
	srv := newTestServer(t, up, nil)

	req := httptest.NewRequest(http.MethodGet, "/Books/x.epub", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	reqs := fu.all()
	if len(reqs) != 1 {
		t.Fatalf("expected one proxied request, got %d", len(reqs))
	}
	if strings.Contains(reqs[0].Path, "//") {
		t.Errorf("double slash in upstream path: %q", reqs[0].Path)
	}
	want := testPrefix + "/Books/x.epub"
	if reqs[0].Path != want {
		t.Errorf("path = %q, want %q", reqs[0].Path, want)
	}
}

func TestNonMultistatusXMLPassesThroughUntouched(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, nil)
	const fb2Body = `<?xml version="1.0"?><FictionBook><href>should not be touched</href></FictionBook>`
	fu.respond = func(rec recordedRequest) (int, string, string) {
		return http.StatusOK, "application/x-fictionbook+xml", fb2Body
	}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, "/Books/book.fb2", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", method, rec.Code)
		}
		if method == http.MethodGet && rec.Body.String() != fb2Body {
			t.Errorf("%s: body mangled: %q", method, rec.Body.String())
		}
		wantLen := strconv.Itoa(len(fb2Body))
		if got := rec.Header().Get("Content-Length"); got != wantLen {
			t.Errorf("%s: Content-Length = %q, want %q", method, got, wantLen)
		}
	}
	_ = fu
}

func TestInterceptedPutForwardsConditionalAndNextcloudHeaders(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, []config.Route{{From: "onyx/Syncs/", Template: "meeting"}})

	req := httptest.NewRequest(http.MethodPut, "/onyx/Syncs/2026-05-06%20note.pdf", bytes.NewReader([]byte("pdf-bytes")))
	req.Header.Set("If", `(<opaquelocktoken:abc>)`)
	req.Header.Set("X-OC-Mtime", "1700000000")
	req.Header.Set("OC-Checksum", "SHA1:deadbeef")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	reqs := fu.all()
	if len(reqs) != 1 {
		t.Fatalf("expected one upstream request, got %d", len(reqs))
	}
	got := reqs[0].Header
	if got.Get("If") != `(<opaquelocktoken:abc>)` {
		t.Errorf("If header not forwarded: %q", got.Get("If"))
	}
	if got.Get("X-OC-Mtime") != "1700000000" {
		t.Errorf("X-OC-Mtime not forwarded: %q", got.Get("X-OC-Mtime"))
	}
	if got.Get("OC-Checksum") != "SHA1:deadbeef" {
		t.Errorf("OC-Checksum not forwarded: %q", got.Get("OC-Checksum"))
	}
}

func TestInterceptedPutPassesThrough204FromUpstream(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, []config.Route{{From: "onyx/Syncs/", Template: "meeting"}})
	fu.respond = func(rec recordedRequest) (int, string, string) {
		return http.StatusNoContent, "", ""
	}

	req := httptest.NewRequest(http.MethodPut, "/onyx/Syncs/2026-05-06%20note.pdf", bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestStripPrefixFromValue(t *testing.T) {
	up := newTestUpstream(t, "http://upstream.invalid", testPrefix)

	sibling := testPrefix + "ina/x" // "/remote.php/dav/files/antonina/x" — a different user, not a subpath
	if got, changed := up.stripPrefixFromValue(sibling); changed {
		t.Errorf("sibling path %q was rewritten to %q, want untouched", sibling, got)
	}

	// '@' is a valid unescaped path character (RFC 3986 pchar), so a
	// Nextcloud email-style user id normally appears unescaped in hrefs too.
	up2 := newTestUpstream(t, "http://upstream.invalid", testPrefix+"@example.com")
	if got, changed := up2.stripPrefixFromValue(testPrefix + "@example.com/Books/"); !changed || got != "/Books/" {
		t.Errorf("literal '@' in prefix: got %q, changed=%v", got, changed)
	}

	up3 := newTestUpstream(t, "http://upstream.invalid", testPrefix+" home")
	if got, changed := up3.stripPrefixFromValue(testPrefix + "%20home/Books/"); !changed || got != "/Books/" {
		t.Errorf("percent-encoded space in prefix: got %q, changed=%v", got, changed)
	}

	// Same byte, different hex case (some servers emit lowercase percent-hex).
	up4 := newTestUpstream(t, "http://upstream.invalid", testPrefix+"ы")
	if got, changed := up4.stripPrefixFromValue(testPrefix + "%d1%8b/Books/"); !changed || got != "/Books/" {
		t.Errorf("lowercase percent-hex prefix match: got %q, changed=%v", got, changed)
	}
}

func TestProxyPathCannotClimbAbovePrefix(t *testing.T) {
	ts, fu := newFakeUpstream(t)
	up := newTestUpstream(t, ts.URL, testPrefix)
	srv := newTestServer(t, up, nil)

	req := httptest.NewRequest(http.MethodGet, "/../../../secret", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	reqs := fu.all()
	if len(reqs) != 1 {
		t.Fatalf("expected one proxied request, got %d", len(reqs))
	}
	if !strings.HasPrefix(reqs[0].Path, testPrefix) {
		t.Errorf("traversal escaped the prefix: upstream path = %q", reqs[0].Path)
	}
}

func TestOversizedMultistatusBodyPassesThroughWithWarning(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	ts, fu := newFakeUpstream(t)
	up := newTestUpstreamWithLogger(t, ts.URL, testPrefix, logger)
	srv := newTestServer(t, up, nil)

	oversized := strings.Repeat("a", maxRewriteBody+1024)
	fu.respond = func(rec recordedRequest) (int, string, string) {
		return http.StatusMultiStatus, "application/xml; charset=utf-8", oversized
	}

	req := httptest.NewRequest("PROPFIND", "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.Len() != len(oversized) {
		t.Errorf("body length = %d, want %d (passthrough)", rec.Body.Len(), len(oversized))
	}
	if !strings.Contains(logBuf.String(), "cap") {
		t.Errorf("expected a warning log mentioning the size cap, got: %s", logBuf.String())
	}
}
