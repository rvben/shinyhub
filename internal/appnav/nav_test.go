package appnav

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
)

// The shell a Shiny app actually serves: the marker is a node the app owns, so
// asserting the snippet lands after it AND before </body> bounds the insertion
// point on both sides. A one-sided check ("the snippet is present") passes when
// the snippet is spliced into the middle of the app's own markup.
const testShell = `<!DOCTYPE html><html><head><title>App</title></head>` +
	`<body><div id="app-root">content</div></body></html>`

func TestSpliceIntoBody(t *testing.T) {
	t.Run("lands between the app content and the closing tag", func(t *testing.T) {
		out, ok := SpliceIntoBody([]byte(testShell), "<!--SNIP-->")
		if !ok {
			t.Fatal("insertion declined on a well-formed shell")
		}
		s := string(out)
		snip := strings.Index(s, "<!--SNIP-->")
		content := strings.Index(s, `id="app-root"`)
		closing := strings.Index(s, "</body>")
		if snip < 0 {
			t.Fatal("snippet absent")
		}
		if !(content < snip && snip < closing) {
			t.Fatalf("snippet at %d must sit after the app content (%d) and before </body> (%d)", snip, content, closing)
		}
		// Nothing of the original may be lost: removing the snippet must give
		// the input back byte for byte.
		if restored := strings.Replace(s, "<!--SNIP-->", "", 1); restored != testShell {
			t.Fatalf("insertion altered the surrounding document:\n%s", restored)
		}
	})

	t.Run("uses the last closing tag", func(t *testing.T) {
		// A </body> inside a string literal or a comment earlier in the document
		// would otherwise capture the script and put it mid-page, where it runs
		// before the app has finished parsing.
		in := `<body><script>var s = "</body>";</script><p>x</p></body>`
		out, ok := SpliceIntoBody([]byte(in), "<!--SNIP-->")
		if !ok {
			t.Fatal("insertion declined")
		}
		s := string(out)
		if strings.Index(s, "<!--SNIP-->") < strings.Index(s, "<p>x</p>") {
			t.Fatalf("snippet went in at the first </body>, not the last: %s", s)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		out, ok := SpliceIntoBody([]byte("<BODY>hi</BODY>"), "<!--SNIP-->")
		if !ok {
			t.Fatal("uppercase closing tag was not recognised")
		}
		if !strings.HasSuffix(string(out), "<!--SNIP--></BODY>") {
			t.Fatalf("unexpected placement: %s", out)
		}
	})

	t.Run("declines a document without one", func(t *testing.T) {
		in := []byte(`{"json":true}`)
		out, ok := SpliceIntoBody(in, "<!--SNIP-->")
		if ok {
			t.Fatal("insertion claimed success with no </body> to insert before")
		}
		if !bytes.Equal(out, in) {
			t.Fatalf("declined insertion still altered the body: %s", out)
		}
	})

	t.Run("does not mutate the input", func(t *testing.T) {
		// The caller keeps the original buffer to restore the response body with
		// when a later step declines. Splicing in place would corrupt it.
		in := []byte(testShell)
		before := string(in)
		if _, ok := SpliceIntoBody(in, "<!--SNIP-->"); !ok {
			t.Fatal("insertion declined")
		}
		if string(in) != before {
			t.Fatalf("input buffer was mutated:\n%s", in)
		}
	})
}

func TestCSPHash_MatchesEmbeddedScript(t *testing.T) {
	// The hash is what a strict CSP checks the served bytes against. If it is
	// computed from anything but the embedded script, every app with a CSP
	// silently refuses to run the switcher and there is no other signal.
	sum := sha256.Sum256([]byte(Script))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if CSPHash != want {
		t.Fatalf("CSPHash = %s, want %s", CSPHash, want)
	}
}

func TestSnippet_CarriesTheScriptVerbatim(t *testing.T) {
	snippet := Snippet("demo", "https://hub.example.com/")
	if !strings.Contains(snippet, Script) {
		t.Fatal("snippet does not carry the embedded script verbatim; the CSP hash cannot match")
	}
	if !strings.HasSuffix(snippet, "</script>") {
		t.Fatalf("snippet is not a closed script tag: %q", snippet[max(0, len(snippet)-80):])
	}
}

func TestSnippet_BodyIsIdenticalAcrossApps(t *testing.T) {
	// One CSP hash admits the switcher fleet-wide only while the bytes between
	// the tags never vary. Per-page values must ride on attributes, which a hash
	// does not cover.
	a := Snippet("alpha", "https://hub.example.com/")
	b := Snippet("beta", "https://other.example.com/")

	bodyOf := func(s string) string {
		_, rest, ok := strings.Cut(s, ">")
		if !ok {
			t.Fatalf("no tag close in %q", s[:min(120, len(s))])
		}
		return rest
	}
	if bodyOf(a) != bodyOf(b) {
		t.Fatal("script body differs between apps; one CSP hash cannot admit both")
	}
}

func TestSnippet_EscapesAttributes(t *testing.T) {
	// A slug reaches this function from the URL. It is validated upstream, but
	// the escaping is what makes that a defence in depth rather than the only
	// thing standing between a crafted slug and script execution.
	got := Snippet(`x" onload="evil()`, `/" onload="evil()`)
	if strings.Contains(got, `onload="evil()`) {
		t.Fatalf("input escaped its attribute: %s", got[:min(300, len(got))])
	}
	if !strings.Contains(got, "&#34;") {
		t.Fatalf("quote was not escaped: %s", got[:min(300, len(got))])
	}
}

func TestSnippetWithName_CarriesFriendlyName(t *testing.T) {
	got := SnippetWithName("revenue-forecast", "Revenue & Forecast", "/")
	if !strings.Contains(got, `data-current-name="Revenue &amp; Forecast"`) {
		t.Fatalf("friendly name was not carried as an escaped attribute: %s", got[:min(300, len(got))])
	}
}

func TestSnippetWithGenerationCarriesVersionHandoffContract(t *testing.T) {
	got := SnippetWithGeneration("revenue", "Revenue", "/", "opaque-generation-token")
	for _, want := range []string{
		`data-served-generation="opaque-generation-token"`,
		`data-version-url="/app/revenue/.shinyhub/version.json"`,
		`data-switch-url="/app/revenue/.shinyhub/version/switch"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generation snippet missing %s", want)
		}
	}
}

func TestSnippet_DefaultsHomeToRoot(t *testing.T) {
	// A single-origin deployment has no separate dashboard host, so "/" is both
	// correct and what an empty configured base URL must become. An empty href
	// would render a link that reloads the app page instead.
	if !strings.Contains(Snippet("demo", ""), `data-home-url="/"`) {
		t.Fatal("empty home URL did not default to /")
	}
}

// The script and the payload are two halves of one contract that no compiler
// checks: the server marshals Go field tags, the script reads string keys, and a
// rename on either side produces undefined at the reader with no error anywhere.
// The jsdom suite cannot catch it either, because its fixtures are hand-written
// and would be renamed alongside the script.
//
// Every field is required to have a consumer. A field the script never reads is
// either dead weight on every app page load or a forgotten reader; both are
// resolved by a decision here, not by silence.
func TestWireContract_EveryPayloadFieldIsReadByTheScript(t *testing.T) {
	for _, spec := range []struct {
		what string
		typ  reflect.Type
	}{
		{"Payload", reflect.TypeOf(Payload{})},
		{"App", reflect.TypeOf(App{})},
	} {
		for i := 0; i < spec.typ.NumField(); i++ {
			field := spec.typ.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			// The property-access form, not the bare word: "name" and "slug"
			// occur all over a script that has local variables called both.
			if !strings.Contains(Script, "."+name) {
				t.Errorf("%s.%s marshals as %q, which assets/nav.js never reads: either the script lost its reader or the field is unused on the wire",
					spec.what, field.Name, name)
			}
		}
	}
}

// Apps own their layouts, so the switcher must offer more than one intentional
// placement rather than assuming the default top right is always clear. These
// selectors also pin the bar and its panel to the same placement model.
func TestScript_OffersFourAnchoredPlacements(t *testing.T) {
	for _, position := range []string{"top-center", "top-right", "left-center", "right-center"} {
		for _, surface := range []string{".bar", ".panel", ".restore"} {
			selector := "[data-position='" + position + "'] " + surface
			if !strings.Contains(Script, selector) {
				t.Errorf("assets/nav.js has no %q rule", selector)
			}
		}
	}
}

func TestDataURL(t *testing.T) {
	if got, want := DataURL("demo"), "/app/demo/.shinyhub/nav.json"; got != want {
		t.Fatalf("DataURL = %q, want %q", got, want)
	}
}

func TestOpenable(t *testing.T) {
	// Every status the apps table actually carries, so a new one cannot be added
	// without a decision being made here about whether it is a destination.
	cases := map[string]bool{
		"running":    true,
		"hibernated": true, // a request wakes it
		"waking":     true,
		"deploying":  true,
		"stopped":    false,
		"crashed":    false,
		"error":      false,
	}
	for status, want := range cases {
		if got := Openable(status); got != want {
			t.Fatalf("Openable(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestOpenable_UnknownStatusIsOffered(t *testing.T) {
	// An unrecognised status defaults to openable: the failure mode of offering
	// an app that turns out to be down is a wait page, while the failure mode of
	// hiding a healthy app is a visitor who cannot reach their work at all.
	if !Openable("some-future-status") {
		t.Fatal("an unknown status was treated as unopenable")
	}
}
