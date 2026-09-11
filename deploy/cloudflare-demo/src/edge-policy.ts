// Edge admission policy for the public demo.
//
// Every request that is not answered here reaches the container, and a request
// that reaches a sleeping container boots it. Memory and disk bill for the
// whole time the container is awake, so a path answered at the edge costs
// nothing and the same path forwarded costs a wake cycle.

export const DEMO_HOST = "demo.shinyhub.dev";
export const APP_HOST = "apps.demo.shinyhub.dev";

export const ROBOTS_PATH = "/robots.txt";

// Where the start page's button posts. Posting here is the one way to reach the
// container that does not depend on a request header, so it lives beside the
// admission rules rather than with the page that renders it.
export const DEMO_START_PATH = "/__demo/start";

// Where the one-click demo entry posts to trade a click for a viewer session.
// Here rather than with the login markup so the pages that build these URLs can
// use demoURL without importing the module that classifies them.
export const DEMO_SESSION_PATH = "/__demo/session";

// Carries the page a visitor originally asked for across the wake. A cold deep
// link cannot be served where it was requested, so the path travels as a query
// parameter through the start page, the wake page and the demo login, and the
// visitor lands on it once there is a session to view it with.
export const DEMO_NEXT_PARAM = "demo_next";

// Where a visitor is sent when the page they asked for cannot be served cold.
// Absolute rather than relative to the request, because the app origin is one of
// the places this redirect is issued from and the entry page does not live there.
export const ENTRY_URL = `https://${DEMO_HOST}/`;

// Reports the path raw asks for, or null when it is not a path on the demo's own
// control host. The value arrives in a query string anyone can write and ends up
// in a Location header, so no property of the input settles it: the parser
// strips tab and newline characters and reads a backslash as a slash, turning
// several innocent-looking prefixes into another origin. Two things decide, and
// both are read off the result rather than the input. The resolved origin has to
// be this host. And the string handed back has to begin with exactly one slash,
// because "." and ".." segments are removed after the origin is settled, so a
// path that resolves onto this host can still come back as //evil.example, which
// is a path here and a protocol-relative URL to every browser.
export function safeDestination(raw: string | null): string | null {
  if (raw === null || !raw.startsWith("/")) {
    return null;
  }
  let resolved: URL;
  try {
    resolved = new URL(raw, ENTRY_URL);
  } catch {
    return null;
  }
  if (resolved.origin !== new URL(ENTRY_URL).origin) {
    return null;
  }
  const destination = resolved.pathname + resolved.search;
  if (!destination.startsWith("/") || destination.startsWith("//")) {
    return null;
  }
  return destination;
}

// Reports the page a request is really asking to see. A request already carrying
// a threaded destination is one of the cold path's own hops, so it is the
// parameter that names the page and the request's own path is machinery; the
// wake page's no-script refresh re-requests the start path exactly this way.
export function requestedDestination(pathname: string, search: string): string | null {
  const threaded = new URLSearchParams(search).get(DEMO_NEXT_PARAM);
  if (threaded !== null) {
    return safeDestination(threaded);
  }
  if (pathname.startsWith("/__demo/")) {
    return null;
  }
  return safeDestination(pathname + search);
}

// Builds an absolute URL on the control host, carrying destination when there is
// one worth carrying. Every hop of the cold path is built through here, so a
// destination can only ever move a visitor around the demo.
export function demoURL(pathname: string, destination: string | null): string {
  const url = new URL(pathname, ENTRY_URL);
  const next = safeDestination(destination);
  if (next !== null && next !== "/") {
    url.searchParams.set(DEMO_NEXT_PARAM, next);
  }
  return url.toString();
}

// The demo carries nothing worth indexing, and every crawler hit that reaches
// the container buys a wake cycle.
export const robotsBody = "User-agent: *\nDisallow: /\n";

// Paths the app origin (apps.demo.shinyhub.dev) serves. Mirrors
// internal/apporigin, which the server's app-origin boundary uses to 404
// everything else; forwarding those rejects would wake the container to produce
// a 404 the edge can produce for free. TestDemoWorkerAppOriginMatchesServer
// pins the two lists together.
const APP_ORIGIN_PREFIXES = ["/app/"];
const APP_ORIGIN_EXACT = new Set(["/healthz", "/readyz", "/favicon.ico"]);

// Reports whether the app origin serves pathname.
export function appOriginAdmits(pathname: string): boolean {
  if (APP_ORIGIN_EXACT.has(pathname)) {
    return true;
  }
  return APP_ORIGIN_PREFIXES.some((prefix) => pathname.startsWith(prefix));
}

// Statuses in which the container is not serving. A stopped container charges
// nothing until something touches it, and touching it commits to a whole
// sleepAfter window, so anything that would reach it in these states has to be
// worth that window. "stopping" belongs here because a probe that catches a
// shutdown mid-flight restarts it, buying the window the shutdown was ending.
const DOWN_STATUSES = new Set(["stopped", "stopped_with_code", "stopping"]);

// Reports whether a container reporting status is down. An unrecognised status
// reads as up: the runtime gaining a state is likelier than a container being
// down, and guessing down would refuse visitors a live container could serve.
export function isAsleep(status: string): boolean {
  return DOWN_STATUSES.has(status);
}

// What the edge should do with a request before any container call.
//   serve-robots: answer from the Worker
//   reject:       static 404, the origin would 404 it anyway
//   forward:      the container may serve it
export type EdgeVerdict = "serve-robots" | "reject" | "forward";

// Pages a visitor can arrive on cold. Everything else on the control host is
// reached from one of these, so by the time it is requested the container is
// already awake.
const ENTRY_PATHS = new Set(["/", "/login"]);

// The fields of a request the cold-start gate reads.
export interface ColdRequest {
  hostname: string;
  method: string;
  pathname: string;
  secFetchDest: string | null;
  accept: string | null;
}

// What the edge should do with a request that arrives while the container is
// asleep.
//   wake:              start the container, this is a visitor
//   start:             offer the start page, which costs nothing to render
//   redirect-to-entry: send the browser to the entry page, which may wake it
//   refuse:            answer without contacting the container
export type ColdVerdict = "wake" | "start" | "redirect-to-entry" | "refuse";

// Reports whether request looks like someone asking for a page, as opposed to a
// script, probe or sub-resource fetch. Mirrors isPageLoad in
// internal/proxy/render_admission.go, and is generous the same way: the Accept
// fallback admits any client that asks for HTML, which includes every crawler.
// That is the right trade where being wrong costs a redirect. It is the wrong
// trade where being wrong costs a sleepAfter window, so the decision to start
// the container does not use this.
function isNavigation(request: ColdRequest): boolean {
  if (request.secFetchDest !== null) {
    return request.secFetchDest === "document";
  }
  return request.accept?.includes("text/html") ?? false;
}

// Reports whether a browser generated request by navigating to it. Sec-Fetch-Dest
// is added by the browser and cannot be set by the page issuing the request, so
// unlike Accept it is not something a crawler produces by asking for HTML.
function isBrowserNavigation(request: ColdRequest): boolean {
  return request.method === "GET" && request.secFetchDest === "document";
}

// classifyColdRequest decides who is allowed to spend a cold start. Starting the
// container commits to at least one sleepAfter window of memory and disk
// billing, so the right to do it belongs to a person opening the demo, not to
// whatever else finds the hostname.
export function classifyColdRequest(request: ColdRequest): ColdVerdict {
  // A person following a link to an app on a sleeping demo gets the entry page,
  // which wakes the container and is somewhere to wait. Nothing on the app
  // origin wakes it directly: the app is not running yet, so spending the cold
  // start there buys a page that still cannot be served.
  if (request.hostname === APP_HOST) {
    return request.method === "GET" && isNavigation(request) ? "redirect-to-entry" : "refuse";
  }
  if (request.hostname !== DEMO_HOST) {
    return "refuse";
  }
  // Bots fetch and parse; they do not fill in forms. A post to the start path is
  // therefore the one request that needs no header to be believed, which is what
  // lets the start page hand a visitor the container even when nothing about
  // their request distinguishes them from a crawler.
  if (request.method === "POST" && request.pathname === DEMO_START_PATH) {
    return "wake";
  }
  // The one-click entry is a form post, so it arrives without the headers that
  // distinguish a browser navigating from a script, and it has nowhere to render
  // a wake page regardless. Sending it to the entry page costs the visitor one
  // redirect and denies an unadorned POST the power to spend a sleepAfter
  // window: the navigation that follows is an entry-path GET, which does wake it.
  if (request.method === "POST" && request.pathname === DEMO_SESSION_PATH) {
    return "redirect-to-entry";
  }
  if (request.method !== "GET" && request.method !== "HEAD") {
    return "refuse";
  }
  if (!ENTRY_PATHS.has(request.pathname)) {
    return isNavigation(request) ? "redirect-to-entry" : "refuse";
  }
  // Nothing is refused on an entry page. The start page is rendered here at the
  // edge, so answering with it costs nothing and gives whoever asked something
  // true: a demo that is asleep, and a button that starts it. Only a browser
  // navigating gets the container itself.
  return isBrowserNavigation(request) ? "wake" : "start";
}

// How long the Worker may trust its own observation that the container is
// healthy. Reading container state costs a round trip to the Durable Object on
// every request, including each warm app proxy hop.
export const AWAKE_MEMO_MS = 5_000;

// Reports whether an isolate that saw the container healthy at lastHealthyAt may
// skip the state read and forward. Sound because the container sleeps only after
// ten idle minutes, so one seen healthy a few seconds ago is still awake. Being
// wrong forwards to a container that has crashed, which starts it again; it can
// never refuse a visitor who should have been let through.
export function mayAssumeAwake(lastHealthyAt: number | null, now: number): boolean {
  if (lastHealthyAt === null) {
    return false;
  }
  const age = now - lastHealthyAt;
  return age >= 0 && age < AWAKE_MEMO_MS;
}

export function classifyEdgeRequest(hostname: string, pathname: string): EdgeVerdict {
  if (pathname === ROBOTS_PATH) {
    return "serve-robots";
  }
  if (hostname === APP_HOST && !appOriginAdmits(pathname)) {
    return "reject";
  }
  return "forward";
}
