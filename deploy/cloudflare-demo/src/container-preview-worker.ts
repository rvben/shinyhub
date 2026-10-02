import { DurableObject } from "cloudflare:workers";
import { authorized, ContainerPreview } from "./container-preview.ts";
import type { PreviewOptions } from "./container-preview.ts";
import demoEntrypoint from "../entrypoint.sh";

interface Env {
  CONTAINER_PREVIEW: DurableObjectNamespace<ShinyHubContainerPreview>;
  // Set through Wrangler secrets; there is deliberately no default token.
  CONTAINER_BENCHMARK_TOKEN: string;
  CONTAINER_POLICY: PreviewOptions["policy"];
  CONTAINER_INSTANCE: PreviewOptions["instance"];
}

export class ShinyHubContainerPreview extends DurableObject<Env> {
  private controller: ContainerPreview;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    if (!ctx.container) throw new Error("Container binding missing");
    if (env.CONTAINER_POLICY !== "default" && env.CONTAINER_POLICY !== "durable_object") throw new Error("Invalid preview policy");
    if (!["standard-1", "standard-2"].includes(env.CONTAINER_INSTANCE)) throw new Error("Invalid preview instance size");
    if (env.CONTAINER_POLICY === "durable_object" && !ctx.container.images.demo) throw new Error("Preview image is missing");
    this.controller = new ContainerPreview(ctx.container, ctx.storage, {
      policy: env.CONTAINER_POLICY,
      instance: env.CONTAINER_INSTANCE,
      image: env.CONTAINER_POLICY === "durable_object" ? ctx.container.images.demo : "default",
      // Keep the same startup script in both policies, including when reusing
      // an already prepared image while testing a bootstrap-only change.
      entrypoint: ["/bin/sh", "-c", demoEntrypoint],
    });
    // Setting the timeout can wait for a pending allocation. Keep stop/state
    // available instead of blocking every request during that operation.
    ctx.waitUntil(this.controller.resume().catch((error) => {
      console.error("Could not restore container inactivity timeout", error);
    }));
  }

  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    switch (`${request.method} ${url.pathname}`) {
      case "GET /state": return Response.json({ running: this.ctx.container!.running, phase: this.controller.phase, activeConnections: this.controller.activeConnections });
      case "GET /ready": return Response.json({ ready: await this.controller.ready() });
      case "POST /start": return Response.json(await this.controller.start());
      case "POST /stop": await this.controller.stop(); return new Response(null, { status: 204 });
      case "POST /snapshot": return Response.json(await this.controller.snapshot());
      case "DELETE /snapshot": await this.controller.forgetSnapshot(); return new Response(null, { status: 204 });
    }
    // Protected proxy for the existing HTTP/WebSocket smoke suite. Canonical
    // hosts are internal to this isolated container; no production Worker is called.
    for (const [prefix, host] of [["/control/", "demo.shinyhub.dev"], ["/apps/", "apps.demo.shinyhub.dev"]]) {
      if (url.pathname.startsWith(prefix)) {
        const upstream = new URL(`http://${host}`);
        upstream.pathname = url.pathname.slice(prefix.length - 1);
        upstream.search = url.search;
        const headers = new Headers(request.headers);
        headers.delete("authorization");
        headers.set("x-forwarded-proto", "https");
        return this.controller.proxy(new Request(upstream, {
          method: request.method, headers, body: request.body, redirect: "manual", signal: request.signal,
        }));
      }
    }
    return new Response("Not found", { status: 404 });
  }

  async alarm(): Promise<void> {
    await this.controller.alarm();
  }
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    // Authenticate before obtaining a DO stub: unauthorized traffic cannot
    // spend container startup time or read snapshot handles.
    if (!authorized(request, env.CONTAINER_BENCHMARK_TOKEN)) {
      return new Response("Unauthorized", { status: 401, headers: { "cache-control": "no-store" } });
    }
    const preview = env.CONTAINER_PREVIEW.get(env.CONTAINER_PREVIEW.idFromName("benchmark"));
    try {
      const response = await preview.fetch(request);
      // Preserve Workers' original WebSocket upgrade response.
      if (response.status === 101) return response;
      const headers = new Headers(response.headers);
      headers.set("cache-control", "no-store");
      return new Response(response.body, { status: response.status, headers });
    } catch (error) {
      console.error("Container preview operation failed", error);
      return Response.json({ error: "Container operation failed; inspect preview logs" }, { status: 503, headers: { "cache-control": "no-store" } });
    }
  },
} satisfies ExportedHandler<Env>;
