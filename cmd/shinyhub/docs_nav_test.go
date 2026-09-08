package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// readmeDocLink matches a Markdown link into docs/ in README.md, including the
// guide table rows near the end of the file.
var readmeDocLink = regexp.MustCompile(`\((docs/[A-Za-z0-9._/-]+\.md)[^)]*\)`)

// navTargets walks the decoded `nav` value and collects every ".md" path in it.
// The array is heterogeneous - a page is `{ "Title" = "page.md" }` and a section
// is `{ "Title" = [ ... ] }` - so recursion is simpler than a typed schema, and
// it keeps working if a section gains another level.
func navTargets(v any, out map[string]bool) {
	switch t := v.(type) {
	case string:
		if strings.HasSuffix(t, ".md") {
			out[t] = true
		}
	case []any:
		for _, e := range t {
			navTargets(e, out)
		}
	case map[string]any:
		for _, e := range t {
			navTargets(e, out)
		}
	}
}

// docsNav reads the published site's navigation and the set of pages under
// docs/.
func docsNav(t *testing.T, root string) (nav map[string]bool, pages []string) {
	t.Helper()

	var cfg struct {
		Project struct {
			Nav []any `toml:"nav"`
		} `toml:"project"`
	}
	if _, err := toml.DecodeFile(filepath.Join(root, "zensical.toml"), &cfg); err != nil {
		t.Fatal(err)
	}
	nav = map[string]bool{}
	navTargets(cfg.Project.Nav, nav)
	if len(nav) < 20 {
		t.Fatalf("found only %d pages in the zensical.toml nav; the nav or its shape changed", len(nav))
	}

	docsDir := filepath.Join(root, "docs")
	err := filepath.WalkDir(docsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Local planning material is gitignored and is not published documentation.
		if d.IsDir() && d.Name() == "superpowers" {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, relErr := filepath.Rel(docsDir, path)
		if relErr != nil {
			return relErr
		}
		pages = append(pages, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) < 20 {
		t.Fatalf("found only %d pages under docs/; the scan is broken", len(pages))
	}
	return nav, pages
}

// A doc that is not in the nav is a doc nobody reads. The site builds its
// sidebar from zensical.toml, so a page added to docs/ without a nav entry ships
// invisibly: it is published, it is in search, and no path through the
// navigation reaches it. Three pages had drifted out this way, one of them a
// security-relevant admin capability whose only inbound link anywhere was an
// inline mention inside an unrelated page.
//
// A link from README.md deliberately does NOT excuse a missing nav entry. The
// README is the GitHub front page, a different surface with a different
// audience; all three orphans were already linked there and were still
// unreachable on the site.
func TestEveryDocIsInTheSiteNav(t *testing.T) {
	nav, pages := docsNav(t, "../..")

	var missing []string
	present := map[string]bool{}
	for _, p := range pages {
		present[p] = true
		if !nav[p] {
			missing = append(missing, p)
		}
	}

	// The other direction: a nav entry pointing at a page that was renamed or
	// deleted is a dead link in the published sidebar.
	var dangling []string
	for p := range nav {
		if !present[p] {
			dangling = append(dangling, p)
		}
	}

	sort.Strings(missing)
	sort.Strings(dangling)
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf(
			"docs pages missing from the zensical.toml nav: %v", missing))
	}
	if len(dangling) > 0 {
		problems = append(problems, fmt.Sprintf(
			"zensical.toml nav points at pages that do not exist: %v", dangling))
	}
	if len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
}

// The README guide table is the other front door, and its links rot the same
// way: a renamed page leaves a 404 on the repository's landing page, where it is
// the first thing a newcomer clicks.
func TestReadmeDocLinksResolve(t *testing.T) {
	root := "../.."

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	matches := readmeDocLink.FindAllStringSubmatch(string(readme), -1)
	if len(matches) < 10 {
		t.Fatalf("found only %d links into docs/ in README.md; the link format changed", len(matches))
	}

	seen := map[string]bool{}
	var broken []string
	for _, m := range matches {
		rel := m[1]
		if seen[rel] {
			continue
		}
		seen[rel] = true
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			broken = append(broken, rel)
		}
	}
	sort.Strings(broken)
	if len(broken) > 0 {
		t.Fatalf("README.md links to docs pages that do not exist: %v", broken)
	}
}
