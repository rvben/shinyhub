import { Container, getContainer } from "@cloudflare/containers";
import demoEntrypoint from "../entrypoint.sh";
import { createDemoWorker } from "./demo-worker.ts";
import { DEMO_HOST, APP_HOST } from "./edge-policy.ts";

interface Env {
  SHINYHUB_DEMO: DurableObjectNamespace<ShinyHubDemo>;
}

export class ShinyHubDemo extends Container {
  defaultPort = 8080;
  sleepAfter = "10m";
  // Reuse the pinned production release image with the validated startup gate.
  entrypoint = ["/bin/sh", "-c", demoEntrypoint];
}

export default createDemoWorker<Env>({
  controlHost: DEMO_HOST,
  appHost: APP_HOST,
  container: (env) => getContainer(env.SHINYHUB_DEMO, "public-demo"),
});
