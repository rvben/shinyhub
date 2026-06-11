package cli

import "os"

type outputFormat string

const (
	formatTable  outputFormat = "table"
	formatJSON   outputFormat = "json"
	formatNDJSON outputFormat = "ndjson"
)

// resolvedFormat caches the last resolution (set by resolveFormat in a later
// change) so the error renderer matches the success-path format. While
// empty, currentFormat falls back to TTY detection, which is also the
// correct behavior for errors raised before any command resolved a format
// (cobra usage errors).
var resolvedFormat outputFormat

func currentFormat() outputFormat {
	if resolvedFormat != "" {
		return resolvedFormat
	}
	if isTTY(os.Stdout) {
		return formatTable
	}
	return formatJSON
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
