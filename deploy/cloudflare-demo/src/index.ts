import { Container, getContainer } from "@cloudflare/containers";
import { bridgeContainerWebSocket } from "./container-websocket.ts";
import { DurableWebSocketClients } from "./durable-websocket-clients.ts";
import { SmokeWebSocketEvidence, validSmokeID } from "./websocket-evidence.ts";
import demoEntrypoint from "../entrypoint.sh";
import { createDemoWorker } from "./demo-worker.ts";
import { DEMO_HOST, APP_HOST } from "./edge-policy.ts";

interface Env {
  SHINYHUB_DEMO: DurableObjectNamespace<ShinyHubDemo>;
  DEMO_DEPLOYMENT_ID?: string;
}

export class ShinyHubDemo extends Container {
  defaultPort = 8080;
  sleepAfter = "10m";
  // Reuse the pinned production release image with the validated startup gate.
  entrypoint = ["/bin/sh", "-c", demoEntrypoint];

  private activeWebSockets = 0;
  private websocketClients = new DurableWebSocketClients(this.ctx);
  private smokeEvidence = new SmokeWebSocketEvidence();

  webSocketMessage(socket: WebSocket, message: string | ArrayBuffer): void {
    this.websocketClients.message(socket, message);
  }
  webSocketClose(socket: WebSocket, code: number, reason: string, wasClean: boolean): void {
    this.websocketClients.close(socket, code, reason, wasClean);
  }
  webSocketError(socket: WebSocket): void { this.websocketClients.error(socket); }

  getSmokeWebSocketResults(id: string) { return this.smokeEvidence.read(id); }

  override async onActivityExpired(): Promise<void> {
    if (this.activeWebSockets > 0) {
      this.renewActivityTimeout();
      return;
    }
    return super.onActivityExpired();
  }

  override async start(...args: Parameters<Container["start"]>): Promise<void> {
    try {
      await super.start(...args);
    } catch (error) {
      // The alert filters the raw retry diagnostic. Record an exhausted start
      // here in case the caller can no longer emit demo_wake_failed.
      if (error instanceof Error
        && error.message === "There is no container instance that can be provided to this Durable Object, try again later") {
        console.error({ event: "demo_container_start_failed", reason: "allocation_unavailable" });
      }
      throw error;
    }
  }

  override async fetch(request: Request): Promise<Response> {
    if (request.headers.get("upgrade")?.toLowerCase() !== "websocket") return super.fetch(request);
    if (!this.ctx.container!.running || (await this.getState()).status !== "healthy") {
      await this.startAndWaitForPorts(this.defaultPort, { abort: request.signal });
    }
    this.renewActivityTimeout();
    const upstream = await this.ctx.container!.getTcpPort(this.defaultPort).fetch(
      request.url.replace(/^https:/, "http:"), request,
    );
    if (upstream.webSocket === null) return upstream;
    const pair = new WebSocketPair();
    const connectionID = crypto.randomUUID();
    const marker = new URL(request.url).searchParams.get("__demo_smoke");
    const smokeID = validSmokeID(marker) ? marker : undefined;
    this.activeWebSockets++;
    // The close callback still has upstream I/O after the client disconnects.
    this.ctx.waitUntil(bridgeContainerWebSocket(pair[1], upstream.webSocket, () => this.renewActivityTimeout(), (result) => {
      this.activeWebSockets--;
      this.smokeEvidence.record(marker, new URL(request.url).pathname, connectionID, result);
      this.renewActivityTimeout();
      const log = {
        event: result.clean ? "demo_websocket_closed" : "demo_websocket_failed",
        connection_id: connectionID, smoke_id: smokeID,
        client_code: result.codes[0], backend_code: result.codes[1],
      };
      if (result.clean) console.info(log);
      else console.error(log);
    }, 5_000, this.websocketClients.control(pair[1])));
    return new Response(null, { status: 101, webSocket: pair[0], headers: upstream.headers });
  }

  override async startAndWaitForPorts(
    ...args: Parameters<Container["startAndWaitForPorts"]>
  ): Promise<void> {
    const [portsOrArgs, cancellationOptions, startOptions] = args;
    // Fleet bootstrap keeps 8080 closed until all apps are ready. Apply this
    // default here so the SDK's automatic containerFetch startup also uses it.
    if (typeof portsOrArgs === "object" && portsOrArgs !== null && !Array.isArray(portsOrArgs)) {
      return super.startAndWaitForPorts({
        ...portsOrArgs,
        cancellationOptions: {
          ...portsOrArgs.cancellationOptions,
          portReadyTimeoutMS: portsOrArgs.cancellationOptions?.portReadyTimeoutMS ?? 90_000,
        },
      });
    }
    return super.startAndWaitForPorts(portsOrArgs, {
      ...cancellationOptions,
      portReadyTimeoutMS: cancellationOptions?.portReadyTimeoutMS ?? 90_000,
    }, startOptions);
  }
}

export default createDemoWorker<Env>({
  controlHost: DEMO_HOST,
  appHost: APP_HOST,
  container: (env) => getContainer(env.SHINYHUB_DEMO, "public-demo"),
  deploymentID: (env) => env.DEMO_DEPLOYMENT_ID,
});
