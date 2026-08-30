package pgvector

import (
	"bytes"
	"strings"
	"testing"
)

// The bulk path writes the CSV form of COPY rather than binding parameters, so
// the encoding is this package's problem and gets a test that needs no
// database. Chunk text is arbitrary document content: it contains quotes,
// newlines, tabs and backslashes sooner or later, and a delimiter chosen on the
// assumption that it does not is a corruption that shows up as a shifted column
// months later.
func TestCSVEncodesEveryAwkwardByte(t *testing.T) {
	var buf bytes.Buffer
	err := writeCSV(&buf, []any{
		`he said "hi"`,
		"line one\nline two",
		"a\tb",
		`back\slash`,
		"comma,separated",
		nil,
		"",
		42,
		0.5,
		true,
		[]float32{1, -0.25},
	})
	if err != nil {
		t.Fatalf("writeCSV: %v", err)
	}
	want := `"he said ""hi""","line one` + "\n" + `line two","a` + "\t" + `b","back\slash","comma,separated",,"",` +
		`"42","0.5","t","[1,-0.25]"` + "\n"
	if got := buf.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// A NUL byte cannot be stored in a Postgres text column at all. Stripping it
// would be a silent edit of the buyer's corpus, so it is refused by name.
func TestCSVRefusesANULByte(t *testing.T) {
	var buf bytes.Buffer
	err := writeCSV(&buf, []any{"before\x00after"})
	if err == nil {
		t.Fatal("a NUL byte was accepted")
	}
	if !strings.Contains(err.Error(), "NUL") {
		t.Errorf("error = %v, want it to name the byte", err)
	}
}
