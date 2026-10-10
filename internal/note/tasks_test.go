package note

import (
	"reflect"
	"testing"
)

func TestExtractHighlights(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "none",
			in:   "- plain bullet\n- **framed** text\n- a == b",
			want: nil,
		},
		{
			name: "whole bullet becomes task text",
			in:   "- мне: roadmap\n- ==Спросить== Клода про сверку с CH\n- other",
			want: []string{"Спросить Клода про сверку с CH"},
		},
		{
			name: "multiple spans on one line and several lines",
			in:   "==Draft== the ==RFC== today\nnoise\n### ==Check deploy==",
			want: []string{"Draft the RFC today", "Check deploy"},
		},
		{
			name: "nested bullets and checkboxes",
			in:   "### Дима\n- fixes\n    - ==collect metrics== for abacus\n  - [ ] ==ping infra==\n1. ==numbered item==",
			want: []string{"collect metrics for abacus", "ping infra", "numbered item"},
		},
		{
			name: "duplicates collapse",
			in:   "- ==same==\n- ==same==",
			want: []string{"same"},
		},
		{
			name: "fenced code ignored",
			in:   "```\n==not a task==\n```\n- ==task==",
			want: []string{"task"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractHighlights(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestPlaceTasks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		tasks   []string
		want    string
	}{
		{
			name:    "no tasks keeps placeholder",
			content: "## Action items\n- [ ]\n",
			tasks:   nil,
			want:    "## Action items\n- [ ]\n",
		},
		{
			name:    "replaces placeholder",
			content: "## TODO\n\n- [ ]\n",
			tasks:   []string{"Write roadmap", "Fill IPF"},
			want:    "## TODO\n\n- [ ] Write roadmap\n- [ ] Fill IPF\n",
		},
		{
			name:    "ignores checkbox inside marker block",
			content: "## OCR\n\n<!-- inkflow:ocr:start -->\n- [ ]\n<!-- inkflow:ocr:end -->\n\n## TODO\n- [ ]\n",
			tasks:   []string{"A"},
			want:    "## OCR\n\n<!-- inkflow:ocr:start -->\n- [ ]\n<!-- inkflow:ocr:end -->\n\n## TODO\n- [ ] A\n",
		},
		{
			name:    "already placed tasks are not duplicated",
			content: "## TODO\n- [x] A\n",
			tasks:   []string{"A"},
			want:    "## TODO\n- [x] A\n",
		},
		{
			name:    "no placeholder falls back to tasks block",
			content: "## Notes\n- \n",
			tasks:   []string{"A"},
			want:    "## Notes\n- \n\n## Tasks\n\n<!-- inkflow:tasks:start -->\n- [ ] A\n<!-- inkflow:tasks:end -->\n\n",
		},
		{
			name:    "existing tasks block is replaced",
			content: "## Tasks\n\n<!-- inkflow:tasks:start -->\n- [ ] old\n<!-- inkflow:tasks:end -->\n\n",
			tasks:   []string{"new"},
			want:    "## Tasks\n\n<!-- inkflow:tasks:start -->\n- [ ] new\n<!-- inkflow:tasks:end -->\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PlaceTasks(tt.content, tt.tasks); got != tt.want {
				t.Fatalf("got:\n%q\nwant:\n%q", got, tt.want)
			}
		})
	}
}

func TestTaskLinesWithPrefix(t *testing.T) {
	got := TaskLines([]string{"one", "two"}, "#agent ")
	want := "- [ ] #agent one\n- [ ] #agent two"
	if got != want {
		t.Fatalf("got %q", got)
	}
}
