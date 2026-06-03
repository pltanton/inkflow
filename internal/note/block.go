// Package note holds small helpers for editing Obsidian-flavoured Markdown
// notes maintained by inkflow.
package note

import (
	"fmt"
	"regexp"
	"strings"
)

// UpsertMarkerBlock inserts a "## <heading>" section whose body is fenced
// by `<!-- inkflow:<markerKey>:start -->` / `:end -->` comments, replacing
// the same section if it already exists. If body is empty or whitespace-only,
// content is returned unchanged so the caller doesn't have to special-case
// the disabled-section path.
func UpsertMarkerBlock(content, heading, markerKey, body string) string {
	body = strings.TrimRight(body, "\n")
	if strings.TrimSpace(body) == "" {
		return content
	}

	block := fmt.Sprintf(
		"## %s\n\n<!-- inkflow:%s:start -->\n%s\n<!-- inkflow:%s:end -->\n\n",
		heading, markerKey, body, markerKey,
	)

	pattern := regexp.MustCompile(
		`(?s)(\n?)## ` + regexp.QuoteMeta(heading) +
			`\n\n<!-- inkflow:` + regexp.QuoteMeta(markerKey) +
			`:start -->.*?<!-- inkflow:` + regexp.QuoteMeta(markerKey) +
			`:end -->\n{0,2}`,
	)
	if match := pattern.FindStringSubmatchIndex(content); match != nil {
		leadingNewline := content[match[2]:match[3]]
		return content[:match[0]] + leadingNewline + block + content[match[1]:]
	}

	if content == "" {
		return block
	}
	sep := "\n\n"
	switch {
	case strings.HasSuffix(content, "\n\n"):
		sep = ""
	case strings.HasSuffix(content, "\n"):
		sep = "\n"
	}
	return content + sep + block
}
