import { Container, getContainer } from "@cloudflare/containers";
import {
  decorateDemoLogin,
  DEMO_SCRIPT_PATH,
  DEMO_SESSION_PATH,
  DEMO_STYLE_PATH,
  demoLoginScript,
  demoLoginStyles,
} from "./demo-login";
import { DEMO_READY_PATH, demoWakeResponse } from "./demo-wake";
import {
  APP_HOST,
  classifyColdRequest,
  classifyEdgeRequest,
  DEMO_HOST,
  ENTRY_URL,
  isAsleep,
  mayAssumeAwake,
  robotsBody,
} from "./edge-policy.ts";

interface Env {
  SHINYHUB_DEMO: DurableObjectNamespace<ShinyHubDemo>;
}

const allowedHosts = new Set([DEMO_HOST, APP_HOST]);

const DEMO_VIEWER_USERNAME = "demo-viewer";
const DEMO_VIEWER_PASSWORD = "explore-shinyhub-demo";

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

// When this isolate last saw the container healthy. Module scope, so it is kept
// across the requests one isolate serves and lost when it is recycled.
let lastHealthyAt: number | null = null;

export class ShinyHubDemo extends Container {
  defaultPort = 8080;
  sleepAfter = "10m";
}

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    if (!allowedHosts.has(url.hostname)) {
      return new Response("Not found", { status: 404 });
    }

    // Answer what the edge can answer before touching the container: a
    // forwarded request wakes a sleeping one, and wake time is what the
    // container bills for.
    const verdict = classifyEdgeRequest(url.hostname, url.pathname);
    if (verdict === "serve-robots") {
      return demoAsset(robotsBody, "text/plain; charset=utf-8");
    }
    if (verdict === "reject") {
      return new Response("Not found", {
        status: 404,
        headers: { "cache-control": "no-store" },
      });
    }

    if (url.hostname === DEMO_HOST && request.method === "GET") {
      if (url.pathname === DEMO_STYLE_PATH) {
        return demoAsset(demoLoginStyles, "text/css; charset=utf-8");
      }
      if (url.pathname === DEMO_SCRIPT_PATH) {
        return demoAsset(demoLoginScript, "text/javascript; charset=utf-8");
      }
    }

    const headers = new Headers(request.headers);
    headers.set("x-forwarded-proto", "https");

    const container = getContainer(env.SHINYHUB_DEMO, "public-demo");
    // Container state lives in the Durable Object, so reading it is a round trip
    // in front of every request, each warm app proxy hop included. An isolate
    // that saw the container healthy moments ago forwards without asking again.
    const now = Date.now();
    let healthy = mayAssumeAwake(lastHealthyAt, now);
    let asleep = false;
    if (!healthy) {
      const state = await container.getState();
      healthy = state.status === "healthy";
      asleep = isAsleep(state.status);
      if (healthy) {
        lastHealthyAt = now;
      }
    }

    if (url.hostname === DEMO_HOST && url.pathname === DEMO_READY_PATH) {
      if (request.method !== "GET") {
        return new Response("Method not allowed", {
          status: 405,
          headers: { allow: "GET", "cache-control": "no-store" },
        });
      }
      if (asleep) {
        return demoStateResponse("asleep");
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
    // background probes no longer keep it awake around the clock.
    if (!healthy) {
      const coldVerdict = classifyColdRequest({
        hostname: url.hostname,
        method: request.method,
        pathname: url.pathname,
        secFetchDest: request.headers.get("sec-fetch-dest"),
        accept: request.headers.get("accept"),
      });
      if (coldVerdict === "refuse") {
        return new Response(`The ShinyHub demo is asleep. Open ${ENTRY_URL} to start it.\n`, {
          status: 503,
          headers: {
            "cache-control": "no-store",
            "content-type": "text/plain; charset=utf-8",
            "retry-after": "60",
          },
        });
      }
      if (coldVerdict === "redirect-to-entry") {
        return Response.redirect(ENTRY_URL, 303);
      }
      if (coldVerdict === "wake") {
        ctx.waitUntil(container.start().catch((error: unknown) => {
          console.error("Unable to start the ShinyHub demo container", error);
        }));
        // Only a navigation is ever classified as a wake, so there is always a
        // page to render the wait in. Starting the container is left to run past
        // this response rather than held open for the whole boot.
        return demoWakeResponse();
      }
    }

    if (url.hostname === DEMO_HOST && url.pathname === DEMO_SESSION_PATH) {
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
      const loginResponse = await container.fetch(new Request(
        new URL("/api/auth/session", url).toString(),
        {
          method: "POST",
          headers,
          body: JSON.stringify({
            username: DEMO_VIEWER_USERNAME,
            password: DEMO_VIEWER_PASSWORD,
          }),
        },
      ));
      const sessionCookie = loginResponse.headers.get("set-cookie");
      if (!loginResponse.ok || sessionCookie === null) {
        return Response.redirect(new URL("/?demo_error=1", url).toString(), 303);
      }

      return new Response(null, {
        status: 303,
        headers: {
          "cache-control": "no-store",
          location: "/",
          "set-cookie": sessionCookie,
        },
      });
    }

    const upstream = await container.fetch(new Request(request, { headers }));

    // A WebSocket upgrade response cannot be reconstructed: the Workers
    // Response constructor only accepts HTTP status codes 200-599. Returning
    // Cloudflare's original response preserves the attached WebSocket pair.
    if (upstream.status === 101) {
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
      url.hostname === DEMO_HOST
      && request.method === "GET"
      && responseHeaders.get("content-type")?.includes("text/html")
    ) {
      return decorateDemoLogin(response, url.searchParams.has("demo_error"));
    }

    return response;
  },
} satisfies ExportedHandler<Env>;
