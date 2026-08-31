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

interface Env {
  SHINYHUB_DEMO: DurableObjectNamespace<ShinyHubDemo>;
}

const allowedHosts = new Set([
  "demo.shinyhub.dev",
  "apps.demo.shinyhub.dev",
]);

const DEMO_HOST = "demo.shinyhub.dev";
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

    if (url.hostname === DEMO_HOST && url.pathname === DEMO_READY_PATH) {
      if (request.method !== "GET") {
        return new Response("Method not allowed", {
          status: 405,
          headers: { allow: "GET", "cache-control": "no-store" },
        });
      }

      const healthURL = new URL("/healthz", url);
      const healthResponse = await container.fetch(new Request(healthURL, {
        method: "GET",
        headers,
      }));
      await healthResponse.body?.cancel();
      return new Response(null, {
        status: healthResponse.ok ? 204 : 503,
        headers: {
          "cache-control": "no-store",
          "retry-after": "2",
          "x-shinyhub-demo-state": healthResponse.ok ? "ready" : "starting",
        },
      });
    }

    if (
      url.hostname === DEMO_HOST
      && request.method === "GET"
      && url.pathname === "/"
    ) {
      const state = await container.getState();
      if (state.status !== "healthy") {
        ctx.waitUntil(container.start().catch((error: unknown) => {
          console.error("Unable to start the ShinyHub demo container", error);
        }));
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
