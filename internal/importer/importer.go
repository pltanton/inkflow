package importer

import (
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

	"inkflow/internal/config"
	"inkflow/internal/frontmatter"
	"inkflow/internal/plan"
	"inkflow/internal/state"
)

type Importer struct {
	cfg   *config.Config
	store *state.Store
}

func New(cfg *config.Config, store *state.Store) *Importer {
	return &Importer{cfg: cfg, store: store}
}

func (i *Importer) Import(input string, reader io.Reader, modTime time.Time) (*state.Record, error) {
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
		return nil, err
	}
	existing, err := i.lookupRecord(input, sha)
	if err != nil {
		return nil, err
	}
	return i.persist(existing, input, modTime, sha, t, data)
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

func (i *Importer) persist(existing *state.Record, sourcePath string, modTime time.Time, sha string, t plan.Result, pdfData []byte) (*state.Record, error) {
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
	if err := i.writeNote(t); err != nil {
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

func (i *Importer) writeNote(t plan.Result) error {
	noteAbs := filepath.Join(i.cfg.VaultDir, filepath.FromSlash(t.NoteRel))
	if err := os.MkdirAll(filepath.Dir(noteAbs), 0o755); err != nil {
		return err
	}
	if content, err := os.ReadFile(noteAbs); err == nil {
		return os.WriteFile(noteAbs, []byte(frontmatter.UpdateTags(string(content), t.Tags)), 0o644)
	} else if !os.IsNotExist(err) {
		return err
	}
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
	return os.WriteFile(noteAbs, []byte(body), 0o644)
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
