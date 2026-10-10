package caldav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type Config struct {
	URL           string
	User          string
	Password      string
	ObsidianVault string
	Categories    []string
}

type Client struct {
	cfg        Config
	httpClient *http.Client
	now        func() time.Time
}

type NoteTasks struct {
	NotePath string
	Template string
	Tags     []string
	Mine     []string
	Agent    []string
}

type Todo struct {
	UID         string
	Summary     string
	Description string
	Categories  []string
}

func New(cfg Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		now:        time.Now,
	}
}

func (c *Client) PushNote(ctx context.Context, n NoteTasks) error {
	var errs []error
	for _, todo := range c.Todos(n) {
		if err := c.Put(ctx, todo); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Client) Todos(n NoteTasks) []Todo {
	base := append([]string{}, c.cfg.Categories...)
	if n.Template != "" && n.Template != "default" {
		base = append(base, n.Template)
	}
	base = append(base, n.Tags...)
	base = append(base, "inkflow")

	desc := n.NotePath + "\n" + ObsidianURI(c.cfg.ObsidianVault, n.NotePath)
	todos := make([]Todo, 0, len(n.Mine)+len(n.Agent))
	for _, task := range n.Mine {
		todos = append(todos, Todo{
			UID:         UID(n.NotePath, task),
			Summary:     task,
			Description: desc,
			Categories:  dedupe(base),
		})
	}
	for _, task := range n.Agent {
		todos = append(todos, Todo{
			UID:         UID(n.NotePath, task),
			Summary:     task,
			Description: desc,
			Categories:  dedupe(append(append([]string{}, base...), "agent")),
		})
	}
	return todos
}

func (c *Client) Put(ctx context.Context, todo Todo) error {
	target := strings.TrimRight(c.cfg.URL, "/") + "/" + url.PathEscape(todo.UID) + ".ics"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, strings.NewReader(c.ICS(todo)))
	if err != nil {
		return fmt.Errorf("caldav: build request: %w", err)
	}
	req.SetBasicAuth(c.cfg.User, c.cfg.Password)
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	req.Header.Set("If-None-Match", "*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("caldav: put %s: %w", todo.UID, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode == http.StatusPreconditionFailed:
		return nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	default:
		return fmt.Errorf("caldav: put %s: %d %s", todo.UID, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func (c *Client) ICS(todo Todo) string {
	cats := make([]string, len(todo.Categories))
	for i, cat := range todo.Categories {
		cats[i] = escapeText(cat)
	}
	lines := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//inkflow//EN",
		"BEGIN:VTODO",
		"UID:" + todo.UID,
		"DTSTAMP:" + c.now().UTC().Format("20060102T150405Z"),
		"SUMMARY:" + escapeText(todo.Summary),
		"DESCRIPTION:" + escapeText(todo.Description),
	}
	if len(cats) > 0 {
		lines = append(lines, "CATEGORIES:"+strings.Join(cats, ","))
	}
	lines = append(lines, "STATUS:NEEDS-ACTION", "END:VTODO", "END:VCALENDAR")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(fold(l))
		b.WriteString("\r\n")
	}
	return b.String()
}

func UID(notePath, task string) string {
	sum := sha256.Sum256([]byte(notePath + "\n" + task))
	return "inkflow-" + hex.EncodeToString(sum[:16])
}

func ObsidianURI(vault, notePath string) string {
	enc := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	return "obsidian://open?vault=" + enc(vault) + "&file=" + enc(notePath)
}

func escapeText(s string) string {
	return strings.NewReplacer(
		`\`, `\\`,
		";", `\;`,
		",", `\,`,
		"\r\n", `\n`,
		"\n", `\n`,
	).Replace(s)
}

// RFC 5545 §3.1: lines longer than 75 octets must be folded.
func fold(line string) string {
	const limit = 75
	if len(line) <= limit {
		return line
	}
	var b strings.Builder
	width := limit
	for len(line) > width {
		cut := width
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		b.WriteString(line[:cut])
		b.WriteString("\r\n ")
		line = line[cut:]
		width = limit - 1
	}
	b.WriteString(line)
	return b.String()
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
