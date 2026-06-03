package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"inkflow/internal/config"
)

func TestSelectMostSpecificMatchWins(t *testing.T) {
	routes := []config.Route{
		{From: "Projects/"},
		{From: "Projects/Alpha/"},
	}
	got, err := Select(routes, "Projects/Alpha/note.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Matched {
		t.Fatal("expected match")
	}
	if got.Route.From != "Projects/Alpha/" {
		t.Fatalf("expected most specific match, got %q", got.Route.From)
	}
}

func TestSelectRejectsAmbiguousMatch(t *testing.T) {
	routes := []config.Route{
		{From: "Projects/Alpha/"},
		{From: "Projects/Alpha/"},
	}
	if _, err := Select(routes, "Projects/Alpha/note.pdf"); err == nil {
		t.Fatal("expected ambiguous match error")
	}
}

func TestRenderPattern(t *testing.T) {
	got := RenderPattern("{date}-{slug}.{ext}", time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC), "standup", "standup", "pdf")
	if got != "2026-05-06-standup.pdf" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderNoteBodyAppendsTagsOnce(t *testing.T) {
	body, err := RenderNoteBody("", NoteData{
		Date:       "2026-05-06",
		Title:      "Abacus",
		PDFRelPath: "_files/Attachments/Boox/Syncs/2026-05-06 Abacus.pdf",
		Template:   "meeting",
		Tags:       []string{"finance", "analytics"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Count(body, "tags:") != 1 {
		t.Fatalf("expected one tags block, got:\n%s", body)
	}
	if !strings.Contains(body, "  - finance\n") || !strings.Contains(body, "  - analytics\n") {
		t.Fatalf("missing dynamic tags:\n%s", body)
	}
}

func TestRenderNoteBodyUsesTemplateDirOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meeting.md.tmpl")
	if err := os.WriteFile(path, []byte("title: {{.Title}}\n{{tagLines .Tags}}"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := RenderNoteBody(dir, NoteData{Title: "Abacus", Tags: []string{"finance"}, Template: "meeting"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(body, "title: Abacus") || !strings.Contains(body, "  - finance") {
		t.Fatalf("override not used:\n%s", body)
	}
}

func TestBuildUsesFilenameDateTitleAndTags(t *testing.T) {
	routes := []config.Route{{From: "Syncs/"}}
	cfg := &config.Config{DefaultPDFDir: "pdfs", DefaultNoteDir: "notes"}
	got, err := Build(routes, cfg, "Syncs/2026-05-06 Processing service [finance] [analytics].pdf", time.Date(2026, 5, 7, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Date.Format("2006-01-02") != "2026-05-06" {
		t.Fatalf("date = %s", got.Date.Format("2006-01-02"))
	}
	if got.Title != "Processing service" {
		t.Fatalf("title = %q", got.Title)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "finance" || got.Tags[1] != "analytics" {
		t.Fatalf("tags = %#v", got.Tags)
	}
	if got.PDFRel != "pdfs/2026-05-06-processing-service.pdf" {
		t.Fatalf("pdf rel = %q", got.PDFRel)
	}
	if got.NoteRel != "notes/2026-05-06 Processing service.md" {
		t.Fatalf("note rel = %q", got.NoteRel)
	}
}

func TestBuildStripsHashtagsFromTitle(t *testing.T) {
	routes := []config.Route{{From: "Syncs/"}}
	cfg := &config.Config{DefaultPDFDir: "pdfs", DefaultNoteDir: "notes"}
	got, err := Build(routes, cfg, "Syncs/2026-05-07 processing service [finance] [ledger].pdf", time.Date(2026, 5, 7, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Title != "processing service" {
		t.Fatalf("title = %q", got.Title)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "finance" || got.Tags[1] != "ledger" {
		t.Fatalf("tags = %#v", got.Tags)
	}
	if got.NoteRel != "notes/2026-05-07 processing service.md" {
		t.Fatalf("note rel = %q", got.NoteRel)
	}
}

func TestBuildPropagatesRouteAIFlag(t *testing.T) {
	routes := []config.Route{{From: "Syncs/", AI: true}}
	cfg := &config.Config{DefaultPDFDir: "pdfs", DefaultNoteDir: "notes"}
	got, err := Build(routes, cfg, "Syncs/note.pdf", time.Date(2026, 6, 4, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.AI {
		t.Fatal("expected AI flag to propagate")
	}
}
