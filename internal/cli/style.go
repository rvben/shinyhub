package cli

import (
	"io"
	"os"
	"strings"
)

// noColorFlag holds the global --no-color flag. Registered as a root persistent
// flag in AddCommandsTo.
var noColorFlag bool

// ANSI select-graphic-rendition sequences. The palette is deliberately small:
// color is always additive to a word or glyph that already carries the meaning,
// so a monochrome terminal loses decoration, never information.
const (
	ansiReset   = "\x1b[0m"
	ansiDim     = "\x1b[2m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiReverse = "\x1b[7m"
)

// styler renders text for one specific writer. It carries two independent
// switches because they answer different questions:
//
//   - color: may this writer receive ANSI color? Off for anything that is not a
//     terminal, and off when the user asked for no color.
//   - tty: is this writer a terminal at all? Gates decoration that is not color
//     (status glyphs, the progress spinner). NO_COLOR asks for no color, not for
//     a different layout, so a glyph survives it.
//   - redraw: may this writer receive cursor control (carriage return plus
//     erase-line)? A dumb terminal is still a terminal but renders those escapes
//     literally, so it gets the line-per-step form instead of an animation.
//
// All are false for an io.Writer that is not an *os.File, which is what every
// test passes, so command output captured into a buffer is never decorated and
// stays byte-identical to the pre-styling output.
type styler struct {
	color  bool
	tty    bool
	redraw bool
	ascii  bool
}

// stylerFor returns the styler that applies to w.
func stylerFor(w io.Writer) styler {
	f, isFile := w.(*os.File)
	if !isFile {
		return styler{}
	}
	tty := isTTY(f)
	return styler{
		color:  colorEnabledFor(tty),
		tty:    tty,
		redraw: tty && os.Getenv("TERM") != "dumb",
		ascii:  !utf8Locale(),
	}
}

// colorEnabledFor decides whether ANSI color may be emitted to a writer that is
// a terminal (tty) or not, applying the conventional precedence:
//
//  1. --no-color              off  (explicit request wins over everything)
//  2. NO_COLOR set            off  (https://no-color.org)
//  3. CLICOLOR=0              off  (BSD convention)
//  4. TERM=dumb               off  (terminal cannot render attributes)
//  5. CLICOLOR_FORCE/FORCE_COLOR  on   (color into a pipe, for CI logs)
//  6. writer is a terminal    on
//  7. otherwise               off
//
// The force variables only override the terminal test, never an explicit
// opt-out, and stylerFor still requires an *os.File - so a stray FORCE_COLOR in
// the environment can never colorize a test's bytes.Buffer.
func colorEnabledFor(tty bool) bool {
	if noColorFlag {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("CLICOLOR") == "0" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	if envSetNonZero("CLICOLOR_FORCE") || envSetNonZero("FORCE_COLOR") {
		return true
	}
	return tty
}

// envSetNonZero reports whether name is set to anything other than "" or "0".
// The force variables are conventionally read as booleans, so FORCE_COLOR=0
// means "do not force" rather than "force".
func envSetNonZero(name string) bool {
	v := os.Getenv(name)
	return v != "" && v != "0"
}

// utf8Locale reports whether the environment declares a UTF-8 locale, deciding
// between Unicode and ASCII glyphs. The first locale variable that is set
// decides, matching POSIX precedence. When none is set the answer is yes: an
// unset locale is the default on a modern terminal, and assuming ASCII there
// would downgrade output that renders correctly today. Only an explicit
// non-UTF-8 locale (LANG=C, LC_ALL=POSIX) selects the ASCII glyphs.
func utf8Locale() bool {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		v := os.Getenv(name)
		if v == "" {
			continue
		}
		return strings.Contains(strings.ToUpper(v), "UTF-8") ||
			strings.Contains(strings.ToUpper(v), "UTF8")
	}
	return true
}

func (s styler) paint(code, v string) string {
	if !s.color || v == "" {
		return v
	}
	return code + v + ansiReset
}

func (s styler) dim(v string) string     { return s.paint(ansiDim, v) }
func (s styler) red(v string) string     { return s.paint(ansiRed, v) }
func (s styler) green(v string) string   { return s.paint(ansiGreen, v) }
func (s styler) yellow(v string) string  { return s.paint(ansiYellow, v) }
func (s styler) reverse(v string) string { return s.paint(ansiReverse, v) }

// status paints a lifecycle word by what it means for the operator: green for a
// steady healthy state, yellow for a transition that will resolve on its own,
// red for a failure, dim for a state that is off on purpose or standing by
// (idle: a healthy elastic pool waiting for its first request). An unrecognized
// word is returned unchanged rather than guessed at, so a status the server
// grows later renders plainly instead of being miscolored.
func (s styler) status(word string) string {
	switch word {
	case "running", "succeeded", "success", "ready", "active", "enabled", "healthy", "ok", "created", "updated", "added", "switched":
		return s.green(word)
	case "failed", "failure", "crashed", "error", "unhealthy", "conflict", "rejected", "expired", "revoked":
		return s.red(word)
	case "deploying", "starting", "restarting", "stopping", "pending", "warming", "booting", "provisioning", "running_now":
		return s.yellow(word)
	case "stopped", "hibernated", "sleeping", "idle", "disabled", "unknown", "unmanaged", "skipped", "unchanged", "never", "none", "-":
		return s.dim(word)
	}
	return word
}

// Glyphs. Each Unicode form and its ASCII fallback are one column wide so a
// glyph column stays aligned in either locale.

func (s styler) glyphOK() string {
	if s.ascii {
		return "v"
	}
	return "✓"
}

func (s styler) glyphFail() string {
	if s.ascii {
		return "x"
	}
	return "✗"
}

// glyphEllipsis marks text that was cut short, so a clipped value cannot be
// read as a shorter value that exists.
func (s styler) glyphEllipsis() string {
	if s.ascii {
		return "~"
	}
	return "…"
}

// glyphPaint colors a fleet report glyph by what it stands for, leaving the
// glyph itself untouched. The glyph set is fixed by the report's printed
// legend, so it is matched literally rather than reinterpreted here; an
// unrecognized glyph is returned as-is.
func (s styler) glyphPaint(g string) string {
	switch g {
	case "✗":
		return s.red(g)
	case "+", "~", ">":
		return s.green(g)
	case "•", "=", "-":
		return s.dim(g)
	}
	return g
}

// okPrefix returns the success marker that precedes a confirmation sentence, or
// "" for a non-terminal writer. It is empty when piped so the prose a script
// greps for is unchanged by this package.
func (s styler) okPrefix() string {
	if !s.tty {
		return ""
	}
	return s.green(s.glyphOK()) + " "
}

// failPrefix is okPrefix's counterpart for a failure sentence.
func (s styler) failPrefix() string {
	if !s.tty {
		return ""
	}
	return s.red(s.glyphFail()) + " "
}

// failMark returns the failure glyph painted red. Unlike failPrefix it is
// present for a non-terminal writer too: it marks an item in a problem list,
// where the glyph is part of the list's shape rather than an ornament on a
// sentence, and dropping it when piped would change the layout.
//
// Prefer this over composing glyphPaint with glyphFail. glyphPaint matches the
// Unicode glyph literally, so in an ASCII locale it would be handed "x", fall
// through its switch, and return it unpainted.
func (s styler) failMark() string { return s.red(s.glyphFail()) }
