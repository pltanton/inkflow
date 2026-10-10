package webdavserver

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"inkflow/internal/ai"
	"inkflow/internal/caldav"
	"inkflow/internal/config"
	"inkflow/internal/importer"
	"inkflow/internal/state"
)

type fakeTaskSink struct {
	pushed []caldav.NoteTasks
	err    error
}

func (f *fakeTaskSink) PushNote(ctx context.Context, n caldav.NoteTasks) error {
	f.pushed = append(f.pushed, n)
	return f.err
}

func importWithAI(t *testing.T, template, upload string, res ai.Result, sink *fakeTaskSink) string {
	t.Helper()
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
		Routes:         []config.Route{{From: "Notes/", Template: template, AI: true}},
	}
	imp := importer.New(cfg, store, fakeAIClient{result: res})
	if sink != nil {
		imp.SetTaskSink(sink)
	}
	srv := &Server{cfg: cfg, imp: imp}

	req := httptest.NewRequest("PUT", "/Notes/"+upload, bytes.NewReader([]byte("pdf-bytes")))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	matches, err := filepath.Glob(filepath.Join(vaultDir, "notes", "*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one note, got %v (%v)", matches, err)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestPutRendersOwnerTasksAndAgentHighlights(t *testing.T) {
	sink := &fakeTaskSink{}
	s := importWithAI(t, "1-1", "2026-10-09%20Iaroslav.pdf", ai.Result{
		OCR:     "- надо заполнить IPF\n- ==Спросить== Клода про сверку с CH\n  - ==собрать метрики==",
		Summary: []string{"bullet"},
		MyTasks: []string{"Заполнить IPF", "Подготовить roadmap"},
	}, sink)

	if !strings.Contains(s, "## Action items\n- [ ] Заполнить IPF\n- [ ] Подготовить roadmap\n") {
		t.Fatalf("owner tasks not rendered into Action items:\n%s", s)
	}
	if !strings.Contains(s, "## Agent\n\n<!-- inkflow:agent:start -->\n- [ ] #agent Спросить Клода про сверку с CH\n- [ ] #agent собрать метрики\n<!-- inkflow:agent:end -->") {
		t.Fatalf("agent section missing:\n%s", s)
	}
	if !strings.Contains(s, "\nperson: Iaroslav\n") || !strings.Contains(s, "\nstatus: inbox\n") {
		t.Fatalf("unexpected frontmatter:\n%s", s)
	}
	if len(sink.pushed) != 1 {
		t.Fatalf("expected one caldav push, got %d", len(sink.pushed))
	}
	got := sink.pushed[0]
	if got.NotePath != "notes/2026-10-09 Iaroslav.md" || got.Template != "1-1" ||
		len(got.Mine) != 2 || len(got.Agent) != 2 {
		t.Fatalf("unexpected push: %+v", got)
	}
}

func TestPutWithoutTasksMarksNoteDone(t *testing.T) {
	sink := &fakeTaskSink{}
	s := importWithAI(t, "sync", "2026-10-07%20TP.pdf", ai.Result{
		OCR:     "### Дима\n- фиксит daedalus",
		Summary: []string{"bullet"},
	}, sink)

	if !strings.Contains(s, "\nstatus: done\n") {
		t.Fatalf("expected status done:\n%s", s)
	}
	if !strings.Contains(s, "## TODO\n\n- [ ]\n") {
		t.Fatalf("expected placeholder kept:\n%s", s)
	}
	if strings.Contains(s, "## Agent") {
		t.Fatalf("agent section must be omitted:\n%s", s)
	}
	if len(sink.pushed) != 0 {
		t.Fatalf("nothing to push, got %+v", sink.pushed)
	}
}

func TestPutWithOnlyHighlightsStaysInbox(t *testing.T) {
	s := importWithAI(t, "sync", "2026-10-07%20TP.pdf", ai.Result{
		OCR: "- ==check logs==",
	}, nil)
	if !strings.Contains(s, "\nstatus: inbox\n") {
		t.Fatalf("expected status inbox:\n%s", s)
	}
}

func TestPutCalDAVFailureDoesNotFailImport(t *testing.T) {
	sink := &fakeTaskSink{err: errors.New("caldav: put x: 503")}
	s := importWithAI(t, "meeting", "2026-10-09%20Review.pdf", ai.Result{
		OCR:     "x",
		MyTasks: []string{"Send notes"},
	}, sink)
	if !strings.Contains(s, "- [ ] Send notes") {
		t.Fatalf("note not rendered:\n%s", s)
	}
	if len(sink.pushed) != 1 {
		t.Fatalf("expected push attempt, got %d", len(sink.pushed))
	}
}
