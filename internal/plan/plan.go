package plan

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"inkflow/internal/config"
	"inkflow/internal/util"
)

type Result struct {
	SourcePath string
	PDFRel     string
	NoteRel    string
	Title      string
	Date       time.Time
	Template   string
	Tags       []string
	AI         bool
}

var ErrNoRoute = errors.New("no route matches")

var fileDatePrefix = "2006-01-02"
var tagPattern = regexp.MustCompile(`\[([^\]]+)\]`)

func Build(routes []config.Route, defaults *config.Config, input string, now time.Time) (Result, error) {
	match, err := Select(routes, input)
	if err != nil {
		return Result{}, err
	}
	if !match.Matched {
		return Result{}, fmt.Errorf("%w: %q", ErrNoRoute, input)
	}
	r := match.Route
	if r.PDFDir == "" {
		r.PDFDir = defaults.DefaultPDFDir
	}
	if r.NoteDir == "" {
		r.NoteDir = defaults.DefaultNoteDir
	}
	pdfName := r.PDFName
	if pdfName == "" {
		pdfName = "{date}-{slug}.pdf"
	}
	noteName := r.NoteName
	if noteName == "" {
		noteName = "{date} {stem}.md"
	}
	stem := strings.TrimSuffix(path.Base(input), path.Ext(input))
	date, title, tags := parseStem(stem, now)
	slug := util.Slug(title)
	ext := strings.TrimPrefix(path.Ext(input), ".")
	pdfPattern := RenderPattern(pdfName, date, title, slug, ext)
	notePattern := RenderPattern(noteName, date, title, slug, ext)
	return Result{
		SourcePath: input,
		PDFRel:     util.SlashPath(r.PDFDir, pdfPattern),
		NoteRel:    util.SlashPath(r.NoteDir, notePattern),
		Title:      title,
		Date:       date,
		Template:   r.Template,
		Tags:       tags,
		AI:         r.AI,
	}, nil
}

func parseStem(stem string, fallback time.Time) (time.Time, string, []string) {
	date := fallback
	title := strings.TrimSpace(stem)
	if len(title) >= len(fileDatePrefix) {
		if parsed, err := time.Parse(fileDatePrefix, title[:len(fileDatePrefix)]); err == nil {
			date = parsed
			title = strings.TrimSpace(title[len(fileDatePrefix):])
		}
	}
	tags := make([]string, 0)
	title = tagPattern.ReplaceAllStringFunc(title, func(match string) string {
		sub := tagPattern.FindStringSubmatch(match)
		if len(sub) == 2 {
			tag := util.Slug(sub[1])
			if tag != "" {
				tags = append(tags, tag)
			}
		}
		return " "
	})
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		title = strings.TrimSpace(stem)
	}
	return date, title, dedupeTags(tags)
}

func dedupeTags(tags []string) []string {
	if len(tags) < 2 {
		return tags
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}
