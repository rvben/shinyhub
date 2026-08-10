package cli

import (
	"bytes"
	"strings"
	"testing"
)

func renderTable(t *table) string {
	var buf bytes.Buffer
	t.render(&buf)
	return buf.String()
}

func TestTableSizesColumnsToWidestCell(t *testing.T) {
	got := renderTable(newTable("NAME", "HOST").
		row(txt("dev"), txt("http://127.0.0.1:8099")).
		row(txt("production-eu"), txt("https://shinyhub.example.com")))

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d lines: %q", len(lines), got)
	}
	// The second column must start at the same offset on every line, including
	// the header. This is the property the hand-rolled fixed-width formats broke
	// as soon as a value outgrew the width baked into the format string.
	want := strings.Index(lines[0], "HOST")
	values := []string{"http://127.0.0.1:8099", "https://shinyhub.example.com"}
	for i, ln := range lines[1:] {
		if got := strings.Index(ln, values[i]); got != want {
			t.Errorf("row %q: column 2 starts at %d, want %d", ln, got, want)
		}
	}
}

func TestTableRightAlignsNumericColumns(t *testing.T) {
	got := renderTable(newTable("SLUG", "DEPLOYS").alignRight(1).
		row(txt("demo"), txt(1)).
		row(txt("reporting"), txt(120)))

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	// Right alignment means the last character of each count lands in the same
	// column; that is what makes a column of numbers comparable at a glance.
	for _, ln := range lines {
		if got, want := len(strings.TrimRight(ln, " ")), len(strings.TrimRight(lines[0], " ")); got != want {
			t.Errorf("line %q ends at %d, want %d (ragged right edge)", ln, got, want)
		}
	}
}

func TestTableRowsHaveNoTrailingWhitespace(t *testing.T) {
	got := renderTable(newTable("SLUG", "STATUS").
		row(txt("demo"), txt("running")).
		row(txt("a-much-longer-slug"), txt("stopped")))

	for _, ln := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if strings.HasSuffix(ln, " ") {
			t.Errorf("line %q has trailing whitespace", ln)
		}
	}
}

// TestTableMeasuresPlainTextNotEscapes is the alignment invariant that makes
// coloring a table safe: a painted cell must occupy exactly as many columns as
// its plain text.
func TestTableMeasuresPlainTextNotEscapes(t *testing.T) {
	tbl := newTable("SLUG", "STATUS", "DEPLOYS").alignRight(2).
		row(txt("demo"), statusTxt("running"), txt(1)).
		row(txt("reporting"), statusTxt("failed"), txt(12))

	var plainBuf, colorBuf bytes.Buffer
	tbl.render(&plainBuf) // buffer: styler inert
	renderTableWithStyler(tbl, &colorBuf, styler{color: true})

	plain := strings.Split(strings.TrimRight(plainBuf.String(), "\n"), "\n")
	colored := strings.Split(strings.TrimRight(colorBuf.String(), "\n"), "\n")
	if len(plain) != len(colored) {
		t.Fatalf("line count differs: plain %d, colored %d", len(plain), len(colored))
	}
	for i := range plain {
		if got := stripANSI(colored[i]); got != plain[i] {
			t.Errorf("line %d: colored strips to %q, want %q", i, got, plain[i])
		}
	}
	if !strings.Contains(colorBuf.String(), ansiGreen) {
		t.Error("colored render carries no green: the status cell was not painted")
	}
}

// TestTableMissingCellsRenderAsDash covers a row built from a projected item
// that is missing a field: the column must hold a placeholder rather than
// collapse and shift every later column.
func TestTableMissingCellsRenderAsDash(t *testing.T) {
	got := renderTable(newTable("NAME", "HOST", "USER").
		row(txt("dev"), txt("http://127.0.0.1:8099"), txt("admin")).
		row(txt("staging"), txt("https://staging.example.com")))

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if !strings.HasSuffix(lines[2], "-") {
		t.Errorf("short row = %q, want a trailing %q placeholder", lines[2], "-")
	}
}

// TestTableNoteHangsUnderItsRow pins where a per-row detail line lands: past the
// first column, so it reads as belonging to the row above rather than as a row
// of its own.
func TestTableNoteHangsUnderItsRow(t *testing.T) {
	got := renderTable(newTable("ID", "STATUS").
		row(txt(3), txt("succeeded")).
		row(txt(4), txt("failed")).note("bundle missing app.py"))

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want header + 2 rows + 1 note, got %d lines: %q", len(lines), got)
	}
	// Column 0 is 2 wide ("ID"), plus the 2-space gap: the note starts at 4.
	want := "    └ bundle missing app.py"
	if lines[3] != want {
		t.Errorf("note line = %q, want %q", lines[3], want)
	}
	if strings.Index(lines[3], "└") != strings.Index(lines[0], "STATUS") {
		t.Errorf("note marker at %d, want the second column's offset %d",
			strings.Index(lines[3], "└"), strings.Index(lines[0], "STATUS"))
	}
}

// TestTableNoteIgnoresEmpty covers passing an optional field straight through:
// an absent failure reason must not print a bare marker line.
func TestTableNoteIgnoresEmpty(t *testing.T) {
	got := renderTable(newTable("ID", "STATUS").
		row(txt(3), txt("succeeded")).note(""))

	if strings.Contains(got, "└") {
		t.Errorf("empty note produced a marker line:\n%s", got)
	}
}

// TestTableIndentShiftsEveryLine covers a table nested under a heading (the
// Workers block of `apps show`): the shift must apply to the header and to
// every row, or the block reads as two separate tables.
func TestTableIndentShiftsEveryLine(t *testing.T) {
	got := renderTable(newTable("SLOT", "STATUS").indent(2).
		row(txt(0), txt("running")).
		row(txt(1), txt("booting")).note("waiting for register"))

	for _, ln := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !strings.HasPrefix(ln, "  ") {
			t.Errorf("line %q is not indented", ln)
		}
	}
}

// TestTableMarkerColumnStaysEmpty pins the difference between "no value" and
// "not this row": an unmarked row leaves the marker column blank rather than
// filling it with the missing-value dash.
func TestTableMarkerColumnStaysEmpty(t *testing.T) {
	got := renderTable(newTable("", "NAME").
		row(markTxt("*"), txt("dev")).
		row(markTxt(""), txt("production")))

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if !strings.HasPrefix(lines[1], "*  dev") {
		t.Errorf("marked row = %q, want a leading marker", lines[1])
	}
	if !strings.HasPrefix(lines[2], "   production") {
		t.Errorf("unmarked row = %q, want a blank marker column (no dash)", lines[2])
	}
}

// TestTablePaintedCellsKeepAlignment extends the plain-text-measurement
// invariant to the marker and alert cells, whose paint runs on a one-character
// and a whole-word field respectively.
func TestTablePaintedCellsKeepAlignment(t *testing.T) {
	tbl := newTable("", "APP", "STALE").
		row(markTxt("*"), txt("demo"), alertTxt("yes")).
		row(markTxt(""), txt("reporting"), txt("no"))

	var plainBuf, colorBuf bytes.Buffer
	tbl.render(&plainBuf)
	renderTableWithStyler(tbl, &colorBuf, styler{color: true})

	plain := strings.Split(strings.TrimRight(plainBuf.String(), "\n"), "\n")
	colored := strings.Split(strings.TrimRight(colorBuf.String(), "\n"), "\n")
	for i := range plain {
		if got := stripANSI(colored[i]); got != plain[i] {
			t.Errorf("line %d: colored strips to %q, want %q", i, got, plain[i])
		}
	}
	if !strings.Contains(colorBuf.String(), ansiRed) {
		t.Error("colored render carries no red: the alert cell was not painted")
	}
	if !strings.Contains(colorBuf.String(), ansiGreen) {
		t.Error("colored render carries no green: the marker cell was not painted")
	}
}

func TestCellString(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "-"},
		{"", "-"},
		{"demo", "demo"},
		{7, "7"},
		{float64(12), "12"}, // JSON numbers decode as float64; counts must not gain ".0"
		{true, "true"},
	}
	for _, tc := range cases {
		if got := cellString(tc.in); got != tc.want {
			t.Errorf("cellString(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// renderTableWithStyler renders with an explicit styler, which is the only way
// to exercise the painted path from a test: stylerFor deliberately refuses to
// style anything that is not a terminal.
func renderTableWithStyler(t *table, w *bytes.Buffer, s styler) {
	t.renderWith(w, s)
}

// stripANSI removes SGR escape sequences so a colored line can be compared
// against its plain equivalent.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
