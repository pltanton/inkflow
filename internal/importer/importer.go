package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"inkflow/internal/ai"
	"inkflow/internal/config"
	"inkflow/internal/frontmatter"
	"inkflow/internal/note"
	"inkflow/internal/plan"
	"inkflow/internal/state"
)

type Importer struct {
	cfg   *config.Config
	store *state.Store
	ai    ai.Provider
}

func New(cfg *config.Config, store *state.Store, aiProvider ai.Provider) *Importer {
	return &Importer{cfg: cfg, store: store, ai: aiProvider}
}

func (i *Importer) Import(ctx context.Context, input string, reader io.Reader, modTime time.Time) (*state.Record, error) {
	if !strings.EqualFold(path.Ext(input), ".pdf") {
		return nil, fmt.Errorf("not a pdf: %s", input)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	shaSum := sha256.Sum256(data)
	sha := hex.EncodeToString(shaSum[:])

	t, err := plan.Build(i.cfg.Routes, i.cfg, input, modTime)
	if err != nil {
		slog.Default().Debug("route_not_matched", "source", input, "err", err)
		return nil, err
	}
	slog.Default().Debug("route_matched", "source", input, "pdf_rel", t.PDFRel, "note_rel", t.NoteRel, "template", t.Template, "ai", t.AI)
	slog.Default().Debug("filename_parsed", "source", input, "date", t.Date.Format("2006-01-02"), "title", t.Title, "tags", t.Tags)
	existing, err := i.lookupRecord(input, sha)
	if err != nil {
		return nil, err
	}
	samePaths := existing != nil && existing.VaultPDFPath == t.PDFRel && existing.VaultNotePath == t.NoteRel
	slog.Default().Debug("dedup_check", "source", input, "sha256", sha, "record_found", existing != nil, "same_paths", samePaths)
	// Dedup: if we already imported the exact same bytes into the same vault
	// paths, there's nothing useful to redo — the PDF on disk is identical,
	// the marker blocks already hold the previous AI output, and a re-run
	// would only burn Gemini tokens and produce visible flicker. A route
	// change (different PDFRel/NoteRel) bypasses dedup so files land in the
	// new location.
	if existing != nil && existing.SHA256 == sha &&
		existing.VaultPDFPath == t.PDFRel && existing.VaultNotePath == t.NoteRel {
		slog.Default().Info("dedup_skipped", "source", input, "sha256", sha, "note", t.NoteRel, "pdf", t.PDFRel)
		return existing, nil
	}
	return i.persist(ctx, existing, input, modTime, sha, t, data)
}

func (i *Importer) lookupRecord(sourcePath, sha string) (*state.Record, error) {
	if old, err := i.store.GetBySourcePath(sourcePath); err != nil {
		return nil, err
	} else if old != nil && old.SHA256 == sha {
		return old, nil
	}

	old, err := i.store.GetByHash(sha)
	if err != nil || old == nil {
		return old, err
	}
	if old.SourcePath != sourcePath {
		prevPath := old.SourcePath
		old.SourcePath = sourcePath
		old.ImportedAt = time.Now().UTC()
		if err := i.store.Save(prevPath, old); err != nil {
			return nil, err
		}
	}
	return old, nil
}

func (i *Importer) persist(ctx context.Context, existing *state.Record, sourcePath string, modTime time.Time, sha string, t plan.Result, pdfData []byte) (*state.Record, error) {
	pdfAbs := filepath.Join(i.cfg.VaultDir, filepath.FromSlash(t.PDFRel))
	noteAbs := filepath.Join(i.cfg.VaultDir, filepath.FromSlash(t.NoteRel))
	if err := os.MkdirAll(filepath.Dir(pdfAbs), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(noteAbs), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(pdfAbs, pdfData, 0o644); err != nil {
		return nil, err
	}
	slog.Default().Debug("pdf_written", "path", pdfAbs, "bytes", len(pdfData))
	rec := &state.Record{
		SourcePath:    sourcePath,
		SHA256:        sha,
		SourceModTime: modTime,
		SourceSize:    int64(len(pdfData)),
		VaultPDFPath:  t.PDFRel,
		VaultNotePath: t.NoteRel,
		ImportedAt:    time.Now().UTC(),
	}
	previousSourcePath := ""
	previousPDFPath := ""
	previousNotePath := ""
	if existing != nil {
		previousSourcePath = existing.SourcePath
		previousPDFPath = existing.VaultPDFPath
		previousNotePath = existing.VaultNotePath
		*existing = *rec
		rec = existing
	}

	var summaryBody, ocrBody string
	if t.AI && i.ai != nil {
		slog.Default().Debug("ai_queued", "source", sourcePath, "route_ai_enabled", true)
		slog.Default().Debug("ai_call_start", "source", sourcePath)
		res, err := i.ai.Process(ctx, bytes.NewReader(pdfData))
		if err != nil {
			slog.Default().Debug("ai_call_failed", "source", sourcePath, "err", err)
			msg := fmt.Sprintf("_AI failed: %s_", err.Error())
			summaryBody, ocrBody = msg, msg
			rec.AIStatus = state.AIStatusFailed
			rec.AIRetryCount++
			rec.AILastError = err.Error()
			rec.AILastRetryAt = time.Now().UTC()
		} else {
			slog.Default().Debug("ai_call_success", "source", sourcePath, "ocr_len", len(res.OCR), "summary_bullets", len(res.Summary))
			if res.OCR != "" {
				ocrBody = res.OCR
			} else {
				ocrBody = "_AI returned no transcription._"
			}
			if len(res.Summary) > 0 {
				summaryBody = "- " + strings.Join(res.Summary, "\n- ")
			} else {
				summaryBody = "_AI returned no summary._"
			}
			rec.AIStatus = state.AIStatusSuccess
			rec.AIRetryCount++
			rec.AILastError = ""
			rec.AILastRetryAt = time.Now().UTC()
		}
	} else {
		slog.Default().Debug("ai_skipped", "source", sourcePath, "reason", "route_ai_disabled_or_provider_unavailable")
	}

	if err := i.writeNote(t, summaryBody, ocrBody); err != nil {
		removeIfDistinct(previousPDFPath, pdfAbs)
		removeIfDistinct(previousNotePath, noteAbs)
		return nil, err
	}
	if err := i.saveRecord(previousSourcePath, rec); err != nil {
		removeIfDistinct(previousPDFPath, pdfAbs)
		removeIfDistinct(previousNotePath, noteAbs)
		return nil, err
	}
	if previousPDFPath != "" && previousPDFPath != rec.VaultPDFPath {
		_ = os.Remove(filepath.Join(i.cfg.VaultDir, filepath.FromSlash(previousPDFPath)))
	}
	if previousNotePath != "" && previousNotePath != rec.VaultNotePath {
		_ = os.Remove(filepath.Join(i.cfg.VaultDir, filepath.FromSlash(previousNotePath)))
	}
	logImported(sourcePath, t.NoteRel, t.PDFRel)
	return rec, nil
}

func (i *Importer) saveRecord(previousSourcePath string, rec *state.Record) error {
	if previousSourcePath == "" {
		return i.store.Put(rec)
	}
	return i.store.Save(previousSourcePath, rec)
}

func (i *Importer) writeNote(t plan.Result, summaryBody, ocrBody string) error {
	noteAbs := filepath.Join(i.cfg.VaultDir, filepath.FromSlash(t.NoteRel))
	if err := os.MkdirAll(filepath.Dir(noteAbs), 0o755); err != nil {
		return err
	}
	var content string
	if existing, err := os.ReadFile(noteAbs); err == nil {
		content = frontmatter.UpdateTags(string(existing), t.Tags)
	} else if !os.IsNotExist(err) {
		return err
	} else {
		slog.Default().Debug("template_render_start", "template_dir", i.cfg.TemplateDir, "template", t.Template)
		body, err := plan.RenderNoteBody(i.cfg.TemplateDir, plan.NoteData{
			Date:       t.Date.Format("2006-01-02"),
			Title:      t.Title,
			PDFRelPath: t.PDFRel,
			Template:   t.Template,
			Tags:       t.Tags,
		})
		if err != nil {
			return err
		}
		slog.Default().Debug("template_rendered", "template", t.Template, "bytes", len(body))
		content = body
	}
	content = note.UpsertMarkerBlock(content, "Summary", "summary", summaryBody)
	content = note.UpsertMarkerBlock(content, "OCR", "ocr", ocrBody)
	return os.WriteFile(noteAbs, []byte(content), 0o644)
}

func removeIfDistinct(oldPath, newPath string) {
	if oldPath == "" || oldPath != newPath {
		_ = os.Remove(newPath)
	}
}

func logImported(sourcePath, notePath, pdfPath string) {
	if logger := slog.Default(); logger != nil {
		logger.Info("imported", "source", sourcePath, "note", notePath, "pdf", pdfPath)
	}
}

// RetryAI re-runs the AI processing step for an already-imported record.
// It reads the PDF from record.VaultPDFPath, calls the AI provider, and on
// success rewrites the note marker blocks and saves the updated record.
// On any failure it returns an error without modifying the note or the store —
// the caller is responsible for updating the record state.
func (i *Importer) RetryAI(ctx context.Context, rec state.Record) error {
	if i.ai == nil {
		return fmt.Errorf("retry AI: no AI provider configured")
	}

	pdfAbs := filepath.Join(i.cfg.VaultDir, filepath.FromSlash(rec.VaultPDFPath))
	pdfData, err := os.ReadFile(pdfAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("retry AI: vault PDF not found: %s", pdfAbs)
		}
		return fmt.Errorf("retry AI: read vault PDF: %w", err)
	}

	res, err := i.ai.Process(ctx, bytes.NewReader(pdfData))
	if err != nil {
		return err
	}

	var summaryBody, ocrBody string
	if res.OCR != "" {
		ocrBody = res.OCR
	} else {
		ocrBody = "_AI returned no transcription._"
	}
	if len(res.Summary) > 0 {
		summaryBody = "- " + strings.Join(res.Summary, "\n- ")
	} else {
		summaryBody = "_AI returned no summary._"
	}

	noteAbs := filepath.Join(i.cfg.VaultDir, filepath.FromSlash(rec.VaultNotePath))
	existing, err := os.ReadFile(noteAbs)
	if err != nil {
		return fmt.Errorf("retry AI: read vault note: %w", err)
	}
	content := note.UpsertMarkerBlock(string(existing), "Summary", "summary", summaryBody)
	content = note.UpsertMarkerBlock(content, "OCR", "ocr", ocrBody)
	if err := os.WriteFile(noteAbs, []byte(content), 0o644); err != nil {
		return fmt.Errorf("retry AI: write vault note: %w", err)
	}

	rec.AIStatus = state.AIStatusSuccess
	rec.AIRetryCount++
	rec.AILastError = ""
	rec.AILastRetryAt = time.Now().UTC()
	if err := i.store.Put(&rec); err != nil {
		return fmt.Errorf("retry AI: save record: %w", err)
	}
	slog.Info("retry AI: success", "source", rec.SourcePath)
	return nil
}

// WriteNoteError writes an error message into the Summary and OCR marker
// blocks of the note associated with rec. It is called by the retry
// scheduler when retries are exhausted or a non-retryable error occurs.
func (i *Importer) WriteNoteError(rec state.Record, msg string) error {
	noteAbs := filepath.Join(i.cfg.VaultDir, filepath.FromSlash(rec.VaultNotePath))
	existing, err := os.ReadFile(noteAbs)
	if err != nil {
		return fmt.Errorf("write note error: read %s: %w", noteAbs, err)
	}
	content := note.UpsertMarkerBlock(string(existing), "Summary", "summary", msg)
	content = note.UpsertMarkerBlock(content, "OCR", "ocr", msg)
	return os.WriteFile(noteAbs, []byte(content), 0o644)
}
