(() => {
  "use strict";

  // DESIGN CONTRACT
  // THESIS: A support session is a temporary safety state, never an invisible identity switch.
  // OWN-WORLD: Constellation's dark control surface gains one unmistakable amber safety rail.
  // STORY: Name the represented user, show the hard deadline, then offer a single clean exit.
  // FIRST VIEWPORT: The rail is a direct document child, outside SPA-managed body flow, and remains sticky while scrolling.
  // FORM: A compact status line, tabular countdown, and explicit destructive stop control.
  // FINISH: The experience is complete only when "End support session" visibly returns control.

  const loader = document.currentScript;
  if (!loader || document.getElementById("shinyhub-support-session")) return;

  const host = document.createElement("div");
  host.id = "shinyhub-support-session";
  host.setAttribute("data-shinyhub-platform-ui", "support-session");
  host.style.cssText = "display:block!important;position:sticky!important;top:0!important;z-index:2147483647!important;width:100%!important;isolation:isolate!important;";
  const root = host.attachShadow({ mode: "closed" });
  const actor = loader.dataset.actor || "Administrator";
  const subject = loader.dataset.subject || "this user";
  const expiresAt = Number(loader.dataset.expiresAt || 0);
  const stopURL = loader.dataset.stopUrl || "";

  const css = `
    :host { color-scheme: dark; }
    * { box-sizing: border-box; }
    .rail {
      min-height: 52px; display: flex; align-items: center; gap: 14px;
      padding: 8px clamp(12px, 2vw, 24px); color: #fff8e7;
      background: #21170a; border-bottom: 1px solid #a86608;
      box-shadow: 0 8px 24px rgba(15, 9, 2, .28);
      font: 600 14px/1.35 ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    .mark { width: 28px; height: 28px; flex: 0 0 auto; color: #fbbf24; }
    .copy { min-width: 0; flex: 1 1 auto; }
    .title { font-weight: 760; letter-spacing: -.012em; overflow-wrap: anywhere; }
    .detail { color: #f7dca3; font-size: 12px; font-weight: 520; margin-top: 1px; overflow-wrap: anywhere; }
    .timer { white-space: nowrap; color: #ffe9b8; font-variant-numeric: tabular-nums; font-size: 12px; }
    form { margin: 0; display: flex; }
    .sr { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
    button {
      appearance: none; border: 1px solid #f59e0b; border-radius: 8px;
      background: #f59e0b; color: #241604; padding: 7px 11px;
      font: 750 12px/1.2 ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      cursor: pointer; white-space: nowrap; transition: background-color 150ms ease-out, box-shadow 150ms ease-out;
    }
    button:hover { background: #fbbf24; box-shadow: 0 4px 14px rgba(245, 158, 11, .22); }
    button:focus-visible { outline: 3px solid #fff8e7; outline-offset: 2px; }
    button:disabled { cursor: wait; opacity: .66; box-shadow: none; }
    .error { color: #f87171; font-size: 12px; margin-top: 2px; overflow-wrap: anywhere; }
    ::selection { background: #f59e0b; color: #21170a; }
    @media (max-width: 620px) {
      .rail { align-items: flex-start; gap: 9px; padding: 9px 10px; flex-wrap: wrap; }
      .mark { width: 24px; height: 24px; }
      .copy { flex-basis: calc(100% - 36px); }
      .timer { margin-left: 33px; }
      form { margin-left: auto; }
      button { min-block-size: 44px; }
    }
    @media (prefers-reduced-motion: reduce) { button { transition: none; } }
  `;

  if (typeof CSSStyleSheet === "function" && "adoptedStyleSheets" in root) {
    try {
      const sheet = new CSSStyleSheet();
      sheet.replaceSync(css);
      root.adoptedStyleSheets = [sheet];
    } catch (_) {
      const style = document.createElement("style");
      style.textContent = css;
      root.append(style);
    }
  } else {
    const style = document.createElement("style");
    style.textContent = css;
    root.append(style);
  }

  const rail = document.createElement("div");
  rail.className = "rail";
  rail.setAttribute("role", "region");
  rail.setAttribute("aria-label", "Active ShinyHub support session");
  const svgNS = "http://www.w3.org/2000/svg";
  const mark = document.createElementNS(svgNS, "svg");
  mark.setAttribute("class", "mark");
  mark.setAttribute("viewBox", "0 0 24 24");
  mark.setAttribute("fill", "none");
  mark.setAttribute("aria-hidden", "true");
  const triangle = document.createElementNS(svgNS, "path");
  triangle.setAttribute("d", "M12 3 21 19H3L12 3Z");
  triangle.setAttribute("stroke", "currentColor");
  triangle.setAttribute("stroke-width", "1.8");
  triangle.setAttribute("stroke-linejoin", "round");
  const stem = document.createElementNS(svgNS, "path");
  stem.setAttribute("d", "M12 8.5v5");
  stem.setAttribute("stroke", "currentColor");
  stem.setAttribute("stroke-width", "1.8");
  stem.setAttribute("stroke-linecap", "round");
  const dot = document.createElementNS(svgNS, "circle");
  dot.setAttribute("cx", "12"); dot.setAttribute("cy", "16.6"); dot.setAttribute("r", "1"); dot.setAttribute("fill", "currentColor");
  mark.append(triangle, stem, dot);

  const copy = document.createElement("div");
  copy.className = "copy";
  const title = document.createElement("div");
  title.className = "title";
  title.append(document.createTextNode("Support session · Viewing as "));
  const subjectName = document.createElement("strong");
  subjectName.textContent = subject;
  title.append(subjectName);
  const detail = document.createElement("div");
  detail.className = "detail";
  detail.textContent = `${actor} is the administrator. Actions here can change ${subject}’s data.`;
  const errorNode = document.createElement("div");
  errorNode.className = "error";
  errorNode.setAttribute("role", "alert");
  errorNode.hidden = true;
  copy.append(title, detail, errorNode);
  const timerNode = document.createElement("div");
  timerNode.className = "timer";
  const live = document.createElement("span");
  live.className = "sr";
  live.setAttribute("aria-live", "polite");
  live.setAttribute("aria-atomic", "true");
  const endForm = document.createElement("form");
  endForm.method = "post";
  endForm.action = stopURL;
  const endButton = document.createElement("button");
  endButton.type = "submit";
  endButton.textContent = "End support session";
  endForm.append(endButton);
  rail.append(mark, copy, timerNode, live, endForm);
  root.append(rail);

  const timer = timerNode;
  const status = live;
  const button = endButton;
  const error = errorNode;
  let expiryStopAttempted = false;
  let lastAnnouncement = "";

  const finish = (automatic = false) => {
    if (button.disabled || !stopURL) return;
    if (automatic) expiryStopAttempted = true;
    button.disabled = true;
    button.textContent = "Ending…";
    endForm.submit();
  };

  const tick = () => {
    keepMounted();
    const remaining = Math.max(0, expiresAt - Date.now());
    const total = Math.ceil(remaining / 1000);
    const minutes = Math.floor(total / 60);
    const seconds = String(total % 60).padStart(2, "0");
    timer.textContent = remaining > 0 ? `Ends in ${minutes}:${seconds}` : "Session expired";
    const announcement = remaining <= 0
      ? "Support session expired."
      : (remaining <= 60000 ? "Support session ends in one minute."
        : (remaining <= 300000 ? "Support session ends in five minutes." : ""));
    if (announcement && announcement !== lastAnnouncement) {
      lastAnnouncement = announcement;
      status.textContent = announcement;
    }
    if (remaining <= 1000 && !expiryStopAttempted) finish(true);
  };

  endForm.addEventListener("submit", (event) => {
    event.preventDefault();
    finish(false);
  });
  let observedRoot = null;
  const observer = new MutationObserver(() => keepMounted());
  function keepMounted() {
    const html = document.documentElement;
    if (!html) return;
    html.setAttribute("data-shinyhub-support-session", "active");
    if (observedRoot !== html) {
      observer.disconnect();
      observer.observe(html, { childList: true });
      observedRoot = html;
    }
    if (host.parentNode !== html) {
      html.insertBefore(host, document.body || null);
    }
  }
  keepMounted();
  document.getElementById("shinyhub-support-session-fallback")?.remove();
  tick();
  window.setInterval(tick, 1000);
  window.dispatchEvent(new CustomEvent("shinyhub:support-session-active", { detail: { subject, actor } }));
})();
