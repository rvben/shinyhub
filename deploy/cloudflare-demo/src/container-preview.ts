// An isolated benchmark controller, never imported by the production Worker.
// Snapshots here are experimental filesystem checkpoints, not durable backups.
import { trackResponse } from "./container-connections.ts";

export const IDLE_TIMEOUT_MS = 10 * 60 * 1000;
const SNAPSHOT_MAX_AGE_MS = 29 * 24 * 60 * 60 * 1000;
const SNAPSHOT_KEY = "filesystem-snapshot";
const RUNNING_IMAGE_KEY = "running-image";
const AUTH_SECRET_KEY = "demo-auth-secret";
const DEPLOY_TOKEN_KEY = "demo-deploy-token";
const IDLE_DEADLINE_KEY = "idle-deadline";
const ACTIVE_CONNECTIONS_KEY = "active-connections";

type Runtime = Pick<Container, "running" | "start" | "destroy" | "getTcpPort" | "setInactivityTimeout" | "snapshotContainer" | "monitor">;
interface Storage {
  get<T>(key: string): Promise<T | undefined>;
  put<T>(key: string, value: T): Promise<void>;
  delete(key: string): Promise<boolean>;
  setAlarm(timestamp: number): Promise<void>;
  deleteAlarm(): Promise<void>;
}
export interface SavedSnapshot {
  handle: ContainerSnapshot;
  image: string;
  lastUsedAt: number;
}
export interface PreviewOptions {
  policy: "default" | "durable_object";
  image: string;
  instance: "standard-1" | "standard-2";
  entrypoint?: string[];
  // Browser canaries use their own origins with the same prepared image.
  controlOrigin?: string;
  appOrigin?: string;
}
export interface BootResult {
  mode: "warm" | "image" | "snapshot" | "image-after-snapshot-failure";
  readyMs: number;
}

export function usableSnapshot(saved: SavedSnapshot | undefined, image: string, now: number): boolean {
  return saved !== undefined && saved.image === image && now >= saved.lastUsedAt
    && now - saved.lastUsedAt < SNAPSHOT_MAX_AGE_MS;
}

export function authorized(request: Request, token: string | undefined): boolean {
  return Boolean(token && request.headers.get("authorization") === `Bearer ${token}`);
}

export class ContainerPreview {
  private mutation: Promise<unknown> = Promise.resolve();
  private booting: Promise<BootResult> | null = null;
  private runtime: Runtime;
  private storage: Storage;
  private options: PreviewOptions;
  private now: () => number;
  private delay: (ms: number) => Promise<void>;
  private connections = new Set<symbol>();
  phase = "idle";

  get activeConnections(): number { return this.connections.size; }

  constructor(
    runtime: Runtime,
    storage: Storage,
    options: PreviewOptions,
    now: () => number = Date.now,
    delay: (ms: number) => Promise<void> = (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  ) {
    this.runtime = runtime;
    this.storage = storage;
    this.options = options;
    this.now = now;
    this.delay = delay;
  }

  async resume(): Promise<void> {
    await this.exclusive(async () => {
      if (!this.runtime.running) {
        await this.clearIdle();
        return;
      }
      const deadline = await this.storage.get<number>(IDLE_DEADLINE_KEY);
      // A DO reset drops its non-hibernating connections. Give that disconnect
      // a full idle window, without resurrecting a stale in-memory counter.
      const disconnected = await this.storage.get<boolean>(ACTIVE_CONNECTIONS_KEY);
      await this.storage.delete(ACTIVE_CONNECTIONS_KEY);
      if (deadline === undefined || disconnected) await this.touch();
      else await this.storage.setAlarm(deadline);
    });
    // This native operation may wait for allocation. Never put it ahead of
    // stop() in the mutation queue: destroy() must remain available to recover.
    if (this.runtime.running) await this.runtime.setInactivityTimeout(IDLE_TIMEOUT_MS);
  }

  // monitor() can keep the DO resident after readiness and delay the native
  // inactivity timer. A persistent alarm bounds idle time independently of it.
  async alarm(): Promise<void> {
    await this.exclusive(async () => {
      const deadline = await this.storage.get<number>(IDLE_DEADLINE_KEY);
      if (deadline === undefined) return;
      if (this.connections.size > 0) {
        await this.touch();
        return;
      }
      if (deadline > this.now()) {
        await this.storage.setAlarm(deadline);
        return;
      }
      if (this.runtime.running) await this.runtime.destroy();
      this.connections.clear();
      await this.clearIdle();
    });
  }

  // Readiness probes never allocate or restart a container.
  async ready(): Promise<boolean> {
    if (!this.runtime.running) return false;
    try {
      const healthURL = new URL("/healthz", this.options.controlOrigin ?? "http://demo.shinyhub.dev");
      healthURL.protocol = "http:";
      const response = await this.runtime.getTcpPort(8080).fetch(new Request(healthURL, {
        headers: { "x-forwarded-proto": "https" }, signal: AbortSignal.timeout(3000),
      }));
      await response.body?.cancel();
      return response.ok;
    } catch {
      return false;
    }
  }

  async start(): Promise<BootResult> {
    if (this.booting) return this.booting;
    this.booting = this.exclusive(() => this.boot());
    try { return await this.booting; }
    finally { this.booting = null; }
  }

  async stop(): Promise<void> {
    await this.exclusive(async () => {
      this.phase = "stopping";
      if (this.runtime.running) await this.runtime.destroy();
      this.connections.clear();
      await this.clearIdle();
      this.phase = "idle";
    });
  }

  async forgetSnapshot(): Promise<void> {
    await this.exclusive(async () => { await this.storage.delete(SNAPSHOT_KEY); });
  }

  async snapshot(): Promise<ContainerSnapshot> {
    return this.exclusive(async () => {
      if (this.options.policy !== "durable_object") throw new Error("Snapshots require durable_object scheduling");
      if (!await this.ready()) throw new Error("The container must be ready before snapshotting");
      if (await this.storage.get<string>(RUNNING_IMAGE_KEY) !== this.options.image) {
        throw new Error("Stop and restart the container on the current image before snapshotting");
      }
      const handle = await this.runtime.snapshotContainer({ name: "shinyhub-demo-benchmark" });
      await this.storage.put<SavedSnapshot>(SNAPSHOT_KEY, { handle, image: this.options.image, lastUsedAt: this.now() });
      await this.touch();
      return handle;
    });
  }

  async proxy(request: Request): Promise<Response> {
    // Do not use a helper that auto-starts on arbitrary proxy requests.
    const connection = await this.exclusive(async () => {
      if (request.signal.aborted || !await this.ready() || request.signal.aborted) return undefined;
      await this.touch();
      if (this.connections.size === 0) await this.storage.put(ACTIVE_CONNECTIONS_KEY, true);
      const id = Symbol();
      this.connections.add(id);
      return id;
    });
    if (connection === undefined) return new Response("Container is asleep or starting", { status: 503 });
    const finish = () => {
      request.signal.removeEventListener("abort", finish);
      void this.exclusive(async () => {
        // Explicit stop invalidates old leases; their later disconnects must
        // never extend the deadline of a newly started container.
        if (!this.connections.delete(connection) || this.connections.size > 0) return;
        await this.storage.delete(ACTIVE_CONNECTIONS_KEY);
        if (this.runtime.running) await this.touch();
        else await this.clearIdle();
      }).catch((error) => console.error("Could not record container disconnect", error));
    };
    // Stream cancellation alone is unreliable across HTTP/DO boundaries.
    // enable_request_signal also releases activity when the client disconnects.
    request.signal.addEventListener("abort", finish, { once: true });
    if (request.signal.aborted) {
      finish();
      return new Response("Client disconnected", { status: 499 });
    }
    try {
      // Native TCP ports serve plain HTTP. Keep the public host and forwarded
      // scheme while converting only this secure internal transport hop.
      const upstreamURL = new URL(request.url);
      upstreamURL.protocol = "http:";
      const upstream = new Request(upstreamURL, request);
      const response = await this.runtime.getTcpPort(8080).fetch(upstream);
      // After an upgrade the accepted socket's close/error events own cleanup.
      if (response.webSocket) request.signal.removeEventListener("abort", finish);
      return trackResponse(response, finish);
    } catch (error) {
      finish();
      throw error;
    }
  }

  private exclusive<T>(operation: () => Promise<T>): Promise<T> {
    const result = this.mutation.then(operation);
    this.mutation = result.catch(() => {});
    return result;
  }

  private async boot(): Promise<BootResult> {
    const startedAt = this.now();
    this.phase = "reading-startup-state";
    if (this.runtime.running) {
      if (this.options.policy === "durable_object" && await this.storage.get<string>(RUNNING_IMAGE_KEY) !== this.options.image) {
        throw new Error("The running container uses an older image; stop it before starting the current image");
      }
      await this.waitReady();
      await this.touch();
      return { mode: "warm", readyMs: this.now() - startedAt };
    }
    const saved = this.options.policy === "durable_object"
      ? await this.storage.get<SavedSnapshot>(SNAPSHOT_KEY) : undefined;
    const restore = usableSnapshot(saved, this.options.image, this.now()) ? saved : undefined;
    if (saved && !restore) await this.storage.delete(SNAPSHOT_KEY);
    let mode: BootResult["mode"] = restore ? "snapshot" : "image";
    try {
      await this.allocate(restore);
    } catch (error) {
      this.phase = "clearing-failed-start";
      if (!restore) {
        await this.runtime.destroy();
        throw error;
      }
      // Expiration, invalid snapshot, or failed restored entrypoint: discard
      // this checkpoint and make one bounded attempt from the current image.
      console.warn("Filesystem snapshot restore failed; retrying the current image", error);
      await this.storage.delete(SNAPSHOT_KEY);
      await this.runtime.destroy();
      try {
        await this.allocate();
      } catch (fallbackError) {
        this.phase = "clearing-failed-start";
        await this.runtime.destroy();
        throw fallbackError;
      }
      mode = "image-after-snapshot-failure";
    }
    if (restore && mode === "snapshot") {
      await this.storage.put<SavedSnapshot>(SNAPSHOT_KEY, { ...restore, lastUsedAt: this.now() });
    }
    await this.touch();
    this.phase = "ready";
    return { mode, readyMs: this.now() - startedAt };
  }

  private async touch(): Promise<void> {
    this.phase = "saving-idle-deadline";
    const deadline = this.now() + IDLE_TIMEOUT_MS;
    await this.storage.put(IDLE_DEADLINE_KEY, deadline);
    this.phase = "scheduling-idle-alarm";
    await this.storage.setAlarm(deadline);
    this.phase = "ready";
  }

  private async clearIdle(): Promise<void> {
    this.phase = "clearing-idle-deadline";
    await this.storage.delete(IDLE_DEADLINE_KEY);
    await this.storage.delete(ACTIVE_CONNECTIONS_KEY);
    this.phase = "clearing-idle-alarm";
    await this.storage.deleteAlarm();
    this.phase = "idle";
  }

  private async allocate(saved?: SavedSnapshot): Promise<void> {
    // The demo image generates a new key unless one is supplied. Snapshot
    // databases contain encrypted data, so every restore must reuse that key.
    this.phase = "reading-credentials";
    const startupEnv = this.options.policy === "durable_object" ? {
      ...(this.options.controlOrigin ? { SHINYHUB_BASE_URL: this.options.controlOrigin } : {}),
      ...(this.options.appOrigin ? { SHINYHUB_APP_ORIGIN: this.options.appOrigin } : {}),
      SHINYHUB_AUTH_SECRET: await this.secret(AUTH_SECRET_KEY),
      SHINYHUB_DEPLOY_TOKEN: `shk_${await this.secret(DEPLOY_TOKEN_KEY)}`,
    } : undefined;
    const capacityDeadline = this.now() + 30_000;
    for (let attempt = 0; ; attempt++) {
      let started = false;
      try {
        this.phase = "requesting-instance";
        if (this.options.policy === "default") {
          this.runtime.start(this.options.entrypoint ? {
            entrypoint: this.options.entrypoint, enableInternet: true,
          } : undefined);
        } else {
          const source = saved ? { containerSnapshot: saved.handle } : { image: this.options.image };
          this.runtime.start({ ...source, instance: this.options.instance, enableInternet: true,
            env: startupEnv,
            ...(this.options.entrypoint ? { entrypoint: this.options.entrypoint } : {}),
          });
        }
        started = true;
        this.phase = "allocating";
        // start() returns before allocation finishes. Observe failures until
        // readiness, including errors not delivered by the timeout operation.
        const exited = this.runtime.monitor().then(() => {
          throw new Error("Container exited before becoming ready");
        });
        let allocationTimer: ReturnType<typeof setTimeout> | undefined;
        try {
          await Promise.race([
            this.runtime.setInactivityTimeout(IDLE_TIMEOUT_MS), exited,
            new Promise<never>((_, reject) => {
              allocationTimer = setTimeout(() => reject(new Error("Container allocation exceeded 90 seconds")), 90_000);
            }),
          ]);
        } finally { if (allocationTimer !== undefined) clearTimeout(allocationTimer); }
        const readinessAbort = new AbortController();
        this.phase = "waiting-health";
        try { await Promise.race([this.waitReady(readinessAbort.signal), exited]); }
        finally { readinessAbort.abort(); }
        break;
      } catch (error) {
        if (error instanceof Error && /no container instance that can be provided/i.test(error.message)) {
          if (this.now() >= capacityDeadline) throw error;
          // Clear the failed asynchronous start before requesting another
          // instance; otherwise subsequent runtime calls can retain its error.
          this.phase = "clearing-failed-allocation";
          await this.runtime.destroy();
          await this.delay(1000);
          continue;
        }
        if (started) throw error;
        if (attempt >= 2) throw error;
        await this.delay(250);
      }
    }
    this.phase = "saving-running-image";
    await this.storage.put(RUNNING_IMAGE_KEY, this.options.image);
  }

  private async secret(key: string): Promise<string> {
    const existing = await this.storage.get<string>(key);
    if (existing !== undefined) {
      if (!/^[a-f0-9]{64}$/.test(existing)) throw new Error("Stored demo authentication key is invalid");
      return existing;
    }
    const secret = Array.from(crypto.getRandomValues(new Uint8Array(32)), (byte) => byte.toString(16).padStart(2, "0")).join("");
    await this.storage.put(key, secret);
    return secret;
  }

  private async waitReady(signal?: AbortSignal): Promise<void> {
    const deadline = this.now() + 90_000;
    do {
      signal?.throwIfAborted();
      if (await this.ready()) return;
      await this.delay(500);
    } while (this.now() < deadline);
    throw new Error("ShinyHub did not become ready within 90 seconds");
  }
}
