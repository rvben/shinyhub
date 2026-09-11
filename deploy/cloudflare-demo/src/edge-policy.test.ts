import { test } from "node:test";
import assert from "node:assert/strict";

import {
  APP_HOST,
  appOriginAdmits,
  AWAKE_MEMO_MS,
  classifyColdRequest,
  classifyEdgeRequest,
  DEMO_HOST,
  ENTRY_URL,
  isAsleep,
  mayAssumeAwake,
  robotsBody,
} from "./edge-policy.ts";

const navigation = { secFetchDest: "document", accept: "text/html,application/xhtml+xml" };
const programmatic = { secFetchDest: null, accept: "*/*" };

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

test("an entry page fetched without a navigation leaves the container asleep", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/",
    ...programmatic,
  }), "refuse");
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/login",
    ...programmatic,
  }), "refuse");
});

test("an accept header alone is enough of a navigation signal", () => {
  assert.equal(classifyColdRequest({
    hostname: DEMO_HOST,
    method: "GET",
    pathname: "/",
    secFetchDest: null,
    accept: "text/html,application/xhtml+xml;q=0.9",
  }), "wake");
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

// The Worker answers a wake verdict with the wake page, which is an HTML page a
// browser renders while it waits. Only a navigation has somewhere to render it,
// so nothing but a GET may be answered that way.
test("only a GET is ever allowed to wake the container", () => {
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
