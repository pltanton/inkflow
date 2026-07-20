package webdavserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"inkflow/internal/ai"
	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/state"
)

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
	imp := importer.New(cfg, store, nil)
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
	})
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
	if !strings.Contains(s, "## Summary") || !strings.Contains(s, "- alpha") || !strings.Contains(s, "- beta") {
		t.Fatalf("summary block missing:\n%s", s)
	}
	if !strings.Contains(s, "## OCR") || !strings.Contains(s, "full transcript") {
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
	})
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
	if !strings.Contains(s, "_AI failed: gemini 401: API key invalid_") {
		t.Fatalf("expected AI error in note:\n%s", s)
	}
	if strings.Count(s, "_AI failed:") < 2 {
		t.Fatalf("expected error in both blocks (summary + ocr):\n%s", s)
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
	imp := importer.New(cfg, store, refuseAIClient{t: t})
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
	})
	srv := &Server{cfg: cfg, imp: imp1}
	req := httptest.NewRequest("PUT", "/Syncs/2026-06-04%20idem.pdf", bytes.NewReader([]byte("v1")))
	srv.ServeHTTP(httptest.NewRecorder(), req)

	// Second upload with a different Result — should replace marker bodies, not append.
	imp2 := importer.New(cfg, store, fakeAIClient{
		result: ai.Result{OCR: "second ocr", Summary: []string{"second bullet"}},
	})
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
	if !strings.Contains(s, "second ocr") || !strings.Contains(s, "- second bullet") {
		t.Fatalf("second-upload content missing:\n%s", s)
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
	})
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
	if !strings.Contains(s, "_AI returned no transcription._") {
		t.Fatalf("expected empty-OCR placeholder:\n%s", s)
	}
	if !strings.Contains(s, "_AI returned no summary._") {
		t.Fatalf("expected empty-summary placeholder:\n%s", s)
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
	})
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
	if calls != 1 {
		t.Errorf("expected ai.Provider.Process called exactly once across two identical uploads, got %d", calls)
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
	})
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
	if stored.AIStatus != state.AIStatusSuccess {
		t.Errorf("AIStatus = %q, want %q", stored.AIStatus, state.AIStatusSuccess)
	}
	if stored.AIRetryCount != 1 {
		t.Errorf("AIRetryCount = %d, want 1", stored.AIRetryCount)
	}
	if stored.AILastError != "" {
		t.Errorf("AILastError = %q, want empty string", stored.AILastError)
	}
	if stored.AILastRetryAt.IsZero() {
		t.Error("AILastRetryAt is zero, want a non-zero timestamp")
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
	})
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
	if !strings.Contains(string(body), "_AI failed: gemini 503: service unavailable_") {
		t.Errorf("error marker block missing from note:\n%s", string(body))
	}

	// New behaviour: AI status is persisted in the state record.
	stored, err := store.GetBySourcePath("Syncs/2026-07-01 failure.pdf")
	if err != nil {
		t.Fatalf("GetBySourcePath: %v", err)
	}
	if stored == nil {
		t.Fatal("no record stored")
	}
	if stored.AIStatus != state.AIStatusFailed {
		t.Errorf("AIStatus = %q, want %q", stored.AIStatus, state.AIStatusFailed)
	}
	if stored.AIRetryCount != 1 {
		t.Errorf("AIRetryCount = %d, want 1", stored.AIRetryCount)
	}
	if stored.AILastError == "" {
		t.Error("AILastError is empty, want error message")
	}
	if stored.AILastRetryAt.IsZero() {
		t.Error("AILastRetryAt is zero, want a non-zero timestamp")
	}
}

func TestPutMetadataOnlyPDFChangeSkipsAICall(t *testing.T) {
	vaultDir := t.TempDir()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
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
	})
	srv := &Server{cfg: cfg, imp: imp}

	bodies := [][]byte{
		[]byte("%PDF-1.7\n1 0 obj << /ModDate (D:20260720100000Z) >> stream\nstroke\nendstream endobj\ntrailer << /ID [<1111><2222>] >>\n%%EOF"),
		[]byte("%PDF-1.7\n1 0 obj << /ModDate (D:20260721100000Z) >> stream\nstroke\nendstream endobj\ntrailer << /ID [<aaaa><bbbb>] >>\n%%EOF"),
	}
	for n, body := range bodies {
		req := httptest.NewRequest("PUT", "/Syncs/2026-06-04%20stable.pdf", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 201 {
			t.Fatalf("attempt %d: status = %d body=%s", n, rec.Code, rec.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("expected metadata-only re-export to skip AI, got %d calls", calls)
	}
}

func TestPutActualPDFContentChangeCallsAIAgain(t *testing.T) {
	vaultDir := t.TempDir()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
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
	})
	srv := &Server{cfg: cfg, imp: imp}

	for n, stroke := range []string{"first stroke", "second stroke"} {
		body := []byte("%PDF-1.7\n1 0 obj << /ModDate (D:20260720100000Z) >> stream\n" + stroke + "\nendstream endobj\n%%EOF")
		req := httptest.NewRequest("PUT", "/Syncs/2026-06-04%20changed.pdf", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 201 {
			t.Fatalf("attempt %d: status = %d body=%s", n, rec.Code, rec.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("expected real content change to call AI again, got %d calls", calls)
	}
}
