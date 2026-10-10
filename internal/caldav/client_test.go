package caldav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func newTestClient(url string) *Client {
	c := New(Config{
		URL:           url,
		User:          "anton",
		Password:      "secret",
		ObsidianVault: "Anton",
		Categories:    []string{"wallet"},
	})
	c.now = func() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) }
	return c
}

func TestUIDIsStableAndDistinct(t *testing.T) {
	a := UID("Wallet/1-1/2026-10-09 Iaroslav.md", "Fill IPF")
	if a != UID("Wallet/1-1/2026-10-09 Iaroslav.md", "Fill IPF") {
		t.Fatal("UID not stable")
	}
	if a == UID("Wallet/1-1/2026-10-09 Iaroslav.md", "Write roadmap") {
		t.Fatal("different tasks share UID")
	}
	if a == UID("Wallet/1-1/2026-10-10 Iaroslav.md", "Fill IPF") {
		t.Fatal("different notes share UID")
	}
	if !strings.HasPrefix(a, "inkflow-") || len(a) != len("inkflow-")+32 {
		t.Fatalf("unexpected UID shape %q", a)
	}
}

func TestTodosCategories(t *testing.T) {
	c := newTestClient("http://example")
	todos := c.Todos(NoteTasks{
		NotePath: "03. Resources/Wallet/1-1/2026-10-09 Iaroslav.md",
		Template: "1-1",
		Tags:     []string{"finance", "wallet"},
		Mine:     []string{"Fill IPF"},
		Agent:    []string{"Ask Claude about CH reconciliation"},
	})
	if len(todos) != 2 {
		t.Fatalf("got %d todos", len(todos))
	}
	if got := strings.Join(todos[0].Categories, ","); got != "wallet,1-1,finance,inkflow" {
		t.Errorf("mine categories = %q", got)
	}
	if got := strings.Join(todos[1].Categories, ","); got != "wallet,1-1,finance,inkflow,agent" {
		t.Errorf("agent categories = %q", got)
	}
	wantDesc := "03. Resources/Wallet/1-1/2026-10-09 Iaroslav.md\n" +
		"obsidian://open?vault=Anton&file=03.%20Resources%2FWallet%2F1-1%2F2026-10-09%20Iaroslav.md"
	if todos[0].Description != wantDesc {
		t.Errorf("description = %q", todos[0].Description)
	}
	if todos[0].UID != UID("03. Resources/Wallet/1-1/2026-10-09 Iaroslav.md", "Fill IPF") {
		t.Errorf("uid = %q", todos[0].UID)
	}
}

func TestTodosSkipsDefaultTemplateCategory(t *testing.T) {
	c := New(Config{})
	todos := c.Todos(NoteTasks{NotePath: "n.md", Template: "default", Mine: []string{"x"}})
	if got := strings.Join(todos[0].Categories, ","); got != "inkflow" {
		t.Errorf("categories = %q", got)
	}
}

func TestICS(t *testing.T) {
	c := newTestClient("http://example")
	got := c.ICS(Todo{
		UID:         "inkflow-abc",
		Summary:     "Call Dima; check a,b",
		Description: "note.md\nobsidian://open?vault=Anton&file=note.md",
		Categories:  []string{"wallet", "inkflow"},
	})
	want := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//inkflow//EN\r\n" +
		"BEGIN:VTODO\r\n" +
		"UID:inkflow-abc\r\n" +
		"DTSTAMP:20261010T120000Z\r\n" +
		"SUMMARY:Call Dima\\; check a\\,b\r\n" +
		"DESCRIPTION:note.md\\nobsidian://open?vault=Anton&file=note.md\r\n" +
		"CATEGORIES:wallet,inkflow\r\n" +
		"STATUS:NEEDS-ACTION\r\n" +
		"END:VTODO\r\n" +
		"END:VCALENDAR\r\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestICSFoldsLongLinesOnRuneBoundaries(t *testing.T) {
	c := newTestClient("http://example")
	summary := strings.Repeat("Уточнить у Владимира проводки ", 5)
	ics := c.ICS(Todo{UID: "u", Summary: summary})
	var unfolded strings.Builder
	for i, line := range strings.Split(strings.TrimSuffix(ics, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("line %d is %d octets", i, len(line))
		}
		if !utf8.ValidString(line) {
			t.Fatalf("line %d splits a rune: %q", i, line)
		}
		if strings.HasPrefix(line, " ") {
			unfolded.WriteString(line[1:])
			continue
		}
		unfolded.WriteString("\n" + line)
	}
	if !strings.Contains(unfolded.String(), "SUMMARY:"+summary) {
		t.Fatalf("summary not recoverable after unfolding:\n%s", unfolded.String())
	}
}

func TestPushNotePutsEachTodoAndTreats412AsExisting(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		if user, pass, ok := r.BasicAuth(); !ok || user != "anton" || pass != "secret" {
			t.Errorf("bad auth %q %q", user, pass)
		}
		if r.Header.Get("If-None-Match") != "*" {
			t.Errorf("If-None-Match = %q", r.Header.Get("If-None-Match"))
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "text/calendar") {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if _, ok := seen[r.URL.Path]; ok {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		seen[r.URL.Path] = string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL + "/remote.php/dav/calendars/anton/inbox/")
	n := NoteTasks{NotePath: "n.md", Template: "sync", Mine: []string{"A"}, Agent: []string{"B"}}
	for range 2 {
		if err := c.PushNote(context.Background(), n); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 resources, got %v", seen)
	}
	path := "/remote.php/dav/calendars/anton/inbox/" + UID("n.md", "A") + ".ics"
	body, ok := seen[path]
	if !ok {
		t.Fatalf("missing %s in %v", path, seen)
	}
	if !strings.Contains(body, "SUMMARY:A\r\n") || !strings.Contains(body, "UID:"+UID("n.md", "A")) {
		t.Errorf("unexpected body:\n%s", body)
	}
}

func TestPushNoteReportsServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	c := newTestClient(srv.URL)
	err := c.PushNote(context.Background(), NoteTasks{NotePath: "n.md", Mine: []string{"A"}})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected 403 error, got %v", err)
	}
}
