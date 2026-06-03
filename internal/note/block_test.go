package note

import (
	"strings"
	"testing"
)

func TestUpsertMarkerBlockInsertsIntoEmptyContent(t *testing.T) {
	got := UpsertMarkerBlock("", "OCR", "ocr", "hello world")
	want := "## OCR\n\n<!-- inkflow:ocr:start -->\nhello world\n<!-- inkflow:ocr:end -->\n\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestUpsertMarkerBlockAppendsAfterExistingBody(t *testing.T) {
	in := "# Title\n\nBody.\n"
	got := UpsertMarkerBlock(in, "OCR", "ocr", "transcription")
	if !strings.Contains(got, "Body.\n") {
		t.Fatalf("existing body lost:\n%s", got)
	}
	if !strings.Contains(got, "## OCR\n\n<!-- inkflow:ocr:start -->\ntranscription\n<!-- inkflow:ocr:end -->") {
		t.Fatalf("OCR block missing:\n%s", got)
	}
}

func TestUpsertMarkerBlockReplacesExistingBlock(t *testing.T) {
	in := "# Title\n\n## OCR\n\n<!-- inkflow:ocr:start -->\nold text\n<!-- inkflow:ocr:end -->\n\nMore body.\n"
	got := UpsertMarkerBlock(in, "OCR", "ocr", "new text")
	if strings.Contains(got, "old text") {
		t.Fatalf("old text survived:\n%s", got)
	}
	if !strings.Contains(got, "new text") {
		t.Fatalf("new text missing:\n%s", got)
	}
	if !strings.Contains(got, "More body.") {
		t.Fatalf("trailing body lost:\n%s", got)
	}
}

func TestUpsertMarkerBlockHandlesTwoDistinctKeys(t *testing.T) {
	content := UpsertMarkerBlock("", "Summary", "summary", "- bullet one\n- bullet two")
	content = UpsertMarkerBlock(content, "OCR", "ocr", "full text")
	if !strings.Contains(content, "<!-- inkflow:summary:start -->") {
		t.Fatalf("summary block missing:\n%s", content)
	}
	if !strings.Contains(content, "<!-- inkflow:ocr:start -->") {
		t.Fatalf("ocr block missing:\n%s", content)
	}
	if !strings.Contains(content, "- bullet one") || !strings.Contains(content, "full text") {
		t.Fatalf("bodies missing:\n%s", content)
	}
	// Replacing summary must not touch ocr.
	updated := UpsertMarkerBlock(content, "Summary", "summary", "- new bullet")
	if strings.Contains(updated, "bullet one") {
		t.Fatalf("old summary text leaked:\n%s", updated)
	}
	if !strings.Contains(updated, "full text") {
		t.Fatalf("ocr block was disturbed:\n%s", updated)
	}
}

func TestUpsertMarkerBlockEmptyBodyReturnsContentUnchanged(t *testing.T) {
	in := "# Title\n\nBody.\n"
	got := UpsertMarkerBlock(in, "OCR", "ocr", "")
	if got != in {
		t.Fatalf("content was modified for empty body:\n%s", got)
	}
}

func TestUpsertMarkerBlockWhitespaceBodyIsNoOp(t *testing.T) {
	in := "# Title\n\nBody.\n"
	got := UpsertMarkerBlock(in, "OCR", "ocr", "   \n\t  ")
	if got != in {
		t.Fatalf("whitespace body mutated content:\n%s", got)
	}
}
