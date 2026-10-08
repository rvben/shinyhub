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
});
