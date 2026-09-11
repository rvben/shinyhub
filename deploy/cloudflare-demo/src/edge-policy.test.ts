import { test } from "node:test";
import assert from "node:assert/strict";

import {
  APP_HOST,
  appOriginAdmits,
  AWAKE_MEMO_MS,
  classifyColdRequest,
  classifyEdgeRequest,
  DEMO_HOST,
  DEMO_NEXT_PARAM,
  DEMO_START_PATH,
  demoURL,
  ENTRY_URL,
  isAsleep,
  mayAssumeAwake,
  requestedDestination,
  robotsBody,
  safeDestination,
} from "./edge-policy.ts";

const navigation = { secFetchDest: "document", secFetchSite: "none", accept: "text/html,application/xhtml+xml" };
const programmatic = { secFetchDest: null, secFetchSite: null, accept: "*/*" };

test("app origin admits proxied app paths", () => {
  assert.equal(appOriginAdmits("/app/streamlit-demo/"), true);
  assert.equal(appOriginAdmits("/app/bookmarking-demo/websocket/"), true);
});

test("app origin admits the probe and favicon paths", () => {
  assert.equal(appOriginAdmits("/healthz"), true);
  assert.equal(appOriginAdmits("/readyz"), true);
  assert.equal(appOriginAdmits("/favicon.ico"), true);
});

test("app origin rejects control-plane paths", () => {
  assert.equal(appOriginAdmits("/"), false);
  assert.equal(appOriginAdmits("/api/user/login"), false);
  assert.equal(appOriginAdmits("/api/auth/session"), false);
  assert.equal(appOriginAdmits("/.env"), false);
});

test("app origin rejects a path that merely starts with an admitted name", () => {
  assert.equal(appOriginAdmits("/healthz-probe"), false);
  assert.equal(appOriginAdmits("/readyz.json"), false);
  assert.equal(appOriginAdmits("/favicon.ico.map"), false);
  assert.equal(appOriginAdmits("/app"), false);
  assert.equal(appOriginAdmits("/apps"), false);
  assert.equal(appOriginAdmits("/application"), false);
});

test("app origin rejects a path carrying an admitted prefix anywhere but the start", () => {
  assert.equal(appOriginAdmits("/static/app/bundle.js"), false);
  assert.equal(appOriginAdmits("/x/app/y"), false);
});

// A container that is down charges nothing until something touches it, and
// touching it commits to a whole sleepAfter window. The readiness endpoint reads
// this to decide whether it may run a health probe, so a status wrongly read as
// awake turns polling the wake page into the thing that wakes the container.
test("every status in which the container is down reads as asleep", () => {
  assert.equal(isAsleep("stopped"), true);
  assert.equal(isAsleep("stopped_with_code"), true);
  // Mid-shutdown still bills, and probing it races the shutdown into a restart.
  assert.equal(isAsleep("stopping"), true);
});

test("a container that is up is never treated as asleep", () => {
  assert.equal(isAsleep("healthy"), false);
  assert.equal(isAsleep("running"), false);
});

// The states above are the whole union the container runtime reports. An
// unrecognised one is more likely a runtime that gained a state than a container
// that is down, and guessing asleep would refuse visitors a running container
// could serve.
test("an unrecognised status is not assumed to be asleep", () => {
  assert.equal(isAsleep("starting"), false);
  assert.equal(isAsleep(""), false);
});

test("robots.txt is answered at the edge on both hosts", () => {
  assert.equal(classifyEdgeRequest(DEMO_HOST, "/robots.txt"), "serve-robots");
  assert.equal(classifyEdgeRequest(APP_HOST, "/robots.txt"), "serve-robots");
});

test("robots.txt disallows every crawler", () => {
  assert.match(robotsBody, /^User-agent: \*$/m);
  assert.match(robotsBody, /^Disallow: \/$/m);
});

test("the app origin rejects what its server would 404", () => {
  assert.equal(classifyEdgeRequest(APP_HOST, "/api/user/login"), "reject");
  assert.equal(classifyEdgeRequest(APP_HOST, "/.env"), "reject");
  assert.equal(classifyEdgeRequest(APP_HOST, "/app/streamlit-demo/"), "forward");
});

test("the control host still forwards its own routes", () => {
  assert.equal(classifyEdgeRequest(DEMO_HOST, "/"), "forward");
  assert.equal(classifyEdgeRequest(DEMO_HOST, "/api/auth/session"), "forward");
  assert.equal(classifyEdgeRequest(DEMO_HOST, "/apps/demo/logs"), "forward");
});

test("a browser arriving at an entry page wakes a sleeping container", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/",
    ...navigation,
  }), "wake");
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/login",
    ...navigation,
  }), "wake");
});

// An entry page is never refused. The start page is rendered at the edge and
// costs nothing, so an unfurler previewing a shared link or a monitor watching
// the hostname gets a real 200 describing a demo that is merely asleep, rather
// than an error. What none of them gets is the container.
test("an entry page fetched without a browser navigation is offered the start page", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/",
    ...programmatic,
  }), "start");
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/login",
    ...programmatic,
  }), "start");
});

test("a head request on an entry page is answered from the edge", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "HEAD",
    pathname: "/",
    ...navigation,
  }), "start");
});

// An Accept header is the only navigation signal a plain HTTP crawler can
// produce, and it produces it by default: a library asking for a page sends
// text/html without being told to. Sec-Fetch-Dest is generated by the browser
// itself and cannot be forged from outside one, so it is what the decision to
// spend a sleepAfter window rests on.
test("an accept header alone is not enough to spend a cold start", () => {
  for (const accept of [
    "text/html,application/xhtml+xml;q=0.9",
    "text/html",
    "*/*, text/html",
  ]) {
    assert.equal(classifyColdRequest({
      hostname: DEMO_HOST,
      method: "GET",
      pathname: "/",
      secFetchDest: null,
      accept,
    }), "start", accept);
  }
});

// A sub-resource fetch carries Sec-Fetch-Dest too, so the header is only worth
// trusting when it says document.
test("a browser fetch that is not a navigation is offered the start page", () => {
  for (const secFetchDest of ["empty", "iframe", "image", "script"]) {
    assert.equal(classifyColdRequest({
      hostname: DEMO_HOST,
      method: "GET",
      pathname: "/",
      secFetchDest,
      accept: "text/html",
    }), "start", secFetchDest);
  }
});

// Crawlers, unfurlers and uptime monitors all issue GETs and parse what comes
// back; none of them submits a form. The start page's button is therefore the
// one way into the container that does not rest on a header a crawler sends by
// default, which is what makes it safe to honour with no headers at all.
test("the start page's button wakes the container", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "POST",
    pathname: DEMO_START_PATH,
    secFetchDest: null,
    accept: null,
  }), "wake");
});

// A post needs no headers to be believed, which also means any page on the web
// can make a visitor's browser send one. A browser says so when it does:
// Sec-Fetch-Site reads cross-site on a form another origin submitted here, and
// nothing legitimate posts this path from off-site. The start page is what that
// request is answered with, so a person who followed a link that tried it still
// sees the demo and can start it themselves.
test("a cross-site post to the start path is offered the start page rather than the container", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "POST",
    pathname: DEMO_START_PATH,
    secFetchDest: "document",
    secFetchSite: "cross-site",
    accept: "text/html",
  }), "start");
});

test("the start page's own button still wakes the container", () => {
  for (const secFetchSite of ["same-origin", "same-site", "none", null]) {
    assert.equal(classifyColdRequest({
      hostname: DEMO_HOST,
      method: "POST",
      pathname: DEMO_START_PATH,
      secFetchDest: "document",
      secFetchSite,
      accept: "text/html",
    }), "wake", String(secFetchSite));
  }
});

// Only the post is judged on where it came from. A shared link is cross-site by
// definition, and following one is how most visitors arrive.
test("a visitor arriving from a link on another site wakes the container", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/",
    ...navigation,
    secFetchSite: "cross-site",
  }), "wake");
});

test("the start path wakes nothing when it is merely fetched", () => {
  for (const method of ["GET", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS"]) {
    assert.notEqual(classifyColdRequest({
      hostname: DEMO_HOST,
      method,
      pathname: DEMO_START_PATH,
      ...navigation,
    }), "wake", method);
  }
});

test("the start button is powerless on the app origin", () => {
  assert.equal(classifyColdRequest({
    hostname: APP_HOST,
    method: "POST",
    pathname: DEMO_START_PATH,
    ...navigation,
  }), "refuse");
});

// The one-click entry posts a form and has nowhere to render a wake page, so it
// is sent to the entry page either way. Answering it with a redirect rather than
// a wake costs the visitor nothing: the navigation that follows is itself an
// entry-path GET and wakes the container one round trip later. What it buys is
// that the cold start is never spent on the POST itself, which carries no
// evidence a browser sent it.
test("the one-click demo entry is sent to the entry page rather than spending the cold start", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "POST",
    pathname: "/__demo/session",
    secFetchDest: "empty",
    accept: "*/*",
  }), "redirect-to-entry");
});

// A POST needs no navigation headers to be accepted, so if it could wake the
// container then one unadorned request would spend a whole sleepAfter window.
test("a bare scripted post to the session path never wakes the container", () => {
  assert.notEqual(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "POST",
    pathname: "/__demo/session",
    secFetchDest: null,
    accept: null,
  }), "wake");
});

// The redirect the session POST receives has to land somewhere that does wake
// the container, or the one-click entry becomes a loop that never boots it.
test("the entry page the session post is sent to is itself allowed to wake it", () => {
  const entry = new URL(ENTRY_URL);
  assert.equal(classifyColdRequest({
    hostname: entry.hostname,
    method: "GET",
    pathname: entry.pathname,
    ...navigation,
  }), "wake");
});

test("probes and control-plane calls leave the container asleep", () => {
  for (const pathname of ["/healthz", "/readyz", "/api/user/login", "/api/auth/session", "/.env"]) {
    assert.equal(classifyColdRequest({
      hostname: DEMO_HOST,
      method: "GET",
      pathname,
      ...programmatic,
    }), "refuse", pathname);
  }
});

test("a control-plane call dressed as a navigation leaves the container asleep", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/api/user/login",
    ...navigation,
  }), "redirect-to-entry");
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "POST",
    pathname: "/api/user/login",
    ...navigation,
  }), "refuse");
});

test("a cold deep link is sent to the entry page rather than refused", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/apps/operations-dashboard",
    ...navigation,
  }), "redirect-to-entry");
});

test("a cold app deep link is sent to the entry page rather than refused", () => {
  assert.equal(classifyColdRequest({
    hostname: APP_HOST,
    method: "GET",
    pathname: "/app/streamlit-demo/",
    ...navigation,
  }), "redirect-to-entry");
});

test("app traffic never wakes the container on its own", () => {
  for (const request of [
    { pathname: "/app/streamlit-demo/", method: "GET", ...programmatic },
    { pathname: "/healthz", method: "GET", ...programmatic },
    { pathname: "/app/bookmarking-demo/websocket/", method: "GET", ...programmatic },
    { pathname: "/app/streamlit-demo/upload", method: "POST", ...navigation },
  ]) {
    assert.equal(
      classifyColdRequest({ hostname: APP_HOST, ...request }),
      "refuse",
      `${request.method} ${request.pathname}`,
    );
  }
});

// The Worker answers a wake verdict with the wake page, which a browser renders
// while it waits, so a wake only ever goes to something with somewhere to render
// it: a document navigation, or the form submission from the start page, which
// replaces the page it was posted from.
test("nothing but an entry navigation or the start button wakes the container", () => {
  for (const method of ["POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"]) {
    for (const pathname of ["/", "/login", "/__demo/session", "/app/streamlit-demo/"]) {
      for (const hostname of [DEMO_HOST, APP_HOST]) {
        assert.notEqual(
          classifyColdRequest({ hostname, method, pathname, ...navigation }),
          "wake",
          `${method} ${hostname}${pathname}`,
        );
      }
    }
  }
});

// Every path the container can be reached on, crossed with every kind of sender
// and every method. Two things wake it, an entry page a browser navigated to and
// the start page's button, and the list below is every request that does either,
// so widening the gate by one case fails here rather than showing up on the bill.
test("the whole surface offers exactly two ways to spend a cold start", () => {
  // Named rather than described by their headers, because "crawler" is the
  // shape the gate exists for: no Sec-Fetch-Dest, because no browser generated
  // it, and Accept: text/html, because it is asking for a page. It is a
  // navigation by every signal a plain HTTP client can produce.
  const senders = {
    browser: navigation,
    offsite: { ...navigation, secFetchSite: "cross-site" },
    crawler: { secFetchDest: null, secFetchSite: null, accept: "text/html,application/xhtml+xml;q=0.9" },
    script: programmatic,
    subresource: { secFetchDest: "empty", secFetchSite: "same-origin", accept: "text/html" },
  };
  const woken: string[] = [];
  for (const hostname of [DEMO_HOST, APP_HOST]) {
    for (const method of ["GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS"]) {
      for (const pathname of ["/", "/login", DEMO_START_PATH, "/__demo/session", "/apps/x", "/app/streamlit-demo/", "/healthz"]) {
        for (const [sender, dest] of Object.entries(senders)) {
          if (classifyColdRequest({ hostname, method, pathname, ...dest }) === "wake") {
            woken.push(`${sender}: ${method} ${hostname}${pathname}`);
          }
        }
      }
    }
  }
  assert.deepEqual(woken.sort(), [
    `browser: GET ${DEMO_HOST}/`,
    `browser: GET ${DEMO_HOST}/login`,
    `browser: POST ${DEMO_HOST}${DEMO_START_PATH}`,
    `crawler: POST ${DEMO_HOST}${DEMO_START_PATH}`,
    // A link from another site is how most visitors arrive, so following one
    // wakes the demo. Its button posted from another site does not: that is the
    // one request a page elsewhere can make a visitor's browser send here.
    `offsite: GET ${DEMO_HOST}/`,
    `offsite: GET ${DEMO_HOST}/login`,
    `script: POST ${DEMO_HOST}${DEMO_START_PATH}`,
    `subresource: POST ${DEMO_HOST}${DEMO_START_PATH}`,
  ]);
});

test("a deep link asks for the page it names", () => {
  assert.equal(requestedDestination("/app/streamlit-demo/", ""), "/app/streamlit-demo/");
  assert.equal(requestedDestination("/apps/operations-dashboard", "?tab=logs"), "/apps/operations-dashboard?tab=logs");
});

// The start page posts to a path of the Worker's own, and the wake page's
// no-script refresh re-requests that path as a GET. Reading the path as the
// destination there would land the visitor back on the machinery instead of on
// the page they followed a link to.
test("a hop of the cold path asks for its threaded destination, not its own path", () => {
  assert.equal(requestedDestination(DEMO_START_PATH, `?${DEMO_NEXT_PARAM}=/app/streamlit-demo/`), "/app/streamlit-demo/");
  assert.equal(requestedDestination(DEMO_START_PATH, ""), null);
  assert.equal(requestedDestination("/__demo/ready", ""), null);
});

test("a threaded destination that could leave the demo is not inherited from the path", () => {
  assert.equal(requestedDestination("/apps/x", `?${DEMO_NEXT_PARAM}=//evil.example`), null);
});

test("a threaded destination is kept only when it stays on the demo", () => {
  assert.equal(safeDestination("/app/streamlit-demo/"), "/app/streamlit-demo/");
  assert.equal(safeDestination("/apps/operations-dashboard?tab=logs"), "/apps/operations-dashboard?tab=logs");
});

// The destination is read off a query string a stranger writes and handed to a
// redirect, so anything that could resolve to another origin is dropped rather
// than repaired. The URL parser strips tabs and reads a backslash as a slash, so
// the shape of the input settles nothing; what decides is the resolved origin
// and the value that actually comes back.
test("a destination that could leave the demo is dropped", () => {
  for (const raw of [
    "//evil.example",
    "https://evil.example/",
    "http://evil.example/",
    "/\t/evil.example",
    "/\\evil.example",
    "\\\\evil.example",
    "app/streamlit-demo/",
    "",
  ]) {
    assert.equal(safeDestination(raw), null, JSON.stringify(raw));
  }
  assert.equal(safeDestination(null), null);
});

// A path that resolves onto the control host can still come back beginning with
// two slashes, because the parser removes "." and ".." segments after deciding
// the origin. That value is a path to this function and a protocol-relative URL
// to every browser, so it is the returned string, not the resolved origin, that
// has the last word: a Location header carrying //evil.example leaves the demo
// however same-origin the input looked.
test("a destination that normalises into a protocol-relative URL is dropped", () => {
  for (const raw of ["/.//evil.example", "/..//evil.example", "/././/evil.example", "/foo/..//evil.example"]) {
    assert.equal(safeDestination(raw), null, JSON.stringify(raw));
  }
});

test("a threaded destination rides the entry URL as a query parameter", () => {
  const url = new URL(demoURL("/", "/app/streamlit-demo/"));
  assert.equal(url.origin, `https://${DEMO_HOST}`);
  assert.equal(url.pathname, "/");
  assert.equal(url.searchParams.get(DEMO_NEXT_PARAM), "/app/streamlit-demo/");
});

test("a URL built without a destination carries no query at all", () => {
  assert.equal(demoURL("/", null), ENTRY_URL);
  assert.equal(demoURL("/", "/"), ENTRY_URL);
});

// Every URL the visitor is sent to is built on the control host from a validated
// path, so a destination can never carry them somewhere the demo does not own.
test("an unsafe destination is dropped rather than carried", () => {
  assert.equal(demoURL("/", "https://evil.example/"), ENTRY_URL);
  assert.equal(demoURL("/", "//evil.example"), ENTRY_URL);
  assert.equal(new URL(demoURL(DEMO_START_PATH, "//evil.example")).origin, `https://${DEMO_HOST}`);
});

test("a host the Worker does not own never wakes the container", () => {
  assert.equal(classifyColdRequest({
    hostname: "demo.shinyhub.dev.evil.example",
    method: "GET",
    pathname: "/",
    ...navigation,
  }), "refuse");
});

// The redirect leaves the app origin, so a target relative to the request would
// land back on APP_HOST, where classifyEdgeRequest rejects it as a static 404.
test("the entry URL is absolute and points at the control host", () => {
  assert.equal(ENTRY_URL, `https://${DEMO_HOST}/`);
  assert.equal(new URL(ENTRY_URL).hostname, DEMO_HOST);
  assert.equal(classifyEdgeRequest(new URL(ENTRY_URL).hostname, new URL(ENTRY_URL).pathname), "forward");
});

// The Worker asks the Durable Object for container state once per request, which
// is a serialized round trip on every warm proxy hop. A container seen healthy
// within the memo window cannot have idled to sleep, because sleepAfter is ten
// minutes, so the Worker may skip the call and forward directly.
test("a container seen healthy moments ago may be assumed awake", () => {
  assert.equal(mayAssumeAwake(1_000, 1_000), true);
  assert.equal(mayAssumeAwake(1_000, 1_000 + AWAKE_MEMO_MS - 1), true);
});

test("the awake memo expires rather than holding forever", () => {
  assert.equal(mayAssumeAwake(1_000, 1_000 + AWAKE_MEMO_MS), false);
  assert.equal(mayAssumeAwake(1_000, 1_000 + AWAKE_MEMO_MS + 1), false);
});

test("an isolate that has never seen the container healthy checks its state", () => {
  assert.equal(mayAssumeAwake(null, 1_000), false);
});

// A memo stamped ahead of now cannot be aged, so it is not trusted.
test("a clock moving backwards falls back to checking the state", () => {
  assert.equal(mayAssumeAwake(2_000, 1_000), false);
});

// Ten minutes of idle is what actually puts the container to sleep. The memo is
// aged from when this isolate saw health, not from the container's own last
// activity, so the two clocks must not be allowed to converge: a memo anywhere
// near the sleep window forwards into a container that has already gone down.
// TestDemoWorkerAwakeMemoStaysFarBelowSleepAfter reads the real sleepAfter out
// of index.ts and pins this same ratio across the two files.
const SLEEP_AFTER_MS = 10 * 60 * 1000;

test("the awake memo is far shorter than the container sleep timer", () => {
  assert.ok(AWAKE_MEMO_MS > 0, "a non-positive memo would disable the fast path");
  assert.ok(
    AWAKE_MEMO_MS <= SLEEP_AFTER_MS / 20,
    `the memo must stay a small fraction of the ${SLEEP_AFTER_MS}ms sleep timer, not ${AWAKE_MEMO_MS}ms`,
  );
});
