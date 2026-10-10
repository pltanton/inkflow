package note

import (
	"regexp"
	"strings"
)

var (
	highlightPattern = regexp.MustCompile(`==(\S(?:[^=\n]*?\S)?)==`)
	linePrefix       = regexp.MustCompile(`^(?:#{1,6}\s+|>\s*|[-*+]\s+|\d+[.)]\s+|\[[ xX]\]\s+)+`)
	markerStart      = regexp.MustCompile(`^<!-- inkflow:[\w-]+:start -->$`)
	markerEnd        = regexp.MustCompile(`^<!-- inkflow:[\w-]+:end -->$`)
)

func ExtractHighlights(markdown string) []string {
	var out []string
	seen := map[string]struct{}{}
	inFence := false
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence || !highlightPattern.MatchString(trimmed) {
			continue
		}
		text := linePrefix.ReplaceAllString(trimmed, "")
		text = strings.TrimSpace(highlightPattern.ReplaceAllString(text, "$1"))
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

func PlaceTasks(content string, tasks []string) string {
	if len(tasks) == 0 {
		return content
	}
	body := TaskLines(tasks, "")
	if strings.Contains(content, "<!-- inkflow:tasks:start -->") {
		return UpsertMarkerBlock(content, "Tasks", "tasks", body)
	}
	lines := strings.Split(content, "\n")
	inBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case markerStart.MatchString(trimmed):
			inBlock = true
		case markerEnd.MatchString(trimmed):
			inBlock = false
		case inBlock:
		case trimmed == "- [ ]":
			lines[i] = body
			return strings.Join(lines, "\n")
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		for _, task := range tasks {
			if trimmed == "- [ ] "+task || trimmed == "- [x] "+task {
				return content
			}
		}
	}
	return UpsertMarkerBlock(content, "Tasks", "tasks", body)
}

func TaskLines(tasks []string, prefix string) string {
	var b strings.Builder
	for i, task := range tasks {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- [ ] ")
		b.WriteString(prefix)
		b.WriteString(task)
	}
	return b.String()
}
