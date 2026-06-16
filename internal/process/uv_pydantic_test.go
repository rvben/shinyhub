package process

import "testing"

func TestRequirementDistName(t *testing.T) {
	cases := map[string]string{
		"shiny":                        "shiny",
		"shiny>=1.2":                   "shiny",
		"shiny==1.2.0":                 "shiny",
		"shiny[theme]>=1.2":            "shiny",
		"  Shiny  ":                    "shiny",
		"shinywidgets>=0.3":            "shinywidgets",
		"shiny-semantic":               "shiny-semantic",
		"httpx>=0.27":                  "httpx",
		"pkg ; python_version>='3.10'": "pkg",
		"pkg @ git+https://example/x":  "pkg",
		"# a comment":                  "",
		"-r other.txt":                 "",
		"--index-url https://example":  "",
		"":                             "",
	}
	for in, want := range cases {
		if got := requirementDistName(in); got != want {
			t.Errorf("requirementDistName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRequirementsImplyPydantic(t *testing.T) {
	yes := []string{
		"shiny",
		"shiny>=1.2\nhttpx",
		"httpx\npandas\nshiny[theme]>=1.2",
		"Shiny==1.4",
	}
	no := []string{
		"httpx\npandas",
		"shinywidgets>=0.3", // a different package
		"shiny-semantic",    // a different package
		"# shiny in a comment",
		"",
	}
	for _, r := range yes {
		if !requirementsImplyPydantic(r) {
			t.Errorf("requirementsImplyPydantic(%q) = false, want true", r)
		}
	}
	for _, r := range no {
		if requirementsImplyPydantic(r) {
			t.Errorf("requirementsImplyPydantic(%q) = true, want false", r)
		}
	}
}
