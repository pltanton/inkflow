package pdfhash

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSumIgnoresVolatileExportMetadata(t *testing.T) {
	a := []byte(`%PDF-1.7
1 0 obj << /CreationDate (D:20260720100000Z) /ModDate (D:20260720100100Z) >> endobj
2 0 obj << /Type /Annot /M (D:20260720100100Z) >> endobj
3 0 obj << /Type /Metadata /Length 68 >> stream
<xmp:ModifyDate>2026-07-20T10:01:00Z</xmp:ModifyDate>
endstream endobj
trailer << /ID [<11111111><22222222>] >>
%%EOF`)
	b := []byte(`%PDF-1.7
1 0 obj << /CreationDate (D:20260721100000Z) /ModDate (D:20260721100100Z) >> endobj
2 0 obj << /Type /Annot /M (D:20260721100100Z) >> endobj
3 0 obj << /Type /Metadata /Length 68 >> stream
<xmp:ModifyDate>2026-07-21T10:01:00Z</xmp:ModifyDate>
endstream endobj
trailer << /ID [<aaaaaaaa><bbbbbbbb>] >>
%%EOF`)

	if Sum(a) != Sum(b) {
		t.Fatal("volatile export metadata changed the stable hash")
	}
}

func TestSumDetectsContentChanges(t *testing.T) {
	a := []byte("%PDF-1.7\n1 0 obj << /Length 7 >> stream\nstroke1\nendstream endobj\n%%EOF")
	b := []byte("%PDF-1.7\n1 0 obj << /Length 7 >> stream\nstroke2\nendstream endobj\n%%EOF")

	if Sum(a) == Sum(b) {
		t.Fatal("page content change did not change the stable hash")
	}
}

func TestSumDoesNotIgnoreUnrelatedDates(t *testing.T) {
	a := []byte("%PDF-1.7\nstream\nMeeting on 2026-07-20\nendstream\n%%EOF")
	b := []byte("%PDF-1.7\nstream\nMeeting on 2026-07-21\nendstream\n%%EOF")

	if Sum(a) == Sum(b) {
		t.Fatal("visible date change did not change the stable hash")
	}
}

func TestSumMatchesRealBOOXReexports(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", "assets", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	if Sum(read("Fish1.pdf")) != Sum(read("Fish2.pdf")) {
		t.Fatal("unchanged BOOX re-exports have different stable hashes")
	}
}
