import { DurableObject } from "cloudflare:workers";
import { ContainerPreview } from "./container-preview.ts";
import { createDemoWorker } from "./demo-worker.ts";
import { handleCanaryOperations } from "./canary-operations.ts";
import demoEntrypoint from "../entrypoint.sh";

export const CANARY_CONTROL_HOST = "staging.demo.shinyhub.dev";
export const CANARY_APP_HOST = "apps.staging.demo.shinyhub.dev";

interface Env {
  SHINYHUB_CANARY: DurableObjectNamespace<ShinyHubCanary>;
  CONTAINER_CANARY_TOKEN: string;
}

export class ShinyHubCanary extends DurableObject<Env> {
  private controller: ContainerPreview;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    if (!ctx.container?.images.demo) throw new Error("Canary image binding is missing");
    this.controller = new ContainerPreview(ctx.container, ctx.storage, {
      policy: "durable_object", image: ctx.container.images.demo, instance: "standard-1",
      entrypoint: ["/bin/sh", "-c", demoEntrypoint],
      controlOrigin: "https://" + CANARY_CONTROL_HOST,
      appOrigin: "https://" + CANARY_APP_HOST,
    });
    ctx.waitUntil(this.controller.resume().catch((error) => {
      console.error("Could not restore canary inactivity timeout", error);
    }));
  }

  async getState(): Promise<{ status: string }> {
    return { status: await this.controller.ready() ? "healthy" : this.ctx.container!.running ? "starting" : "stopped" };
  }
  async state(): Promise<unknown> {
    return { running: this.ctx.container!.running, ready: await this.controller.ready(),
      phase: this.controller.phase, activeConnections: this.controller.activeConnections };
  }
  async start() { return this.controller.start(); }
  async stop(): Promise<void> { await this.controller.stop(); }
  async snapshot() { return this.controller.snapshot(); }
  async forgetSnapshot(): Promise<void> { await this.controller.forgetSnapshot(); }
  async terminate(): Promise<void> {
    // Fixed, operator-only fault injection for verifying unexpected exit recovery.
    this.ctx.container!.signal(9);
  }
  async fetch(request: Request): Promise<Response> { return this.controller.proxy(request); }
  async alarm(): Promise<void> { await this.controller.alarm(); }
}

const browserWorker = createDemoWorker<Env>({
  controlHost: CANARY_CONTROL_HOST, appHost: CANARY_APP_HOST,
  passiveReadiness: true,
  container: (env) => env.SHINYHUB_CANARY.get(env.SHINYHUB_CANARY.idFromName("staging-canary")),
});

export default {
  async fetch(request: Request<unknown, IncomingRequestCfProperties>, env: Env, ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);
    try {
      if (url.hostname === CANARY_CONTROL_HOST && url.pathname.startsWith("/__canary/")) {
        return await handleCanaryOperations(request, env.CONTAINER_CANARY_TOKEN,
          () => env.SHINYHUB_CANARY.get(env.SHINYHUB_CANARY.idFromName("staging-canary")));
      }
      return await browserWorker.fetch!(request, env, ctx);
    } catch (error) {
      console.error("Canary request failed", error);
      return new Response("The staging demo is temporarily unavailable. Please try again.", {
        status: 503, headers: { "cache-control": "no-store", "retry-after": "2" },
      });
    }
  },
} satisfies ExportedHandler<Env>;
