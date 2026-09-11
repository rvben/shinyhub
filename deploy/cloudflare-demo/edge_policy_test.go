package cloudflaredemo

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/apporigin"
)

// The demo Worker rejects at the edge whatever the app origin would 404, so the
// container is never woken to produce a 404. That only stays true while the two
// allowlists agree, and they are written in different languages: this test is
// what keeps them one list.
func TestDemoWorkerAppOriginMatchesServer(t *testing.T) {
	source, err := os.ReadFile("src/edge-policy.ts")
	if err != nil {
		t.Fatal(err)
	}
	worker := string(source)

	workerPrefixes := stringLiterals(t, worker, `APP_ORIGIN_PREFIXES = \[([^\]]*)\]`)
	serverPrefixes := slices.Sorted(slices.Values(apporigin.Prefixes()))
	if !slices.Equal(workerPrefixes, serverPrefixes) {
		t.Errorf("Worker app-origin prefixes = %q, server serves %q", workerPrefixes, serverPrefixes)
	}

	workerExact := stringLiterals(t, worker, `APP_ORIGIN_EXACT = new Set\(\[([^\]]*)\]\)`)
	serverExact := slices.Sorted(slices.Values(apporigin.ExactPaths()))
	if !slices.Equal(workerExact, serverExact) {
		t.Errorf("Worker app-origin exact paths = %q, server serves %q", workerExact, serverExact)
	}
}

// The policy module is only worth anything if the Worker consults it before it
// reaches for the container, so this pins the call site and its position.
func TestDemoWorkerAppliesEdgePolicyBeforeReachingTheContainer(t *testing.T) {
	source, err := os.ReadFile("src/index.ts")
	if err != nil {
		t.Fatal(err)
	}
	worker := string(source)

	for _, required := range []string{
		`classifyEdgeRequest(url.hostname, url.pathname)`,
		`verdict === "serve-robots"`,
		`demoAsset(robotsBody, "text/plain; charset=utf-8")`,
		`verdict === "reject"`,
	} {
		if !strings.Contains(worker, required) {
			t.Errorf("demo Worker is missing edge admission contract %q", required)
		}
	}

	hostCheck := strings.Index(worker, "allowedHosts.has(url.hostname)")
	verdict := strings.Index(worker, "classifyEdgeRequest(url.hostname, url.pathname)")
	container := strings.Index(worker, "getContainer(env.SHINYHUB_DEMO")
	if hostCheck < 0 || verdict < 0 || container < 0 {
		t.Fatalf("demo Worker request flow is unrecognisable: host check %d, verdict %d, container %d",
			hostCheck, verdict, container)
	}
	if verdict < hostCheck {
		t.Error("the edge policy runs before the host check, so it classifies requests for hosts the Worker does not serve")
	}
	if verdict > container {
		t.Error("the edge policy runs after the container handle is taken, so a rejected request can still wake the container")
	}
}

var stringLiteral = regexp.MustCompile(`"([^"]*)"`)

// stringLiterals returns the sorted double-quoted strings inside the first
// capture group of pattern. A pattern that no longer matches fails the test
// rather than reading as an empty list.
func stringLiterals(t *testing.T, source, pattern string) []string {
	t.Helper()
	match := regexp.MustCompile(pattern).FindStringSubmatch(source)
	if match == nil {
		t.Fatalf("src/edge-policy.ts no longer declares %s", pattern)
	}
	var literals []string
	for _, quoted := range stringLiteral.FindAllStringSubmatch(match[1], -1) {
		literals = append(literals, quoted[1])
	}
	if len(literals) == 0 {
		t.Fatalf("%s declares no paths", pattern)
	}
	slices.Sort(literals)
	return literals
}
