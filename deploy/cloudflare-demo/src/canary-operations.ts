import { authorized } from "./container-preview.ts";

export interface CanaryOperations {
  state(): Promise<unknown>;
  start(): Promise<unknown>;
  stop(): Promise<void>;
  snapshot(): Promise<{ size: number }>;
  forgetSnapshot(): Promise<void>;
  terminate(): Promise<void>;
}

const operations: Record<string, string[]> = {
  "/__canary/state": ["GET"],
  "/__canary/start": ["POST"],
  "/__canary/stop": ["POST"],
  "/__canary/snapshot": ["POST", "DELETE"],
  "/__canary/terminate": ["POST"],
};

export async function handleCanaryOperations(
  request: Request, token: string | undefined, resolve: () => CanaryOperations,
): Promise<Response> {
  const headers = { "cache-control": "no-store" };
  // Authenticate and validate the operation before even resolving a DO stub.
  if (!authorized(request, token)) return new Response("Unauthorized", { status: 401, headers });
  const path = new URL(request.url).pathname;
  const methods = operations[path];
  if (!methods) return new Response("Not found", { status: 404, headers });
  if (!methods.includes(request.method)) return new Response("Method not allowed", {
    status: 405, headers: { ...headers, allow: methods.join(", ") },
  });
  const container = resolve();
  switch (request.method + " " + path) {
    case "GET /__canary/state": return Response.json(await container.state(), { headers });
    case "POST /__canary/start": return Response.json(await container.start(), { headers });
    case "POST /__canary/stop": await container.stop(); break;
    case "POST /__canary/snapshot": {
      const snapshot = await container.snapshot();
      // The opaque restore handle stays in private DO storage.
      return Response.json({ size: snapshot.size }, { headers });
    }
    case "DELETE /__canary/snapshot": await container.forgetSnapshot(); break;
    case "POST /__canary/terminate": await container.terminate(); break;
  }
  return new Response(null, { status: 204, headers });
}
