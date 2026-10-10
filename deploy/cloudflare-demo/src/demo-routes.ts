import manifest from "./route-manifest.json" with { type: "json" };

// Generated from the actual Go router, dashboard routes, embedded files and
// curated fleet. The Go drift test requires regeneration when any changes.
const uiExact = new Set(manifest.uiExact);
const uiPatterns = manifest.uiPatterns.map((pattern) => new RegExp(pattern));
const assets = new Set(manifest.assets);
const apps = new Set(manifest.apps);
const projects = new Set(manifest.projects);
const readMethods = new Set(["GET", "HEAD"]);
const disabledProviders = /^\/api\/auth\/(github|google|oidc|forward-auth)(\/|$)/;

function readOnly(method?: string): boolean {
  return method === undefined || readMethods.has(method);
}

// Validate before matching, without rewriting the request passed upstream.
// Ambiguous separators, malformed escapes and traversal must not buy a
// redirect or reach a different server route after another decoding step.
export function canonicalDemoPath(pathname: string): string | null {
  if (!pathname.startsWith("/") || /%2f|%5c/i.test(pathname)) return null;
  let path: string;
  try { path = decodeURIComponent(pathname); } catch { return null; }
  if (/[\x00-\x1f\x7f\\]/.test(path) || path.includes("//") || /%[0-9a-f]{2}/i.test(path)) return null;
  if (path.split("/").some((segment) => segment === "." || segment === "..")) return null;
  return path;
}

export function demoAppPath(pathname: string): boolean {
  const path = canonicalDemoPath(pathname);
  if (path === null) return false;
  const match = /^\/app\/([^/]+)(\/|$)/.exec(path);
  return match !== null && apps.has(match[1]);
}

export function demoNavigationPath(pathname: string): boolean {
  const path = canonicalDemoPath(pathname);
  return path !== null && (dashboardPath(path) || demoAppPath(pathname));
}

function dashboardPath(path: string): boolean {
  if (uiExact.has(path)) return true;
  if (!uiPatterns.some((pattern) => pattern.test(path))) return false;
  const [, family, slug] = path.split("/");
  return family === "apps" ? apps.has(slug) : family === "projects" && projects.has(slug);
}

// Chi uses segment parameters and a terminal wildcard; reject an unfamiliar
// pattern at build/test time instead of silently admitting a broad prefix.
export function compileAPIRoute(template: string): RegExp {
  const segments = template.split("/").map((segment, index, all) => {
    if (segment === "*" && index === all.length - 1) return ".*";
    if (/^\{[a-zA-Z][a-zA-Z0-9_]*\}$/.test(segment)) return "[^/]+";
    if (/[{}*]/.test(segment)) throw new Error(`Unsupported API route: ${template}`);
    return segment.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  });
  return new RegExp("^" + segments.join("/") + "$");
}

const apiRoutes = manifest.api.map((route) => ({ method: route.method, path: compileAPIRoute(route.path) }));
const controlReads = new Set(["/healthz", "/readyz", "/activez", "/favicon.ico", "/announcements.js",
  "/.shinyhub/branding.json", "/.shinyhub/apps.json", "/app/.shinyhub/favicon.ico"]);
const edgePaths = new Set(["/__demo/start", "/__demo/session", "/__demo/ready", "/__demo/status",
  "/__demo/assets/v1/login.css", "/__demo/assets/v1/login.js"]);

export function demoControlAdmits(pathname: string, method?: string): boolean {
  const path = canonicalDemoPath(pathname);
  if (path === null || disabledProviders.test(path)) return false;
  // Method validation on edge endpoints stays in their existing handlers.
  if (edgePaths.has(path)) return true;
  if (path.startsWith("/api/")) {
    return apiRoutes.some((route) => (method === undefined || route.method === method) && route.path.test(path));
  }
  if (path.startsWith("/static/")) {
    const name = path.slice("/static/".length).replace(/^v\/[0-9a-f]{16}\//, "");
    return readOnly(method) && assets.has(name);
  }
  if (controlReads.has(path)) return readOnly(method);
  if (demoAppPath(pathname)) return true; // App framework methods and subroutes vary.
  return readOnly(method) && dashboardPath(path);
}
