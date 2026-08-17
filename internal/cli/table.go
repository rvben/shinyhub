package cli

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// colGap is the run of spaces between two table columns.
const colGap = "  "

// cell is one table cell: the plain text, which is what column widths are
// measured from, plus an optional paint applied after padding. Keeping the two
// apart is what stops an ANSI escape from being counted as visible width and
// pushing every later column out of alignment.
type cell struct {
	text  string
	paint func(styler, string) string
}

// txt returns an unstyled cell. A nil value renders as "-" so a field that is
// absent reads as "not recorded" rather than as an empty column.
func txt(v any) cell { return cell{text: cellString(v)} }

// dimTxt returns a cell for secondary detail: an identifier, a timestamp, a
// value the reader scans past unless they are looking for it.
func dimTxt(v any) cell { return cell{text: cellString(v), paint: styler.dim} }

// statusTxt returns a cell whose color is chosen by the lifecycle word it
// holds. The word itself is always printed, so the color adds emphasis to a
// signal that is already there.
func statusTxt(v any) cell { return cell{text: cellString(v), paint: styler.status} }

// alertTxt returns a cell painted red, for a value that is itself the problem
// the reader is scanning for. The word still says so on its own.
func alertTxt(v any) cell { return cell{text: cellString(v), paint: styler.red} }

// markTxt returns a marker cell for the one row that is current or selected.
// An empty marker stays empty rather than becoming "-": a row that is not the
// current one is not missing a value.
func markTxt(marker string) cell { return cell{text: marker, paint: styler.green} }

func cellString(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case string:
		if t == "" {
			return "-"
		}
		return t
	default:
		return fmt.Sprintf("%v", v)
	}
}

// table renders rows in columns sized to their widest cell. Every list command
// uses it so column widths adapt to the data instead of being frozen at a width
// that a long slug or host URL silently overflows.
type table struct {
	headers []string
	right   map[int]bool
	rows    []tableRow
	lead    string
}

// tableRow is one row plus an optional note: a detail line printed under the
// row, indented past the first column, for something that explains that row
// (a failure reason) and would not fit in a column of its own.
type tableRow struct {
	cells []cell
	note  string
	paint func(styler, string) string
}

// newTable starts a table with the given column headers.
func newTable(headers ...string) *table {
	return &table{headers: headers, right: map[int]bool{}}
}

// alignRight right-aligns the given column indexes. Counts and sizes read
// correctly only when their digits line up.
func (t *table) alignRight(cols ...int) *table {
	for _, c := range cols {
		t.right[c] = true
	}
	return t
}

// indent shifts every line right by n spaces, for a table nested under a
// heading rather than standing on its own.
func (t *table) indent(n int) *table {
	t.lead = strings.Repeat(" ", n)
	return t
}

// row appends one row. A row with fewer cells than there are headers is padded
// with "-" so a short row cannot shift the columns after it.
func (t *table) row(cells ...cell) *table {
	t.rows = append(t.rows, tableRow{cells: cells})
	return t
}

// paintRow applies one style to the complete row added last. It is used for
// selected rows in terminal interfaces, where coloring a marker alone is too
// easy to lose while scanning a dense table.
func (t *table) paintRow(paint func(styler, string) string) *table {
	if len(t.rows) > 0 {
		t.rows[len(t.rows)-1].paint = paint
	}
	return t
}

// note attaches a detail line to the row added last. An empty note is ignored,
// so callers can pass an optional field straight through.
func (t *table) note(text string) *table {
	if text == "" || len(t.rows) == 0 {
		return t
	}
	t.rows[len(t.rows)-1].note = text
	return t
}

// render writes the table to w, styling it only if w is a terminal.
func (t *table) render(w io.Writer) { t.renderWith(w, stylerFor(w)) }

// renderWith writes the table using an explicit styler. Splitting it out lets
// tests exercise the painted path, which render can never produce for the
// buffer a test hands it.
func (t *table) renderWith(w io.Writer, s styler) {
	widths := t.widths()

	header := make([]string, len(t.headers))
	for i, h := range t.headers {
		header[i] = t.pad(h, h, widths[i], i)
	}
	// The whole header line is dimmed as one run: it is a label row, and the
	// reader's eye should land on the data under it.
	fmt.Fprintln(w, t.lead+s.dim(strings.TrimRight(strings.Join(header, colGap), " ")))

	// A note hangs under its row, indented past the first column so it reads as
	// belonging to that row rather than as a row of its own.
	noteIndent := t.lead + strings.Repeat(" ", widths[0]+len(colGap))
	for _, r := range t.rows {
		cols := make([]string, len(t.headers))
		for i := range t.headers {
			c := cell{text: "-"}
			if i < len(r.cells) {
				c = r.cells[i]
			}
			// Pad around the painted text using the plain text's width, so the
			// escapes sit inside the column and never count as visible columns.
			painted := c.text
			if c.paint != nil {
				painted = c.paint(s, c.text)
			}
			cols[i] = t.pad(painted, c.text, widths[i], i)
		}
		line := strings.TrimRight(strings.Join(cols, colGap), " ")
		if r.paint != nil {
			line = r.paint(s, line)
		}
		fmt.Fprintln(w, t.lead+line)
		if r.note != "" {
			fmt.Fprintf(w, "%s%s %s\n", noteIndent, s.dim("└"), s.dim(r.note))
		}
	}
}

// pad sizes one field: rendered is what gets written, plain is what its width
// is measured from. The last column is left unpadded when left-aligned so rows
// carry no trailing whitespace.
func (t *table) pad(rendered, plain string, width, idx int) string {
	fill := width - utf8.RuneCountInString(plain)
	if fill < 0 {
		fill = 0
	}
	if t.right[idx] {
		return strings.Repeat(" ", fill) + rendered
	}
	if idx == len(t.headers)-1 {
		return rendered
	}
	return rendered + strings.Repeat(" ", fill)
}

// widths measures each column against its header and every cell, counting
// runes rather than bytes so a non-ASCII app name does not skew the column.
func (t *table) widths() []int {
	w := make([]int, len(t.headers))
	for i, h := range t.headers {
		w[i] = utf8.RuneCountInString(h)
	}
	for _, r := range t.rows {
		for i, c := range r.cells {
			if i >= len(w) {
				continue
			}
			if n := utf8.RuneCountInString(c.text); n > w[i] {
				w[i] = n
			}
		}
	}
	return w
}
