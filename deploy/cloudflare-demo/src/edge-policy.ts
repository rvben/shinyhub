// Edge admission policy for the public demo.
//
// Every request that is not answered here reaches the container, and a request
// that reaches a sleeping container boots it. Memory and disk bill for the
// whole time the container is awake, so a path answered at the edge costs
// nothing and the same path forwarded costs a wake cycle.

import { DEMO_SESSION_PATH } from "./demo-login.ts";

export const DEMO_HOST = "demo.shinyhub.dev";
export const APP_HOST = "apps.demo.shinyhub.dev";

export const ROBOTS_PATH = "/robots.txt";

// Where a visitor is sent when the page they asked for cannot be served cold.
// Absolute rather than relative to the request, because the app origin is one of
// the places this redirect is issued from and the entry page does not live there.
export const ENTRY_URL = `https://${DEMO_HOST}/`;

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
//   redirect-to-entry: send the browser to the entry page, which may wake it
//   refuse:            answer without contacting the container
export type ColdVerdict = "wake" | "redirect-to-entry" | "refuse";

// Reports whether request is a browser navigating to a page, as opposed to a
// script, probe or sub-resource fetch. Mirrors isPageLoad in
// internal/proxy/render_admission.go.
function isNavigation(request: ColdRequest): boolean {
  if (request.secFetchDest !== null) {
    return request.secFetchDest === "document";
  }
  return request.accept?.includes("text/html") ?? false;
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
  // The one-click entry is a form post, so it arrives without the headers that
  // distinguish a browser navigating from a script, and it has nowhere to render
  // a wake page regardless. Sending it to the entry page costs the visitor one
  // redirect and denies an unadorned POST the power to spend a sleepAfter
  // window: the navigation that follows is an entry-path GET, which does wake it.
  if (request.method === "POST" && request.pathname === DEMO_SESSION_PATH) {
    return "redirect-to-entry";
  }
  if (request.method !== "GET" || !isNavigation(request)) {
    return "refuse";
  }
  return ENTRY_PATHS.has(request.pathname) ? "wake" : "redirect-to-entry";
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
