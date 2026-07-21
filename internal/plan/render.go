package plan

import (
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

//go:embed templates/*.md.tmpl
var embeddedTemplates embed.FS

type NoteData struct {
	Date       string
	Title      string
	PDFRelPath string
	Template   string
	Tags       []string
}

func RenderPattern(pattern string, date time.Time, stem, slug, ext string) string {
	repl := strings.NewReplacer(
		"{date}", date.Format("2006-01-02"),
		"{stem}", stem,
		"{slug}", slug,
		"{ext}", ext,
	)
	return repl.Replace(pattern)
}

func RenderNoteBody(templateDir string, d NoteData) (string, error) {
	src, err := loadTemplateSource(templateDir, d.Template)
	if err != nil {
		return "", err
	}
	tpl, err := template.New("note").Funcs(template.FuncMap{
		"tagLines": tagLines,
	}).Parse(src)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tpl.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

func loadTemplateSource(templateDir, name string) (string, error) {
	candidates := templateCandidates(name)
	if templateDir != "" {
		for _, candidate := range candidates {
			path := filepath.Join(templateDir, candidate)
			if data, err := os.ReadFile(path); err == nil {
				slog.Default().Debug("template_resolved", "name", name, "source", "custom", "candidate", candidate)
				return string(data), nil
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
	}
	slog.Default().Debug("template_custom_not_found", "name", name, "template_dir", templateDir)
	for _, candidate := range candidates {
		data, err := embeddedTemplates.ReadFile(filepath.ToSlash(filepath.Join("templates", candidate)))
		if err == nil {
			slog.Default().Debug("template_resolved", "name", name, "source", "embedded", "candidate", candidate)
			return string(data), nil
		}
	}
	return "", fmt.Errorf("template %q not found", name)
}

func templateCandidates(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "default"
	}
	if name == "default" {
		return []string{"default.md.tmpl"}
	}
	return []string{name + ".md.tmpl", "default.md.tmpl"}
}

func tagLines(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	var b strings.Builder
	for _, tag := range tags {
		b.WriteString("  - ")
		b.WriteString(tag)
		b.WriteString("\n")
	}
	return b.String()
}
