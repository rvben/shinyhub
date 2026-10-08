package envui

import (
	"encoding/json"
	"github.com/rvben/shinyhub/internal/config"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestEnvironmentProductionLinks(t *testing.T) {
	data, err := os.ReadFile("../../testdata/environment-links.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Current, Target string }
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	e := &config.EnvironmentConfig{Label: "Acceptance", ProductionURL: "https://production.example"}
	for _, tc := range cases {
		t.Run(tc.Current, func(t *testing.T) {
			u, err := url.Parse(tc.Current)
			if err != nil {
				t.Fatal(err)
			}
			if got := ProductionHref(e, u); got != e.ProductionURL+tc.Target {
				t.Fatalf("got %s, want %s", got, e.ProductionURL+tc.Target)
			}
		})
	}
}
func TestEnvironmentSnippetEscapesAttributes(t *testing.T) {
	e := &config.EnvironmentConfig{Label: `A"><img src=x>`, Message: `</script><script>bad()</script>`}
	got := Snippet(e, nil, true)
	if strings.Count(got, "<script") != 1 || strings.Contains(got, `data-label="A"><`) {
		t.Fatal("attribute injection")
	}
	if Snippet(nil, nil, true) != "" {
		t.Fatal("unset environment should produce no markup")
	}
}

func TestEnvironmentScriptAndAttributesAreCharsetIndependent(t *testing.T) {
	for _, r := range Script {
		if r > 127 {
			t.Fatal("script must be ASCII so its CSP hash survives legacy app charsets")
		}
	}
	got := Snippet(&config.EnvironmentConfig{Label: "Acceptación", Message: "€ test"}, nil, true)
	for _, r := range got {
		if r > 127 {
			t.Fatal("snippet contains non-ASCII attributes")
		}
	}
	if !strings.Contains(got, "&#243;") || !strings.Contains(got, "&#8364;") {
		t.Fatal("missing unicode entities")
	}
}
