// Package render provides terminal table output helpers.
package render

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// Table writes a tab-aligned table to stdout.
type Table struct {
	w       *tabwriter.Writer
	columns int
}

// NewTable creates a table with the given header row written to stdout.
func NewTable(headers ...string) *Table {
	return NewTableTo(os.Stdout, headers...)
}

// NewTableTo creates a table writing to w.
func NewTableTo(out io.Writer, headers ...string) *Table {
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, strings.Join(cleanStrings(headers), "\t"))
	return &Table{w: w, columns: len(headers)}
}

// Row appends a row. Cells are converted with fmt.Sprint.
func (t *Table) Row(cells ...any) {
	parts := make([]string, len(cells))
	for i, c := range cells {
		parts[i] = CleanText(fmt.Sprint(c))
	}
	fmt.Fprintln(t.w, strings.Join(parts, "\t"))
}

// Flush renders the table.
func (t *Table) Flush() error { return t.w.Flush() }

// CleanText strips terminal control characters from human-readable output.
func CleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)
}

func cleanStrings(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = CleanText(v)
	}
	return out
}
