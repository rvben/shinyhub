package cli

import (
	"fmt"
	"os"
)

type outputFormat string

const (
	formatTable  outputFormat = "table"
	formatJSON   outputFormat = "json"
	formatNDJSON outputFormat = "ndjson"
)

// resolvedFormat caches the last resolveFormat result so the error renderer
// matches the success-path format. Empty until a command resolves a format;
// currentFormat then falls back to TTY detection, which is also correct for
// errors raised before resolution (cobra usage errors).
var resolvedFormat outputFormat

// outputFlagValue holds the global -o/--output flag value. Registered as a
// root persistent flag in AddCommandsTo.
var outputFlagValue string

// quietFlag holds the global -q/--quiet flag. Registered as a root persistent
// flag in AddCommandsTo.
var quietFlag bool

func currentFormat() outputFormat {
	if resolvedFormat != "" {
		return resolvedFormat
	}
	// An explicit -o flag must override TTY detection so the error renderer
	// matches the format the user requested. Only valid values are forwarded;
	// an invalid -o will be rejected later by resolveFormat, and for that error
	// path the TTY fallback is the right choice.
	switch outputFormat(outputFlagValue) {
	case formatTable, formatJSON, formatNDJSON:
		return outputFormat(outputFlagValue)
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

func validationErr(msg, hint string) error {
	return &ExitCodeError{Code: 1, Kind: KindValidation, Err: &hintedMsgError{msg: msg, hint: hint}}
}

// resolveFormat resolves the effective output format for a command. legacyJSON
// is the command's --json alias value; streaming marks commands whose stdout is
// a line stream. On success, resolvedFormat is updated so the error renderer
// matches the success-path format.
func resolveFormat(legacyJSON bool, streaming bool) (outputFormat, error) {
	f, err := resolveFormatWith(outputFlagValue, legacyJSON, isTTY(os.Stdout), streaming)
	if err == nil {
		resolvedFormat = f
	}
	return f, err
}

// resolveLegacyTextJSON resolves the output format for commands with a
// --format text|json flag. "json" maps to the JSON format; "text" maps to
// table. Conflicts with -o/--output are treated as validation errors. The
// resolved format is cached so the error renderer matches.
func resolveLegacyTextJSON(legacyFmt string) (outputFormat, error) {
	switch legacyFmt {
	case "json":
		return resolveFormat(true, false)
	default: // "text"
		if outputFormat(outputFlagValue) == formatJSON {
			return "", validationErr("--format text conflicts with --output json", "drop one of the two flags")
		}
		if outputFormat(outputFlagValue) == formatNDJSON {
			return "", validationErr("--format text conflicts with --output ndjson", "drop one of the two flags")
		}
		resolvedFormat = formatTable
		return formatTable, nil
	}
}

func resolveFormatWith(flagValue string, legacyJSON, stdoutTTY, streaming bool) (outputFormat, error) {
	explicit := outputFormat(flagValue)
	switch explicit {
	case "", formatTable, formatJSON, formatNDJSON:
	default:
		return "", validationErr(
			fmt.Sprintf("unknown output format %q", flagValue),
			"valid formats: table, json, ndjson")
	}
	if legacyJSON {
		if explicit == formatTable {
			return "", validationErr("--json conflicts with --output table", "drop one of the two flags")
		}
		if explicit == "" {
			explicit = formatJSON
		}
	}
	if explicit == "" {
		if !stdoutTTY {
			if streaming {
				return formatNDJSON, nil
			}
			return formatJSON, nil
		}
		return formatTable, nil
	}
	if streaming && explicit == formatJSON {
		return "", validationErr("this command streams output and cannot emit a single JSON document", "use --output ndjson")
	}
	if !streaming && explicit == formatNDJSON {
		return "", validationErr("this command emits a single document, not a stream", "use --output json")
	}
	return explicit, nil
}
