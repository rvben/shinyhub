// Edge admission policy for the public demo.
//
// Every request that is not answered here reaches the container, and a request
// that reaches a sleeping container boots it. Memory and disk bill for the
// whole time the container is awake, so a path answered at the edge costs
// nothing and the same path forwarded costs a wake cycle.

export const DEMO_HOST = "demo.shinyhub.dev";
export const APP_HOST = "apps.demo.shinyhub.dev";

export const ROBOTS_PATH = "/robots.txt";

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

// What the edge should do with a request before any container call.
//   serve-robots: answer from the Worker
//   reject:       static 404, the origin would 404 it anyway
//   forward:      the container may serve it
export type EdgeVerdict = "serve-robots" | "reject" | "forward";

export function classifyEdgeRequest(hostname: string, pathname: string): EdgeVerdict {
  if (pathname === ROBOTS_PATH) {
    return "serve-robots";
  }
  if (hostname === APP_HOST && !appOriginAdmits(pathname)) {
    return "reject";
  }
  return "forward";
}
