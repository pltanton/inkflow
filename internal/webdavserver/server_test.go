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
