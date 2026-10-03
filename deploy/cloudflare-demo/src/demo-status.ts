import { isAsleep } from "./edge-policy.ts";

export const DEMO_STATUS_PATH = "/__demo/status";

// Monitoring reads the SDK's last observed lifecycle state. It never proxies a
// request or starts a container, including while the container is already warm.
export async function passiveDemoStatus(
  method: string,
  container: { getState(): Promise<{ status: string }> },
): Promise<Response> {
  if (method !== "GET") {
    return new Response("Method not allowed", {
      status: 405,
      headers: { allow: "GET", "cache-control": "no-store" },
    });
  }
  const { status } = await container.getState();
  const state = isAsleep(status) ? "asleep" : status === "healthy" ? "ready" : "starting";
  return Response.json({ state }, { headers: { "cache-control": "no-store" } });
}
