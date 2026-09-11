package cloudflaredemo

import (
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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

	hostCheck := indexOf(t, worker, "src/index.ts", "allowedHosts.has(url.hostname)")
	verdict := indexOf(t, worker, "src/index.ts", "classifyEdgeRequest(url.hostname, url.pathname)")
	container := indexOf(t, worker, "src/index.ts", "getContainer(env.SHINYHUB_DEMO")
	if verdict < hostCheck {
		t.Error("the edge policy runs before the host check, so it classifies requests for hosts the Worker does not serve")
	}
	if verdict > container {
		t.Error("the edge policy runs after the container handle is taken, so a rejected request can still wake the container")
	}
}

// The cold-start gate is what stops background traffic from paying for a
// sleepAfter window, so this pins that the Worker consults it and that the
// release smoke test arrives the way the gate expects a visitor to.
func TestDemoWorkerSpendsColdStartsOnVisitorsOnly(t *testing.T) {
	source, err := os.ReadFile("src/index.ts")
	if err != nil {
		t.Fatal(err)
	}
	worker := string(source)

	for _, required := range []string{
		`await container.getState()`,
		`classifyColdRequest({`,
		`secFetchDest: request.headers.get("sec-fetch-dest")`,
		`accept: request.headers.get("accept")`,
		`coldVerdict === "refuse"`,
		`coldVerdict === "redirect-to-entry"`,
		`coldVerdict === "wake"`,
		`ctx.waitUntil(container.start()`,
		// Which statuses mean the container is down decides whether the
		// readiness endpoint may probe it, and probing one that is down is a
		// wake. Spelled inline it was a chain of comparisons no test could
		// reach; routed through the policy module, edge-policy.test.ts pins
		// every status in the runtime's union.
		`isAsleep(state.status)`,
	} {
		if !strings.Contains(worker, required) {
			t.Errorf("demo Worker is missing cold-start gate contract %q", required)
		}
	}

	// The entry page lives on the control host, so a redirect resolved against
	// the request would send an app-origin visitor to a URL the edge then
	// rejects as a static 404. Reading the first redirect after the branch, not
	// merely looking for the absolute one somewhere in the file, because the
	// session handler below issues a redirect of its own that would cover for
	// this one.
	entryBranch := indexOf(t, worker, "src/index.ts", `coldVerdict === "redirect-to-entry"`)
	rest := worker[entryBranch:]
	redirect := indexOf(t, rest, "the redirect-to-entry branch", "Response.redirect(")
	if !strings.HasPrefix(rest[redirect:], `Response.redirect(ENTRY_URL, 303)`) {
		t.Errorf("the redirect-to-entry branch does not redirect to the absolute entry URL, so an app-origin visitor is sent to a path its own host does not serve: %.60s", rest[redirect:])
	}

	// Reading container state is a round trip to the Durable Object in front of
	// every request, so the Worker consults its own recent observation first.
	handle := indexOf(t, worker, "src/index.ts", "getContainer(env.SHINYHUB_DEMO")
	memo := indexOf(t, worker, "src/index.ts", "mayAssumeAwake(lastHealthyAt, now)")
	stateRead := indexOf(t, worker, "src/index.ts", "await container.getState()")
	if memo < handle {
		t.Error("the awake memo is consulted before the container handle exists, so it cannot be gating the state read")
	}
	if memo > stateRead {
		t.Error("the awake memo is consulted after the state read, so every warm request still pays for the round trip")
	}

	// The readiness endpoint is polled by the wake page, so it must report the
	// container's state rather than acquire it.
	ready := indexOf(t, worker, "src/index.ts", `url.pathname === DEMO_READY_PATH`)
	refuseWhenAsleep := indexOf(t, worker, "src/index.ts", `if (asleep) {`)
	healthProbe := indexOf(t, worker, "src/index.ts", `new URL("/healthz", url)`)
	if refuseWhenAsleep < ready || refuseWhenAsleep > healthProbe {
		t.Error("the readiness endpoint probes the container before checking whether it is asleep, so polling the wake page wakes it")
	}

	smoke, err := os.ReadFile("../../scripts/demo-smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	smokeSource := string(smoke)
	for _, required := range []string{
		`--header 'Sec-Fetch-Dest: document'`,
		`--header 'Accept: text/html,application/xhtml+xml'`,
		`did not wake after`,
	} {
		if !strings.Contains(smokeSource, required) {
			t.Errorf("release smoke test no longer wakes the demo as a visitor: missing %q", required)
		}
	}
	wakeDefined := indexOf(t, smokeSource, "scripts/demo-smoke.sh", "\nwake() {")
	wakeCalled := indexOf(t, smokeSource, "scripts/demo-smoke.sh", "\nwake\n")
	firstProbe := indexOf(t, smokeSource, "scripts/demo-smoke.sh", `check "$base_url/healthz"`)
	if wakeCalled < wakeDefined {
		t.Error("the smoke test calls wake before defining it")
	}
	if wakeCalled > firstProbe {
		t.Error("the smoke test probes the demo before waking it, which the edge now refuses")
	}
}

// A container can crash before it ever reports healthy, and the page a visitor
// is left looking at polls a probe that deliberately never starts one. So no
// number of polls can recover it: the poll has to hand the visitor back to the
// gate that can, which is a real navigation. That recovery is written across two
// files, and the state the probe reports is the only thing joining them.
func TestDemoWakePageRecoversAContainerThatNeverCameUp(t *testing.T) {
	source, err := os.ReadFile("src/index.ts")
	if err != nil {
		t.Fatal(err)
	}
	worker := string(source)
	source, err = os.ReadFile("src/demo-wake.ts")
	if err != nil {
		t.Fatal(err)
	}
	pages := string(source)

	readyAt := indexOf(t, worker, "src/index.ts", "url.pathname === DEMO_READY_PATH")
	coldAt := indexOf(t, worker, "src/index.ts", "const coldVerdict = classifyColdRequest(")
	if readyAt > coldAt {
		t.Fatal("the cold gate is above the ready handler, so the slice below reads the wrong code")
	}
	handler := worker[readyAt:coldAt]

	// Down and not-ready-yet are different facts about the container, and only
	// one of them is worth navigating out of. Reported as one value they are
	// indistinguishable to the page, which is what left a crashed container
	// polling forever.
	downAt := indexOf(t, handler, "the ready handler", "if (asleep)")
	healthAt := indexOf(t, handler, "the ready handler", "healthResponse.ok")
	if healthAt < downAt {
		t.Fatal("the ready handler answers on health before it answers on sleep, so the windows below read the wrong branches")
	}
	down := quotedAfter(t, within(handler, downAt, 140), "the down branch of the ready handler", "demoStateResponse(")
	if strings.Contains(within(handler, healthAt, 140), down) {
		t.Errorf("a poll cannot tell a container that is down from one that is still starting: both answer %q, so the page cannot know that waiting is pointless", down)
	}

	// Bounded by the template's own end rather than by whatever is declared
	// after it: the other pages in this file render copy that reads like the
	// script's, so a slice running past the closing backtick is one those pages
	// can answer for.
	scriptAt := indexOf(t, pages, "src/demo-wake.ts", "const wakeScript")
	script := within(pages[scriptAt:], 0, indexOf(t, pages[scriptAt:], "the wake script", "\n`;"))
	// Read where the page compares the reported state, not where the word
	// appears: the script says "Still starting" in its own copy, so a check for
	// the bare word is answered by prose no browser ever acts on.
	compareAt := indexOf(t, script, "the wake script", "x-shinyhub-demo-state")
	if !strings.Contains(within(script, compareAt, 80), down) {
		t.Errorf("the wake page does not act on the %q the probe reports, so a container that never came up reads to it as one still starting: %.80s", down, script[compareAt:])
	}

	// The button says "try again", and the only thing worth trying again is the
	// request that can start a container: a navigation. Polling the probe once
	// more cannot, whatever the container is doing.
	listener := regexp.MustCompile(`retry\.addEventListener\('click', ([A-Za-z_$][\w$]*)\)`).FindStringSubmatch(script)
	if listener == nil {
		t.Fatal("the wake page's retry button is no longer wired to a named handler, so what it does cannot be read here")
	}
	recover := listener[1]
	listenerAt := indexOf(t, script, "the wake script", "retry.addEventListener")
	definedAt := indexOf(t, script, "the wake script", "const "+recover+" = ")
	body := within(script, definedAt, 260)
	if !strings.Contains(body, "location.") || !strings.Contains(body, "dataset.landing") {
		t.Errorf("the retry button does not reopen the demo, so pressing it on a container that never came up polls the same dead probe again: %.120s", body)
	}

	// A visitor who never presses it is the common case, so the poll takes the
	// same way out on its own, and it takes it on the state it just read rather
	// than anywhere else it might sit. Without this the page with no script at
	// all recovers, every four seconds, and the page with script never does.
	checkAt := indexOf(t, script, "the wake script", "const check = ")
	if compareAt < checkAt || compareAt > listenerAt {
		t.Fatal("the wake script reads the reported state outside its poll, so the window below reads the wrong code")
	}
	if !strings.Contains(within(script, compareAt, 160), recover+"(") {
		t.Errorf("the poll reads that the container is down and does nothing with it, so a container that never came up strands every visitor who does not press the button: %.160s", script[compareAt:])
	}
}

// The awake memo lets an isolate forward without re-reading container state, and
// its soundness rests entirely on the container still being up. What puts the
// container down is sleepAfter, declared in a different file, in a different
// unit, and never read by the memo itself. This reads both and pins the margin
// between them: a memo that crept up towards the sleep window would forward into
// a container that had already slept, buying the wake this gate exists to avoid.
func TestDemoWorkerAwakeMemoStaysFarBelowSleepAfter(t *testing.T) {
	worker, err := os.ReadFile("src/index.ts")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := os.ReadFile("src/edge-policy.ts")
	if err != nil {
		t.Fatal(err)
	}

	sleepMatch := regexp.MustCompile(`sleepAfter = "([^"]+)"`).FindStringSubmatch(string(worker))
	if sleepMatch == nil {
		t.Fatal(`src/index.ts no longer declares sleepAfter, so the memo's margin cannot be checked`)
	}
	sleepAfter, err := time.ParseDuration(sleepMatch[1])
	if err != nil {
		t.Fatalf("sleepAfter %q is not a duration Go can read: %v", sleepMatch[1], err)
	}

	memoMatch := regexp.MustCompile(`AWAKE_MEMO_MS = ([^;]+);`).FindStringSubmatch(string(policy))
	if memoMatch == nil {
		t.Fatal("src/edge-policy.ts no longer declares AWAKE_MEMO_MS")
	}
	memo := time.Duration(millisLiteral(t, memoMatch[1])) * time.Millisecond

	if memo <= 0 {
		t.Errorf("the awake memo is %s, which disables the fast path entirely", memo)
	}
	// A twentieth of the sleep window keeps the memo far enough from the moment
	// the container can go down that neither clock skew nor a slow request can
	// close the gap.
	if limit := sleepAfter / 20; memo > limit {
		t.Errorf("the awake memo is %s against a %s sleep timer, past the %s ceiling: an isolate may forward to a container that has already slept", memo, sleepAfter, limit)
	}
}

// millisLiteral evaluates the right-hand side of a TypeScript millisecond
// constant, accepting both a plain literal (5_000) and a product written for
// readability (10 * 60 * 1000), so the contract holds on the value rather than
// on how the value is spelled.
func millisLiteral(t *testing.T, expr string) int {
	t.Helper()
	total := 1
	for _, factor := range strings.Split(expr, "*") {
		n, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(factor), "_", ""))
		if err != nil {
			t.Fatalf("cannot read %q as a number of milliseconds: %v", strings.TrimSpace(expr), err)
		}
		total *= n
	}
	return total
}

// within returns up to n bytes of source from at, so a positional check reads a
// bounded window rather than everything that follows.
func within(source string, at, n int) string {
	if at+n > len(source) {
		return source[at:]
	}
	return source[at : at+n]
}

// quotedAfter returns the double-quoted string needle is immediately followed
// by, so a contract reads the value a call is given. Searching on past the call
// for the next quote anywhere would answer with some other line's string, which
// is a value no part of the contract ever chose.
func quotedAfter(t *testing.T, source, name, needle string) string {
	t.Helper()
	rest := source[indexOf(t, source, name, needle)+len(needle):]
	quoted := stringLiteral.FindStringSubmatchIndex(rest)
	if quoted == nil || quoted[0] != 0 {
		t.Fatalf("%s no longer passes a named state to %s, so the page has nothing to recognise: %.60s", name, needle, rest)
	}
	return rest[quoted[2]:quoted[3]]
}

// indexOf returns where needle starts in source, failing the test when it is
// absent. strings.Index answers -1 there, and -1 sorts below every real
// position, so an ordering assertion written on it holds whatever the file says.
func indexOf(t *testing.T, source, name, needle string) int {
	t.Helper()
	at := strings.Index(source, needle)
	if at < 0 {
		t.Fatalf("%s no longer contains %q, so the contract it anchors cannot be checked", name, needle)
	}
	return at
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
