import assert from "node:assert/strict";
import test from "node:test";
import manifest from "./route-manifest.json" with { type: "json" };
import { canonicalDemoPath, compileAPIRoute, demoControlAdmits, demoNavigationPath } from "./demo-routes.ts";
import { APP_HOST, classifyColdRequest, classifyEdgeRequest, DEMO_HOST, safeDestination } from "./edge-policy.ts";

test("all registered API methods remain reachable except disabled external providers", () => {
  for (const route of manifest.api) {
    const path = route.path.replace(/\{[^}]+\}/g, "example").replace(/\*$/, "nested/file.txt");
    const disabled = /^\/api\/auth\/(github|google|oidc|forward-auth)\//.test(path);
    assert.equal(classifyEdgeRequest(DEMO_HOST, path, route.method), disabled ? "reject" : "forward", `${route.method} ${path}`);
  }
  assert.equal(demoControlAdmits("/api/auth/session", "GET"), false);
  assert.equal(demoControlAdmits("/api/auth/session", "POST"), true);
  assert.equal(demoControlAdmits("/api/not-a-route", "GET"), false);
  assert.equal(demoControlAdmits("/api/apps/example/not-a-route", "GET"), false);
  assert.equal(demoControlAdmits("/api/apps/example/data/nested/file.txt", "GET"), true);
  assert.equal(demoControlAdmits("/api/apps/example/data/nested/file.txt", "PUT"), true);
  assert.throws(() => compileAPIRoute("/api/{id:[0-9]+}"), /Unsupported API route/);
});

test("every shipped asset is admitted under unversioned and immutable URLs", () => {
  for (const name of manifest.assets) {
    for (const prefix of ["/static/", "/static/v/0123456789abcdef/"]) {
      for (const method of ["GET", "HEAD"]) {
        assert.equal(classifyEdgeRequest(DEMO_HOST, prefix + name, method), "forward", method + " " + prefix + name);
      }
      assert.equal(classifyEdgeRequest(DEMO_HOST, prefix + name, "POST"), "reject");
    }
  }
  for (const path of ["/static/", "/static/.env", "/static/unknown.js", "/static/v/not-a-version/app.js"]) {
    assert.equal(classifyEdgeRequest(DEMO_HOST, path), "reject", path);
  }
});

test("dashboard navigation and all curated apps and projects survive the cold entry flow", () => {
  const pages = [...manifest.uiExact, ...manifest.apps.flatMap((slug) => [`/apps/${slug}`, `/apps/${slug}/logs`, `/app/${slug}/`, `/app/${slug}/nested/path`]),
    ...manifest.projects.map((slug) => `/projects/${slug}`)];
  for (const path of pages) {
    assert.equal(classifyEdgeRequest(DEMO_HOST, path, "GET"), "forward", path);
    assert.equal(demoNavigationPath(path), true, path);
    assert.equal(safeDestination(path + "?tab=overview"), path + "?tab=overview", path);
  }
  for (const slug of manifest.apps) {
    assert.equal(classifyEdgeRequest(APP_HOST, `/app/${slug}/websocket/`, "POST"), "forward");
  }
  for (const path of ["/healthz", "/readyz", "/favicon.ico", "/app/.shinyhub/favicon.ico", "/api/app/logout"]) {
    assert.equal(classifyEdgeRequest(APP_HOST, path, "GET"), "forward");
    assert.equal(classifyEdgeRequest(APP_HOST, path, "POST"), "reject");
  }
  for (const path of ["/apps/unknown", "/app/unknown/", "/projects/unknown", "/home/extra", "/apps/dash-demo/logs/extra"]) {
    assert.equal(classifyEdgeRequest(DEMO_HOST, path), "reject", path);
    assert.equal(safeDestination(path), null, path);
  }
});

test("API, assets and probes never redirect a cold browser into a wake", () => {
  for (const pathname of ["/api/server-info", "/api/auth/providers", "/api/apps", "/healthz", "/readyz", "/activez", "/favicon.ico",
    "/static/app.js", "/.shinyhub/apps.json", "/announcements.js"]) {
    for (const hostname of [DEMO_HOST, APP_HOST]) {
      assert.equal(classifyColdRequest({ hostname, pathname, method: "GET", secFetchDest: "document", secFetchSite: "none", accept: "text/html" }), "refuse", hostname + pathname);
    }
    assert.equal(safeDestination(pathname), null, pathname);
  }
});

test("ambiguous paths and scanner namespaces fail closed", () => {
  for (const pathname of ["/.env", "/.git/config", "/wp-login.php", "/wp-admin/", "/phpmyadmin/", "/cgi-bin/test.cgi", "/debug/pprof/", "/metrics",
    "/internal/runtime-bundle/test", "/__demo/unknown", "/%2eenv", "/static//app.js", "/static/%2e%2e/app.js", "/static/%252e%252e/app.js",
    "/api%2fauth/session", "/api/auth/%5csession", "/api/auth/%00session", "/api/auth/%0asession", "/api/auth/%zz/session"]) {
    for (const hostname of [DEMO_HOST, APP_HOST]) assert.equal(classifyEdgeRequest(hostname, pathname), "reject", hostname + pathname);
    assert.equal(safeDestination(pathname), null, pathname);
  }
  assert.equal(canonicalDemoPath("/api/apps/demo/data/a%20b.txt"), "/api/apps/demo/data/a b.txt");
});
