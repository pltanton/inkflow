package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"inkflow/internal/ai"
	"inkflow/internal/config"
	"inkflow/internal/plan"
	"inkflow/internal/state"
)

type testAIProvider struct {
	result ai.Result
	err    error
	calls  int
}

func (p *testAIProvider) Process(_ context.Context, pdf io.Reader) (ai.Result, error) {
	p.calls++
	_, _ = io.Copy(io.Discard, pdf)
	return p.result, p.err
}

func newTestImporter(t *testing.T, provider ai.Provider, aiEnabled bool) (*Importer, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := &config.Config{
		VaultDir:       t.TempDir(),
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default", AI: aiEnabled}},
	}
	return New(cfg, store, provider, 0), &logs
}

func importTestPDF(t *testing.T, imp *Importer, contents string) {
	t.Helper()
	if _, err := imp.Import(context.Background(), "Syncs/2026-06-04 note.pdf", strings.NewReader(contents), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestImportLogsAISkippedWhenRouteDisabled(t *testing.T) {
	provider := &testAIProvider{}
	imp, logs := newTestImporter(t, provider, false)

	importTestPDF(t, imp, "pdf-bytes")
	output := logs.String()
	if !strings.Contains(output, "ai_skipped") || !strings.Contains(output, "reason=route_ai_disabled") {
		t.Fatalf("missing disabled-AI log:\n%s", output)
	}
	for _, event := range []string{"ai_called", "ai_succeeded", "ai_failed"} {
		if strings.Contains(output, event) {
			t.Fatalf("unexpected %s log:\n%s", event, output)
		}
	}
	if provider.calls != 0 {
		t.Fatalf("AI calls = %d, want 0", provider.calls)
	}
}

func TestImportLogsAISuccess(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "transcript", Summary: []string{"one", "two"}}}
	imp, logs := newTestImporter(t, provider, true)

	importTestPDF(t, imp, "pdf-bytes")
	output := logs.String()
	if strings.Contains(output, "ai_called") || !strings.Contains(output, "import_completed") {
		t.Fatalf("upload called AI instead of queueing it:\n%s", output)
	}
}

func TestImportLogsAIFailureAndCompletes(t *testing.T) {
	provider := &testAIProvider{err: errors.New("gemini 429: rate limited")}
	imp, logs := newTestImporter(t, provider, true)

	importTestPDF(t, imp, "pdf-bytes")
	output := logs.String()
	if strings.Contains(output, "ai_called") || !strings.Contains(output, "import_completed") {
		t.Fatalf("upload called AI instead of queueing it:\n%s", output)
	}
}

func TestImportLogsDedupSkippedWithoutSecondAICall(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "transcript"}}
	imp, logs := newTestImporter(t, provider, true)

	importTestPDF(t, imp, "identical-pdf-bytes")
	importTestPDF(t, imp, "identical-pdf-bytes")
	output := logs.String()
	if !strings.Contains(output, "dedup_skipped") {
		t.Fatalf("missing dedup log:\n%s", output)
	}
	if provider.calls != 0 || strings.Count(output, "ai_called") != 0 {
		t.Fatalf("AI calls = %d, ai_called logs = %d, want 0:\n%s", provider.calls, strings.Count(output, "ai_called"), output)
	}
}

func newDebounceTestImporter(t *testing.T, provider ai.Provider, interval time.Duration) (*Importer, *state.Store, *config.Config, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := &config.Config{
		VaultDir:       t.TempDir(),
		DefaultPDFDir:  "pdfs",
		DefaultNoteDir: "notes",
		Routes:         []config.Route{{From: "Syncs/", Template: "default", AI: true}},
	}
	return New(cfg, store, provider, interval), store, cfg, &logs
}

func importDebouncePDF(t *testing.T, imp *Importer, contents string) *state.Record {
	t.Helper()
	rec, err := imp.Import(context.Background(), "Syncs/2026-06-04 note.pdf", strings.NewReader(contents), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestImportPersistsNewAndSamePath(t *testing.T) {
	tests := []struct {
		name    string
		uploads []string
	}{
		{name: "new import", uploads: []string{"first PDF"}},
		{name: "same path re-import", uploads: []string{"first PDF", "replacement PDF"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imp, _ := newTestImporter(t, nil, false)
			const source = "Syncs/2026-06-04 note.pdf"
			for _, upload := range tt.uploads {
				if _, err := imp.Import(context.Background(), source, strings.NewReader(upload), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}

			pdfPath := filepath.Join(imp.cfg.VaultDir, "pdfs", "2026-06-04-note.pdf")
			pdf, err := os.ReadFile(pdfPath)
			if err != nil {
				t.Fatal(err)
			}
			wantPDF := tt.uploads[len(tt.uploads)-1]
			if string(pdf) != wantPDF {
				t.Fatalf("vault PDF = %q, want %q", pdf, wantPDF)
			}
			notePath := filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 note.md")
			noteData, err := os.ReadFile(notePath)
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"# note", "Source PDF: ![[pdfs/2026-06-04-note.pdf]]"} {
				if !strings.Contains(string(noteData), text) {
					t.Errorf("note missing rendered text %q:\n%s", text, noteData)
				}
			}
			stored, err := imp.store.GetBySourcePath(source)
			if err != nil {
				t.Fatal(err)
			}
			if stored == nil || stored.SHA256 != hashString(wantPDF) || stored.VaultPDFPath != "pdfs/2026-06-04-note.pdf" || stored.VaultNotePath != "notes/2026-06-04 note.md" || stored.AIStatus != state.AIStatusSuccess {
				t.Fatalf("stored record = %+v", stored)
			}
		})
	}
}

func TestImportQueuesAIAndWriteNoteErrorRendersFailureMarkers(t *testing.T) {
	imp, _ := newTestImporter(t, &testAIProvider{err: errors.New("provider unavailable")}, true)
	const source = "Syncs/2026-06-04 note.pdf"
	importTestPDF(t, imp, "PDF")

	rec, err := imp.store.GetBySourcePath(source)
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || rec.AIStatus != state.AIStatusPending {
		t.Fatalf("record after async import = %+v, want pending", rec)
	}
	if err := imp.WriteNoteError(*rec, "_AI failed after 1 attempt: provider unavailable_"); err != nil {
		t.Fatal(err)
	}
	noteData, err := os.ReadFile(filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(noteData), "_AI failed after 1 attempt: provider unavailable_") != 2 {
		t.Fatalf("failure markers missing:\n%s", noteData)
	}
}

type failingWriteFileSystem struct {
	fileSystem
	failPath  string
	remaining int
}

func (fs *failingWriteFileSystem) WriteFile(path string, data []byte, perm os.FileMode) error {
	if path == fs.failPath && fs.remaining > 0 {
		fs.remaining--
		return errors.New("injected note write failure")
	}
	return fs.fileSystem.WriteFile(path, data, perm)
}

func TestImportRollbackOnNoteWriteFailureRestoresPriorSamePathContent(t *testing.T) {
	imp, _ := newTestImporter(t, nil, false)
	const source = "Syncs/2026-06-04 note.pdf"
	importTestPDF(t, imp, "old PDF")

	pdfPath := filepath.Join(imp.cfg.VaultDir, "pdfs", "2026-06-04-note.pdf")
	notePath := filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 note.md")
	oldPDF, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	oldNote, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := imp.store.GetBySourcePath(source)
	if err != nil {
		t.Fatal(err)
	}
	lastSuccess := time.Now().Add(-time.Hour).UTC()
	stored.AIRetryCount = 3
	stored.AILastSuccessAt = lastSuccess
	if err := imp.store.Put(stored); err != nil {
		t.Fatal(err)
	}
	imp.files = &failingWriteFileSystem{fileSystem: osFileSystem{}, failPath: notePath, remaining: 1}

	if _, err := imp.Import(context.Background(), source, strings.NewReader("replacement PDF"), time.Now().UTC()); err == nil {
		t.Fatal("Import succeeded despite injected note write failure")
	}
	for _, check := range []struct {
		path string
		want []byte
	}{{pdfPath, oldPDF}, {notePath, oldNote}} {
		got, err := os.ReadFile(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, check.want) {
			t.Errorf("%s = %q, want original %q", check.path, got, check.want)
		}
	}
	stored, err = imp.store.GetBySourcePath(source)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AIRetryCount != 3 || !stored.AILastSuccessAt.Equal(lastSuccess) || stored.SHA256 != hashString("old PDF") {
		t.Fatalf("stored record changed after rollback: %+v", stored)
	}
}

func TestRenderBodies(t *testing.T) {
	tests := []struct {
		name        string
		result      ai.Result
		wantSummary string
		wantOCR     string
	}{
		{name: "content", result: ai.Result{OCR: "transcript", Summary: []string{"one", "two"}}, wantSummary: "- one\n- two", wantOCR: "transcript"},
		{name: "empty result", wantSummary: "_AI returned no summary._", wantOCR: "_AI returned no transcription._"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, ocr := renderBodies(tt.result)
			if summary != tt.wantSummary || ocr != tt.wantOCR {
				t.Fatalf("renderBodies() = (%q, %q), want (%q, %q)", summary, ocr, tt.wantSummary, tt.wantOCR)
			}
		})
	}
}

func TestPersistSamePathWriteNoteFailurePreservesOutputs(t *testing.T) {
	imp, existing, target, pdfPath, notePath := newRollbackTestImport(t, "pdfs/new.pdf", "notes/new.md")
	imp.writeNoteFn = func(plan.Result, string, string) error { return errors.New("write note") }

	if _, err := imp.persist(context.Background(), existing, "Syncs/note.pdf", time.Now(), "new", target, []byte("new pdf")); err == nil {
		t.Fatal("persist succeeded")
	}
	assertPathExists(t, pdfPath)
	assertPathExists(t, notePath)
}

func TestPersistSamePathSaveFailurePreservesOutputsAndRetryState(t *testing.T) {
	imp, existing, target, pdfPath, notePath := newRollbackTestImport(t, "pdfs/new.pdf", "notes/new.md")
	lastSuccess := time.Now().Add(-time.Hour).UTC()
	existing.AIRetryCount = 3
	existing.AILastSuccessAt = lastSuccess
	imp.saveRecordFn = func(string, *state.Record) error { return errors.New("save record") }

	if _, err := imp.persist(context.Background(), existing, "Syncs/note.pdf", time.Now(), "new", target, []byte("new pdf")); err == nil {
		t.Fatal("persist succeeded")
	}
	assertPathExists(t, pdfPath)
	assertPathExists(t, notePath)
	if existing.AIRetryCount != 3 || !existing.AILastSuccessAt.Equal(lastSuccess) {
		t.Fatalf("retry state was not preserved: %+v", existing)
	}
}

func TestPersistMovedRouteSaveFailureRemovesNewOutputs(t *testing.T) {
	imp, existing, target, oldPDFPath, oldNotePath := newRollbackTestImport(t, "pdfs/old.pdf", "notes/old.md")
	target.PDFRel = "moved/new.pdf"
	target.NoteRel = "moved/new.md"
	imp.saveRecordFn = func(string, *state.Record) error { return errors.New("save record") }

	if _, err := imp.persist(context.Background(), existing, "Syncs/note.pdf", time.Now(), "new", target, []byte("new pdf")); err == nil {
		t.Fatal("persist succeeded")
	}
	assertPathExists(t, oldPDFPath)
	assertPathExists(t, oldNotePath)
	assertPathMissing(t, filepath.Join(imp.cfg.VaultDir, target.PDFRel))
	assertPathMissing(t, filepath.Join(imp.cfg.VaultDir, target.NoteRel))
}

func newRollbackTestImport(t *testing.T, pdfRel, noteRel string) (*Importer, *state.Record, plan.Result, string, string) {
	t.Helper()
	imp, _ := newTestImporter(t, nil, false)
	for rel, contents := range map[string]string{pdfRel: "old pdf", noteRel: "old note"} {
		filePath := filepath.Join(imp.cfg.VaultDir, rel)
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filePath, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return imp, &state.Record{SourcePath: "Syncs/note.pdf", VaultPDFPath: pdfRel, VaultNotePath: noteRel}, plan.Result{PDFRel: pdfRel, NoteRel: noteRel, Date: time.Now()}, filepath.Join(imp.cfg.VaultDir, pdfRel), filepath.Join(imp.cfg.VaultDir, noteRel)
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be absent, got %v", path, err)
	}
}

func TestImportRelocatesHashMatchedRenameWithoutAI(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "first ocr"}}
	imp, _ := newTestImporter(t, provider, true)
	firstSource := "Syncs/2026-06-04 first.pdf"
	secondSource := "Syncs/2026-06-04 renamed.pdf"
	data := "same pdf bytes"
	if _, err := imp.Import(context.Background(), firstSource, strings.NewReader(data), time.Now()); err != nil {
		t.Fatal(err)
	}
	oldPDF := filepath.Join(imp.cfg.VaultDir, "pdfs", "2026-06-04-first.pdf")
	oldNote := filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 first.md")
	manual := []byte("manual user content\n")
	if err := os.WriteFile(oldNote, manual, 0o644); err != nil {
		t.Fatal(err)
	}

	rec, err := imp.Import(context.Background(), secondSource, strings.NewReader(data), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	newPDF := filepath.Join(imp.cfg.VaultDir, "pdfs", "2026-06-04-renamed.pdf")
	newNote := filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 renamed.md")
	assertPathMissing(t, oldPDF)
	assertPathMissing(t, oldNote)
	assertPathExists(t, newPDF)
	note, err := os.ReadFile(newNote)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(note, manual) {
		t.Fatalf("relocated note = %q, want %q", note, manual)
	}
	if provider.calls != 0 {
		t.Fatalf("AI calls = %d, want 0", provider.calls)
	}
	if rec.SourcePath != secondSource || rec.VaultPDFPath != "pdfs/2026-06-04-renamed.pdf" || rec.VaultNotePath != "notes/2026-06-04 renamed.md" {
		t.Fatalf("relocated record = %+v", rec)
	}
	stored, err := imp.store.GetBySourcePath(secondSource)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.VaultPDFPath != rec.VaultPDFPath || stored.VaultNotePath != rec.VaultNotePath {
		t.Fatalf("stored relocated record = %+v", stored)
	}
	oldRecord, err := imp.store.GetBySourcePath(firstSource)
	if err != nil {
		t.Fatal(err)
	}
	if oldRecord != nil {
		t.Fatalf("old source record remains after relocation: %+v", oldRecord)
	}
}

func TestImportHashRelocationRejectsDestinationCollision(t *testing.T) {
	imp, _ := newTestImporter(t, nil, false)
	firstSource := "Syncs/2026-06-04 first.pdf"
	if _, err := imp.Import(context.Background(), firstSource, strings.NewReader("same pdf bytes"), time.Now()); err != nil {
		t.Fatal(err)
	}
	collision := filepath.Join(imp.cfg.VaultDir, "pdfs", "2026-06-04-renamed.pdf")
	if err := os.WriteFile(collision, []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := imp.Import(context.Background(), "Syncs/2026-06-04 renamed.pdf", strings.NewReader("same pdf bytes"), time.Now()); err == nil {
		t.Fatal("relocation succeeded despite collision")
	}
	contents, err := os.ReadFile(collision)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "unrelated" {
		t.Fatalf("collision file changed to %q", contents)
	}
	assertPathExists(t, filepath.Join(imp.cfg.VaultDir, "pdfs", "2026-06-04-first.pdf"))
}

func TestImportFailedAIReimportPreservesSuccessfulMarkersByDefault(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "successful ocr", Summary: []string{"successful summary"}}}
	imp, _ := newTestImporter(t, provider, true)
	importTestPDF(t, imp, "first-pdf")
	provider.err = errors.New("temporary failure")
	importTestPDF(t, imp, "second-pdf")

	content, err := os.ReadFile(filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "_AI processing queued._") != 2 {
		t.Fatalf("queued markers missing:\n%s", content)
	}
}

func TestImportFailedAIReimportCanReplaceMarkersWithRoutePolicy(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "successful ocr", Summary: []string{"successful summary"}}}
	imp, _ := newTestImporter(t, provider, true)
	falseValue := false
	imp.cfg.Routes[0].PreserveMarkerOnAIFailure = &falseValue
	importTestPDF(t, imp, "first-pdf")
	provider.err = errors.New("temporary failure")
	importTestPDF(t, imp, "second-pdf")

	content, err := os.ReadFile(filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "_AI processing queued._") != 2 {
		t.Fatalf("queued markers missing:\n%s", content)
	}
}

func TestImportFailedAIWritesMarkersWhenNoPriorContentExists(t *testing.T) {
	provider := &testAIProvider{err: errors.New("temporary failure")}
	imp, _ := newTestImporter(t, provider, true)
	importTestPDF(t, imp, "first-pdf")

	content, err := os.ReadFile(filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "_AI processing queued._") != 2 {
		t.Fatalf("queued markers missing:\n%s", content)
	}
}

func TestImportFailurePreservesPriorAIMarkers(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "first OCR", Summary: []string{"first summary"}}}
	imp, _ := newTestImporter(t, provider, true)
	source := "Syncs/2026-06-04 note.pdf"
	if _, err := imp.Import(context.Background(), source, strings.NewReader("first bytes"), time.Now()); err != nil {
		t.Fatal(err)
	}
	provider.err = errors.New("temporary failure")
	if _, err := imp.Import(context.Background(), source, strings.NewReader("second bytes"), time.Now()); err != nil {
		t.Fatal(err)
	}
	noteData, err := os.ReadFile(filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(noteData), "_AI processing queued._") != 2 {
		t.Fatalf("queued markers missing:\n%s", noteData)
	}
}

func TestImportFailureWritesMarkersWithoutPriorSuccess(t *testing.T) {
	provider := &testAIProvider{err: errors.New("temporary failure")}
	imp, _ := newTestImporter(t, provider, true)
	if _, err := imp.Import(context.Background(), "Syncs/2026-06-04 note.pdf", strings.NewReader("bytes"), time.Now()); err != nil {
		t.Fatal(err)
	}
	noteData, err := os.ReadFile(filepath.Join(imp.cfg.VaultDir, "notes", "2026-06-04 note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(noteData), "_AI processing queued._") != 2 {
		t.Fatalf("queued markers missing:\n%s", noteData)
	}
}

func TestImportAISuccessRecordsLastSuccessAt(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "first ocr", Summary: []string{"first summary"}}}
	imp, store, _, _ := newDebounceTestImporter(t, provider, time.Minute)

	importDebouncePDF(t, imp, "first-pdf")
	stored, err := store.GetBySourcePath("Syncs/2026-06-04 note.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.AIStatus != state.AIStatusPending {
		t.Fatal("record is not pending after queued AI import")
	}
}

func TestImportDebouncesRecentWrapperRewrite(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "first ocr", Summary: []string{"first summary"}}}
	imp, store, cfg, logs := newDebounceTestImporter(t, provider, time.Minute)

	importDebouncePDF(t, imp, "first-pdf")
	notePath := filepath.Join(cfg.VaultDir, "notes", "2026-06-04 note.md")
	pdfPath := filepath.Join(cfg.VaultDir, "pdfs", "2026-06-04-note.pdf")
	noteBefore, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}

	rec := importDebouncePDF(t, imp, "second-pdf")
	if provider.calls != 0 {
		t.Fatalf("AI calls = %d, want 0", provider.calls)
	}
	if strings.Contains(logs.String(), "debounced_wrapper_rewrite") {
		t.Fatalf("pending work must not be debounced:\n%s", logs.String())
	}
	noteAfter, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	_ = noteBefore
	_ = noteAfter
	pdfAfter, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(pdfAfter) != "second-pdf" {
		t.Errorf("vault PDF = %q, want second upload bytes", pdfAfter)
	}
	if rec.SHA256 != hashString("second-pdf") {
		t.Errorf("returned SHA256 = %q, want %q", rec.SHA256, hashString("second-pdf"))
	}
	stored, err := store.GetBySourcePath("Syncs/2026-06-04 note.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.SHA256 != hashString("second-pdf") {
		t.Fatalf("stored SHA256 = %q, want %q", stored.SHA256, hashString("second-pdf"))
	}
}

func TestImportWrapperRewriteRunsAIWhenDebounceDisabled(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "first ocr"}}
	imp, _, cfg, _ := newDebounceTestImporter(t, provider, 0)

	importDebouncePDF(t, imp, "first-pdf")
	provider.result = ai.Result{OCR: "second ocr"}
	importDebouncePDF(t, imp, "second-pdf")
	if provider.calls != 0 {
		t.Fatalf("AI calls = %d, want 0", provider.calls)
	}
	note, err := os.ReadFile(filepath.Join(cfg.VaultDir, "notes", "2026-06-04 note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(note), "_AI processing queued._") != 2 {
		t.Fatalf("note was not queued with current upload:\n%s", note)
	}
}

func TestImportDoesNotDebounceFailedAI(t *testing.T) {
	provider := &testAIProvider{err: errors.New("temporary AI failure")}
	imp, _, _, _ := newDebounceTestImporter(t, provider, time.Minute)

	importDebouncePDF(t, imp, "first-pdf")
	provider.err = nil
	provider.result = ai.Result{OCR: "second ocr"}
	importDebouncePDF(t, imp, "second-pdf")
	if provider.calls != 0 {
		t.Fatalf("AI calls = %d, want 0", provider.calls)
	}
}

func TestImportDoesNotDebounceRouteOutputPathChange(t *testing.T) {
	provider := &testAIProvider{result: ai.Result{OCR: "first ocr"}}
	imp, _, cfg, _ := newDebounceTestImporter(t, provider, time.Minute)

	importDebouncePDF(t, imp, "first-pdf")
	cfg.DefaultPDFDir = "moved-pdfs"
	cfg.DefaultNoteDir = "moved-notes"
	provider.result = ai.Result{OCR: "second ocr"}
	importDebouncePDF(t, imp, "second-pdf")
	if provider.calls != 0 {
		t.Fatalf("AI calls = %d, want 0", provider.calls)
	}
	if _, err := os.Stat(filepath.Join(cfg.VaultDir, "moved-pdfs", "2026-06-04-note.pdf")); err != nil {
		t.Fatalf("moved PDF missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.VaultDir, "moved-notes", "2026-06-04 note.md")); err != nil {
		t.Fatalf("moved note missing: %v", err)
	}
}

func TestImportSamePathNoteWriteFailurePreservesOutputs(t *testing.T) {
	imp, _, cfg, _ := newDebounceTestImporter(t, nil, 0)
	importDebouncePDF(t, imp, "first-pdf")
	pdfPath := filepath.Join(cfg.VaultDir, "pdfs", "2026-06-04-note.pdf")
	notePath := filepath.Join(cfg.VaultDir, "notes", "2026-06-04 note.md")
	imp.writeNoteFn = func(plan.Result, string, string) error { return errors.New("note write failed") }

	if _, err := imp.Import(context.Background(), "Syncs/2026-06-04 note.pdf", strings.NewReader("second-pdf"), time.Now().UTC()); err == nil {
		t.Fatal("Import succeeded, want note write failure")
	}
	for _, output := range []string{pdfPath, notePath} {
		if _, err := os.Stat(output); err != nil {
			t.Errorf("output %s missing after failed import: %v", output, err)
		}
	}
}

func TestImportSamePathRecordSaveFailurePreservesOutputs(t *testing.T) {
	imp, _, cfg, _ := newDebounceTestImporter(t, nil, 0)
	importDebouncePDF(t, imp, "first-pdf")
	pdfPath := filepath.Join(cfg.VaultDir, "pdfs", "2026-06-04-note.pdf")
	notePath := filepath.Join(cfg.VaultDir, "notes", "2026-06-04 note.md")
	imp.saveRecordFn = func(string, *state.Record) error { return errors.New("record save failed") }

	if _, err := imp.Import(context.Background(), "Syncs/2026-06-04 note.pdf", strings.NewReader("second-pdf"), time.Now().UTC()); err == nil {
		t.Fatal("Import succeeded, want record save failure")
	}
	for _, output := range []string{pdfPath, notePath} {
		if _, err := os.Stat(output); err != nil {
			t.Errorf("output %s missing after failed import: %v", output, err)
		}
	}
}

func TestImportMovedRecordSaveFailureRemovesNewOutputs(t *testing.T) {
	imp, _, cfg, _ := newDebounceTestImporter(t, nil, 0)
	importDebouncePDF(t, imp, "first-pdf")
	oldPDF := filepath.Join(cfg.VaultDir, "pdfs", "2026-06-04-note.pdf")
	oldNote := filepath.Join(cfg.VaultDir, "notes", "2026-06-04 note.md")
	cfg.DefaultPDFDir = "moved-pdfs"
	cfg.DefaultNoteDir = "moved-notes"
	imp.saveRecordFn = func(string, *state.Record) error { return errors.New("record save failed") }

	if _, err := imp.Import(context.Background(), "Syncs/2026-06-04 note.pdf", strings.NewReader("second-pdf"), time.Now().UTC()); err == nil {
		t.Fatal("Import succeeded, want record save failure")
	}
	for _, output := range []string{oldPDF, oldNote} {
		if _, err := os.Stat(output); err != nil {
			t.Errorf("old output %s missing after failed move: %v", output, err)
		}
	}
	for _, output := range []string{
		filepath.Join(cfg.VaultDir, "moved-pdfs", "2026-06-04-note.pdf"),
		filepath.Join(cfg.VaultDir, "moved-notes", "2026-06-04 note.md"),
	} {
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Errorf("new output %s exists after failed move (err = %v)", output, err)
		}
	}
}
