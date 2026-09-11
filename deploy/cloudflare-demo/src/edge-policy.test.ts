import { test } from "node:test";
import assert from "node:assert/strict";

import {
  APP_HOST,
  appOriginAdmits,
  classifyEdgeRequest,
  DEMO_HOST,
  robotsBody,
} from "./edge-policy.ts";

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
  assert.equal(appOriginAdmits("/apps"), false);
  assert.equal(appOriginAdmits("/application"), false);
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
