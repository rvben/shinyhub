import { appendFile } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { setTimeout as delay } from "node:timers/promises";

const minute = 60_000;
// The monitor evaluates the previous ten minutes. Leave another minute for
// clock skew between the runner and Cloudflare.
const drainTime = 11 * minute;
const deploymentTimeout = 30 * minute;
const runnerQueueAllowance = 5 * minute;
const policyName = "ShinyHub demo — Worker errors";
const validID = value => typeof value === "string" && /^(?:[a-f0-9]{32}|[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12})$/.test(value);
const normalizedID = value => validID(value) ? value.replaceAll("-", "") : undefined;

export async function manageDemoSilence(action, env = process.env, { fetchAPI = fetch, now = Date.now, wait = delay } = {}) {
  const { CLOUDFLARE_ACCOUNT_ID: account, CLOUDFLARE_DEMO_WORKER_ERRORS_POLICY_ID: policy,
    CLOUDFLARE_NOTIFICATIONS_API_TOKEN: token, SHINYHUB_DEMO_SILENCE_ID: id } = env;
  if (!["start", "success", "failure"].includes(action)) throw new Error("Usage: demo-alert-silence.mjs start|success|failure");
  if (!/^[a-f0-9]{32}$/.test(account ?? "")) throw new Error("CLOUDFLARE_ACCOUNT_ID must be a 32-character hexadecimal ID");
  if (!validID(policy)) throw new Error("CLOUDFLARE_DEMO_WORKER_ERRORS_POLICY_ID must be a Cloudflare policy ID");
  if (!token || /\s/.test(token)) throw new Error("Set CLOUDFLARE_NOTIFICATIONS_API_TOKEN to the bare Notifications Write token");
  if (action !== "start" && !validID(id)) throw new Error("Missing or invalid deployment silence ID");
  const policyID = normalizedID(policy);
  const base = `https://api.cloudflare.com/client/v4/accounts/${account}/alerting/v3`;
  const verifiedAt = env.SHINYHUB_DEMO_VERIFIED_AT;
  const cooldownEnd = Number(verifiedAt) * 1_000 + drainTime;
  if (action === "success" && (!/^[0-9]{10}$/.test(verifiedAt ?? "") || cooldownEnd > now() + drainTime + 60_000)) {
    throw new Error("Missing or invalid Chromium and transport verification time");
  }

  async function api(path, method = "GET", body, allowMissing = false) {
    const resource = path.startsWith("/policies/") ? "policy" : "silence";
    const label = `Cloudflare ${method} ${resource}`;
    let response;
    try {
      response = await fetchAPI(base + path, {
        method, redirect: "error", signal: AbortSignal.timeout(8_000),
        headers: { authorization: `Bearer ${token}`, "content-type": "application/json", "user-agent": "ShinyHub-Demo-Deploy/1.0" },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
    } catch {
      throw new Error(`${label} did not receive a response`);
    }
    if (allowMissing && response.status === 404) return null;
    if (!response.ok) throw new Error(`${label} failed (HTTP ${response.status})`);
    const result = await response.json().catch(() => null);
    if (result?.success !== true) throw new Error(`${label} returned an invalid or unsuccessful result`);
    return result.result;
  }

  async function list() {
    const silences = await api("/silences");
    if (!Array.isArray(silences)) throw new Error("Cloudflare returned an invalid silence list");
    return silences;
  }

  async function confirm(read) {
    let lastError;
    for (let attempt = 0; attempt < 4; attempt++) {
      try {
        const confirmed = await read();
        if (confirmed) return confirmed;
        lastError = undefined;
      } catch (error) {
        lastError = error;
      }
      if (attempt < 3) await wait(1_000);
    }
    if (lastError) throw lastError;
    return undefined;
  }

  if (action === "start") {
    const configured = await api(`/policies/${policy}`);
    if (normalizedID(configured?.id) !== policyID || configured?.name !== policyName
      || configured?.alert_type !== "custom_analytics" || configured?.enabled !== true) {
      throw new Error(`Expected the enabled Cloudflare monitor named ${policyName}`);
    }
    const existing = new Set((await list()).map(silence => normalizedID(silence.id)));
    const scheduled = Math.ceil(now() / 1_000) * 1_000;
    const starts = scheduled + 5_000;
    const expires = scheduled + runnerQueueAllowance + deploymentTimeout + drainTime;
    const window = { policy_id: policy, start_time: new Date(starts).toISOString(), end_time: new Date(expires).toISOString() };

    // Creating the same silence twice after a network failure could leave an
    // unowned mute. Resolve ambiguous delivery by reading back the new window.
    let createError;
    try { await api("/silences", "POST", [window]); }
    catch (error) { createError = error; }
    let matches;
    try {
      matches = await confirm(async () => {
        const found = (await list()).filter(silence => validID(silence.id) && !existing.has(normalizedID(silence.id))
          && normalizedID(silence.policy_id) === policyID && Date.parse(silence.start_time) === starts
          && Date.parse(silence.end_time) === expires);
        return found.length ? found : undefined;
      });
      if (matches?.length !== 1) throw createError ?? new Error("Cloudflare did not confirm exactly one new silence");
    } catch (error) {
      throw new Error(`${error.message}. Any unconfirmed rollout silence expires by ${window.end_time}`);
    }
    await wait(Math.max(0, starts - now()));
    return { id: matches[0].id, endTime: window.end_time };
  }

  const path = `/silences/${id}`;
  const silence = await api(path, "GET", undefined, true);
  const remove = action === "failure" || now() >= cooldownEnd;
  if (silence === null) {
    if (remove) return {};
    throw new Error("The deployment silence disappeared before the alert window cleared");
  }
  if (normalizedID(silence?.id) !== normalizedID(id) || normalizedID(silence.policy_id) !== policyID) {
    throw new Error("Cloudflare silence does not belong to the demo Worker errors monitor");
  }
  if (remove) {
    try { await api(path, "DELETE", undefined, true); }
    catch (error) {
      if (!await confirm(async () => await api(path, "GET", undefined, true) === null)) throw error;
    }
    return {};
  }
  const currentEnd = Date.parse(silence.end_time);
  if (!Number.isFinite(currentEnd)) throw new Error("Cloudflare returned an invalid silence expiry");
  const end = cooldownEnd;
  if (end > currentEnd) throw new Error("The deployment silence expires before the alert window clears");
  const endTime = new Date(end).toISOString();
  if (end < currentEnd) {
    let updateError;
    try { await api("/silences", "PUT", [{ id, end_time: endTime }]); }
    catch (error) { updateError = error; }
    const shortened = await confirm(async () => {
      const updated = await api(path);
      return Date.parse(updated?.end_time) === end;
    });
    if (!shortened) throw updateError ?? new Error("Cloudflare did not confirm the shortened silence expiry");
  }
  return { endTime };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const action = process.argv[2];
    if (action === "start" && (!process.env.GITHUB_OUTPUT || !/^[1-9][0-9]*$/.test(process.env.GITHUB_RUN_ATTEMPT ?? ""))) {
      throw new Error("A GitHub Actions workflow run attempt is required to record the deployment silence");
    }
    const result = await manageDemoSilence(action);
    if (result.id) await appendFile(process.env.GITHUB_OUTPUT,
      `silence_id=${result.id}\nsilence_expiry=${Date.parse(result.endTime) / 1_000}\nworkflow_attempt=${process.env.GITHUB_RUN_ATTEMPT}\n`);
    if (result.endTime) console.log(`Demo Worker errors notifications are silenced until ${result.endTime}`);
    else console.log("No deployment silence remains for the demo Worker errors monitor");
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
