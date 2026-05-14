package frontmatter

import (
	"strings"
	"testing"
)

func TestUpdateTagsPreservesBody(t *testing.T) {
	got := UpdateTags("---\ntags:\n  - old\n---\n\n# Title\nBody\n", []string{"new", "tag"})
	if !strings.Contains(got, "  - new\n") || !strings.Contains(got, "  - tag\n") {
		t.Fatalf("tags not updated:\n%s", got)
	}
	if !strings.Contains(got, "# Title\nBody\n") {
		t.Fatalf("body changed:\n%s", got)
	}
}

func TestUpdateTagsAddsFrontmatter(t *testing.T) {
	got := UpdateTags("# Title\nBody\n", []string{"one", "two", "one"})
	if !strings.HasPrefix(got, "---\n") {
		t.Fatalf("missing frontmatter:\n%s", got)
	}
	if !strings.Contains(got, "  - one\n") || !strings.Contains(got, "  - two\n") {
		t.Fatalf("tags missing:\n%s", got)
	}
	if !strings.HasSuffix(got, "# Title\nBody\n") {
		t.Fatalf("body changed:\n%s", got)
	}
}
