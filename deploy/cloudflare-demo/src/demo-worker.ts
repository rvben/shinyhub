import { validSmokeID, type WebSocketCloseEvidence } from "./websocket-evidence.ts";
import { containerRequest, readContainerState, unavailableResponse, isTransientContainerError } from "./container-request.ts";
import {
  decorateDemoLogin,
  DEMO_SCRIPT_PATH,
  DEMO_STYLE_PATH,
  demoLoginScript,
  demoLoginStyles,
} from "./demo-login.ts";
import { DEMO_READY_PATH, demoStartResponse, demoWakeResponse } from "./demo-wake.ts";
import { DEMO_STATUS_PATH, passiveDemoStatus } from "./demo-status.ts";
import {
  APP_HOST,
  classifyColdRequest,
  classifyEdgeRequest,
  DEMO_HOST,
  DEMO_NEXT_PARAM,
  DEMO_SESSION_PATH,
  DEMO_START_PATH,
  demoURL,
  gateUnconfirmed,
  isAsleep,
  mayAssumeAwake,
  requestedDestination,
  robotsBody,
  safeDestination,
} from "./edge-policy.ts";

export interface DemoContainer {
  getState(): Promise<{ status: string }>;
  getSmokeWebSocketResults?(id: string): Promise<WebSocketCloseEvidence[]>;
  start(): Promise<unknown>;
  fetch(request: Request): Promise<Response>;
}

export interface DemoWorkerOptions<Env> {
  controlHost: string;
  appHost: string;
  container(env: Env): DemoContainer;
  // Native containers already verify HTTP health in getState().
  passiveReadiness?: boolean;
}

const DEMO_VIEWER_USERNAME = "demo-viewer";
const DEMO_VIEWER_PASSWORD = "explore-shinyhub-demo";

// Automatic invocation logs label even intentional sleep/readiness 503s as
// errors. The alert excludes those 503 summaries; report real upstream 5xxs
// separately so an unavailable application still alerts. Keep request data out.
function reportUpstreamFailure(response: Response, operation: "proxy" | "viewer_session" | "dashboard_asset"): void {
  if (response.status >= 500) {
    console.error({ event: "demo_upstream_failed", operation, status: response.status });
  }
}

// The Go server's versioned asset ETag contains the first 16 hex digits of
// the identity body's SHA-256. Verify before sending the immutable response:
// an interrupted stream can otherwise reach the browser as a cacheable 200.
async function verifyDashboardAsset(response: Response, signal: AbortSignal): Promise<Response> {
  if (response.status !== 200) return response;
  const expected = /^"([0-9a-f]{16})"$/.exec(response.headers.get("etag") ?? "")?.[1];
  if (!expected || response.headers.has("content-encoding")) {
    void response.body?.cancel().catch(() => {});
    return unavailableResponse();
  }
  try {
    const bytes = await response.arrayBuffer();
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes), 0, 8);
    const actual = Array.from(digest, byte => byte.toString(16).padStart(2, "0")).join("");
    return actual === expected ? new Response(bytes, response) : unavailableResponse();
  } catch {
    signal.throwIfAborted();
    return unavailableResponse();
  }
}

function demoAsset(body: string, contentType: string): Response {
  return new Response(body, {
    headers: {
      "cache-control": "no-store",
      "content-type": contentType,
      "permissions-policy": "camera=(), microphone=(), geolocation=()",
      "referrer-policy": "strict-origin-when-cross-origin",
      "strict-transport-security": "max-age=31536000; includeSubDomains",
      "x-content-type-options": "nosniff",
    },
  });
}

// The wake page polls for this, so it reports whether the demo can serve a
// request rather than whether the container process exists. A container that is
// down is reported as down rather than as one more not-yet: this probe starts
// nothing, so the page has to know the difference between a wait that ends on
// its own and one that never will.
type DemoState = "ready" | "starting" | "asleep";

function demoStateResponse(state: DemoState): Response {
  const ready = state === "ready";
  const headers = new Headers({
    "cache-control": "no-store",
    "x-shinyhub-demo-state": state,
  });
  if (!ready) {
    headers.set("retry-after", "2");
  }
  return new Response(null, { status: ready ? 204 : 503, headers });
}

// Each configured Worker keeps its own health memo across requests in an
// isolate; production and staging do not share hosts or a container handle.
export function createDemoWorker<Env>(options: DemoWorkerOptions<Env>): ExportedHandler<Env> {
  const { controlHost, appHost } = options;
  if (!/^[a-z0-9.-]+$/.test(controlHost) || !/^[a-z0-9.-]+$/.test(appHost) || controlHost === appHost) {
    throw new Error("Demo hosts must be distinct DNS names");
  }
  const entryURL = `https://${controlHost}/`;
  const allowedHosts = new Set([controlHost, appHost]);
  let lastHealthyAt: number | null = null;
  return {
    async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
      const url = new URL(request.url);
      if (!allowedHosts.has(url.hostname)) {
        return new Response("Not found", { status: 404 });
      }

      // Answer what the edge can answer before touching the container: a
      // forwarded request wakes a sleeping one, and wake time is what the
      // container bills for.
      const verdict = classifyEdgeRequest(url.hostname === appHost ? APP_HOST : DEMO_HOST, url.pathname, request.method);
      if (verdict === "serve-robots") {
        return demoAsset(robotsBody, "text/plain; charset=utf-8");
      }
      if (verdict === "reject") {
        return new Response("Not found", {
          status: 404,
          headers: { "cache-control": "no-store" },
        });
      }

      if (url.hostname === controlHost && request.method === "GET") {
        if (url.pathname === DEMO_STYLE_PATH) {
          return demoAsset(demoLoginStyles, "text/css; charset=utf-8");
        }
        if (url.pathname === DEMO_SCRIPT_PATH) {
          return demoAsset(demoLoginScript, "text/javascript; charset=utf-8");
        }
      }

      const headers = new Headers(request.headers);
      headers.set("x-forwarded-proto", "https");

      const container = options.container(env);
      // Separate from the visitor wake probe: polling this never reaches the
      // container and never refreshes its idle deadline or the warm-state memo.
      if (url.hostname === controlHost && url.pathname === DEMO_STATUS_PATH) {
        const marker = request.headers.get("x-shinyhub-demo-smoke");
        if (request.method === "GET" && validSmokeID(marker) && container.getSmokeWebSocketResults) {
          // Publish the DO's actual close evidence through the correlated HTTP
          // probe. Real-time tails do not reliably include DO handler logs.
          ctx.waitUntil(readContainerState(() => container.getSmokeWebSocketResults!(marker), request.signal)
            .then(records => { for (const record of records) {
              if (record.event === "demo_websocket_closed") console.info(record);
              else console.error(record);
            } })
            .catch(() => console.error({ event: "demo_websocket_verification_failed", smoke_id: marker })));
        }
        return passiveDemoStatus(request.method, container);
      }
      // Container state lives in the Durable Object, so reading it is a round trip
      // in front of every request, each warm app proxy hop included. An isolate
      // that saw the container healthy moments ago forwards without asking again.
      const now = Date.now();
      let healthy = mayAssumeAwake(lastHealthyAt, now);
      let asleep = false;
      const passiveProbe = options.passiveReadiness && url.pathname === DEMO_READY_PATH;
      if (!healthy || passiveProbe) {
        let state: { status: string };
        let stateAttempt = 0;
        try {
          state = await readContainerState(() => (stateAttempt++ === 0 ? container : options.container(env)).getState(), request.signal);
        } catch (error) {
          lastHealthyAt = null;
          if (request.signal.aborted || (!isTransientContainerError(error)
            && !(error instanceof Error && error.name === "TimeoutError"))) throw error;
          console.error({ event: "demo_state_failed", operation: "state" });
          return unavailableResponse();
        }
        healthy = state.status === "healthy";
        asleep = isAsleep(state.status);
        if (healthy) {
          lastHealthyAt = now;
        } else {
          lastHealthyAt = null;
        }
      }

      if (url.hostname === controlHost && url.pathname === DEMO_READY_PATH) {
        if (request.method !== "GET") {
          return new Response("Method not allowed", {
            status: 405,
            headers: { allow: "GET", "cache-control": "no-store" },
          });
        }
        if (asleep) {
          return demoStateResponse("asleep");
        }
        if (passiveProbe) {
          return demoStateResponse(healthy ? "ready" : "starting");
        }

        const healthURL = new URL("/healthz", url);
        const healthResponse = await container.fetch(new Request(healthURL, {
          method: "GET",
          headers,
        }));
        await healthResponse.body?.cancel();
        return demoStateResponse(healthResponse.ok ? "ready" : "starting");
      }

      // Only a visitor opening the demo may spend a cold start. Everything else
      // that arrives while the container is down is answered here, so crawlers and
      // background probes no longer keep it awake around the clock. A container
      // that is up but not yet confirmed healthy has no cold start left to spend,
      // so gateUnconfirmed lets all but a wake fall through to the proxy below.
      if (!healthy) {
        const coldVerdict = gateUnconfirmed(classifyColdRequest({
          hostname: url.hostname === appHost ? APP_HOST : DEMO_HOST,
          method: request.method,
          pathname: url.pathname,
          secFetchDest: request.headers.get("sec-fetch-dest"),
          secFetchSite: request.headers.get("sec-fetch-site"),
          accept: request.headers.get("accept"),
        }), asleep);
        // The page the visitor came for, which the cold path carries across the
        // wake so a deep link does not decay into the dashboard. It is only ever a
        // path on the control host, so it cannot redirect them off the demo.
        const destination = requestedDestination(url.pathname, url.search);

        if (coldVerdict === "refuse") {
          return new Response(`The ShinyHub demo is asleep. Open ${entryURL} to start it.\n`, {
            status: 503,
            headers: {
              "cache-control": "no-store",
              "content-type": "text/plain; charset=utf-8",
              "retry-after": "60",
            },
          });
        }
        if (coldVerdict === "redirect-to-entry") {
          return Response.redirect(demoURL("/", destination, entryURL), 303);
        }
        if (coldVerdict === "start") {
          return demoStartResponse(destination, request.method, entryURL);
        }
        if (coldVerdict === "wake") {
          const wakeID = crypto.randomUUID();
          const site = request.headers.get("sec-fetch-site");
          console.info({
            event: "demo_wake_requested",
            wake_id: wakeID,
            cold_start: asleep,
            trigger: request.method === "POST" ? "start_form" : "navigation",
            entry: url.pathname === DEMO_START_PATH ? "start" : url.pathname === "/login" ? "login" : "root",
            fetch_site: site === null ? "missing"
              : ["none", "same-origin", "same-site", "cross-site"].includes(site) ? site : "other",
            browser_navigation: request.headers.get("sec-fetch-dest") === "document",
          });
          ctx.waitUntil(container.start().then(() => {
            console.info({ event: "demo_wake_completed", wake_id: wakeID, elapsed_ms: Date.now() - now });
          }).catch((error: unknown) => {
            console.error({
              event: "demo_wake_failed",
              wake_id: wakeID,
              elapsed_ms: Date.now() - now,
              reason: error instanceof Error && /timeout|timed out/i.test(error.message)
                ? "timeout" : "start_failed",
            });
          }));
          // A wake is only ever a document navigation or the start page's form
          // submission, so there is always a page to render the wait in. Starting
          // the container is left to run past this response rather than held open
          // for the whole boot.
          return demoWakeResponse(destination, entryURL);
        }
      }

      // Reached only once the container is up, because the cold gate answers the
      // start button with the wake page. There is nothing left to wait for, so the
      // visitor goes straight to the page they came for.
      if (url.hostname === controlHost && url.pathname === DEMO_START_PATH) {
        return Response.redirect(demoURL("/", requestedDestination(url.pathname, url.search), entryURL), 303);
      }

      if (url.hostname === controlHost && url.pathname === DEMO_SESSION_PATH) {
        if (request.method !== "POST") {
          return new Response("Method not allowed", {
            status: 405,
            headers: { allow: "POST", "cache-control": "no-store" },
          });
        }
        if (request.headers.get("sec-fetch-site") === "cross-site") {
          return new Response("Forbidden", {
            status: 403,
            headers: { "cache-control": "no-store" },
          });
        }

        headers.set("content-type", "application/json");
        headers.delete("content-length");
        const loginResponse = await containerRequest(new Request(
          new URL("/api/auth/session", url).toString(),
          {
            method: "POST",
            headers,
            signal: request.signal,
            body: JSON.stringify({
              username: DEMO_VIEWER_USERNAME,
              password: DEMO_VIEWER_PASSWORD,
            }),
          },
        ), (attempt) => container.fetch(attempt), () => {});
        reportUpstreamFailure(loginResponse, "viewer_session");
        // The end of the cold path. A visitor who followed a link to an app now
        // has the session that link needed, so this is where the page they asked
        // for finally gets served rather than the dashboard.
        const next = safeDestination(url.searchParams.get(DEMO_NEXT_PARAM));

        const sessionCookie = loginResponse.headers.get("set-cookie");
        if (!loginResponse.ok || sessionCookie === null) {
          const retry = new URL(demoURL("/", next, entryURL));
          retry.searchParams.set("demo_error", "1");
          return Response.redirect(retry.toString(), 303);
        }

        return new Response(null, {
          status: 303,
          headers: {
            "cache-control": "no-store",
            location: next ?? "/",
            "set-cookie": sessionCookie,
          },
        });
      }

      let upstream: Response;
      let proxyAttempt = 0;
      const verifyAsset = url.hostname === controlHost && request.method === "GET"
        && /^\/static\/v\/[0-9a-f]{16}\//.test(url.pathname) && !headers.has("range");
      const operation = verifyAsset ? "dashboard_asset" : "proxy";
      if (verifyAsset) headers.set("accept-encoding", "identity");
      try {
        upstream = await containerRequest(
          new Request(request, { headers, signal: request.signal }),
          async (attempt) => {
            const response = await (proxyAttempt++ === 0 ? container : options.container(env)).fetch(attempt);
            return verifyAsset ? verifyDashboardAsset(response, attempt.signal) : response;
          },
          () => console.info({ event: "demo_request_recovered", operation }),
        );
      } catch (error) {
        lastHealthyAt = null;
        throw error;
      }
      reportUpstreamFailure(upstream, operation);
      if (upstream.status >= 500) lastHealthyAt = null;

      if (upstream.status === 101 && upstream.webSocket) {
        return upstream;
      }

      const responseHeaders = new Headers(upstream.headers);
      responseHeaders.set("strict-transport-security", "max-age=31536000; includeSubDomains");
      responseHeaders.set("x-content-type-options", "nosniff");
      responseHeaders.set("referrer-policy", "strict-origin-when-cross-origin");
      responseHeaders.set("permissions-policy", "camera=(), microphone=(), geolocation=()");
      responseHeaders.delete("server");

      const response = new Response(upstream.body, {
        status: upstream.status,
        statusText: upstream.statusText,
        headers: responseHeaders,
      });

      if (
        url.hostname === controlHost
        && request.method === "GET"
        && responseHeaders.get("content-type")?.includes("text/html")
      ) {
        return decorateDemoLogin(
          response,
          url.searchParams.has("demo_error"),
          safeDestination(url.searchParams.get(DEMO_NEXT_PARAM)),
          entryURL,
        );
      }

      return response;
    },
  } satisfies ExportedHandler<Env>;
}
