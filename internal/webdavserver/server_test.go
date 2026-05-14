package webdavserver

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
	imp := importer.New(cfg, store)
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
