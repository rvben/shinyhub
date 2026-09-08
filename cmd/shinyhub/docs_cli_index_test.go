package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// docsCLITableRow matches a row of the command index in docs/cli.md, whose
// first cell is a command name in backticks.
var docsCLITableRow = regexp.MustCompile("(?m)^\\|\\s*`([a-z][a-z0-9-]*)`\\s*\\|")

// A hand-written command index goes stale in silence: a new command simply is
// not listed, and nothing about adding one points at the doc. Pinning the index
// to the command tree turns that omission into a build failure.
func TestDocsCLIIndexListsEveryTopLevelCommand(t *testing.T) {
	doc, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}

	documented := map[string]bool{}
	for _, m := range docsCLITableRow.FindAllStringSubmatch(string(doc), -1) {
		documented[m[1]] = true
	}
	if len(documented) == 0 {
		t.Fatal("found no command rows in docs/cli.md; the index or its table format changed")
	}

	var missing, wantHelp []string
	actual := map[string]bool{}
	for _, cmd := range buildRoot().Commands() {
		name := cmd.Name()
		// `help` is cobra's own, and hidden commands are deliberately not
		// part of the documented surface.
		if cmd.Hidden || name == "help" {
			if documented[name] {
				wantHelp = append(wantHelp, name)
			}
			continue
		}
		actual[name] = true
		if !documented[name] {
			missing = append(missing, name)
		}
	}

	var stale []string
	for name := range documented {
		if !actual[name] {
			stale = append(stale, name)
		}
	}

	sort.Strings(missing)
	sort.Strings(stale)
	sort.Strings(wantHelp)
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf("commands missing from docs/cli.md: %v", missing))
	}
	if len(stale) > 0 {
		problems = append(problems, fmt.Sprintf("docs/cli.md lists commands that no longer exist: %v", stale))
	}
	if len(wantHelp) > 0 {
		problems = append(problems, fmt.Sprintf("docs/cli.md lists hidden or built-in commands: %v", wantHelp))
	}
	if len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
}
