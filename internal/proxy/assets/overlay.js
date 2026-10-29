/*
 * Status overlay: explains a mid-session disconnect that the app itself cannot
 * explain.
 *
 * The wait pages (loadingPage, deployingPage) tell a visitor why the app is not
 * there when they arrive. Nothing told them why it left while they were using
 * it: Shiny's own grey overlay reports the socket dropped, which is true and
 * useless, because the app process is the one thing that cannot know whether it
 * was hibernated, redeployed, or simply died. ShinyHub knows. This asks it.
 *
 * Three properties keep an injected script that runs inside every app on the
 * platform from becoming a fleet-wide outage:
 *
 *   1. It does not call a framework internal. It wraps WebSocket only to tag
 *      this app's /websocket/ URL with a random connection ID; every other URL
 *      reaches the native constructor unchanged. Its disconnect UI depends on
 *      Shiny appending a #shiny-disconnected-overlay
 *      div to <body> on disconnect and removing it on reconnect, which is a
 *      public, user-styleable DOM contract shared by R and Python Shiny. During
 *      safe automatic recovery it makes that marker transparent, retaining its
 *      pointer blocker. After
 *      an explicit viewer choice it may hide that marker so the retained page
 *      can be inspected as a clearly labelled offline snapshot.
 *   2. Every entry point is wrapped. A throw here must never escape into a page
 *      whose app is already in trouble.
 *   3. An app that never emits that div never sees the status UI. The socket
 *      wrapper leaves every route except this app's /websocket/ unchanged.
 *
 * Every value that varies per app rides on the script tag's data-* attributes
 * and never in this body. A CSP hash covers script text only, so keeping the
 * text byte-identical across apps means one hash admits the overlay everywhere.
 */
(function () {
  "use strict";

  var SHINY_OVERLAY_ID = "shiny-disconnected-overlay";
  var OWN_ID = "shinyhub-status-overlay";
  var TITLE_ID = "shinyhub-status-title";
  var MESSAGE_ID = "shinyhub-status-message";
  var OPEN_ID = "shinyhub-status-open";
  var RELOAD_ID = "shinyhub-status-reload";
  var RESTART_ID = "shinyhub-status-restart";
  var DOT_ID = "shinyhub-status-dot";
  var SESSION_EVENT = "shinyhub:session-status";
  var SESSION_OWNER_EVENT = "shinyhub:session-status-owner";

  var tag = document.currentScript;
  if (!tag || typeof window.fetch !== "function" || !window.MutationObserver) {
    return;
  }
  var readyURL = tag.getAttribute("data-ready-url");
  if (!readyURL) {
    return;
  }
  var pollMs = toInt(tag.getAttribute("data-poll-ms"), 3000);
  var maxPolls = toInt(tag.getAttribute("data-max-polls"), 20);
  // The WebSocket API hides a rejected upgrade's status and Retry-After from
  // page JavaScript. The proxy supplies its render retry advice here instead.
  var retryMs = toInt(tag.getAttribute("data-retry-seconds"), 2) * 1000;
  var capacityKey = "shinyhub-capacity:" + window.location.pathname;
  var capacityBudgetMs = 60000;
  try {
    var nav = (window.performance.getEntriesByType &&
      window.performance.getEntriesByType("navigation")[0]) || {};
    if (nav.type !== "reload") window.sessionStorage.removeItem(capacityKey);
  } catch (e) {
    // Storage can be disabled by the browser; keep retrying within this page.
  }

  var polls = 0;
  var timer = null;
  var reloadTimer = null;
  var showing = false;
  var currentState = null;
  var previousFocus = null;
  var everConnected = false;
  var memoryDeadline = 0;
  var activeConnectionID = "";
  var endedConnectionID = "";
  var incidentConnectionID = "";
  var recoveryTimer = null;
  var recoveryDeadline = null;
  var restoreTimer = null;
  var recoveryStarted = 0;
  var quietMarker = null;
  var markerOpacity = "";
  var markerOpacityPriority = "";
  var markerAriaHidden = null;
  var recoveryKey = "shinyhub-recovery:" + window.location.pathname;
  var incidentSerial = 0;
  var probeSerial = 0;
  var probeTimer = null;
  var probeAbort = null;

  function cancelProbe() {
    probeSerial++;
    if (probeTimer !== null) window.clearTimeout(probeTimer);
    probeTimer = null;
    if (probeAbort) probeAbort.abort();
    probeAbort = null;
  }

  function canRestore() {
    if (!everConnected || typeof window.CustomEvent !== "function" ||
        document.getElementById("shinyhub-session-notice")) return false;
    try {
      // Keep the circuit breaker across reloads, including a briefly successful
      // connection. With storage disabled, keep the safe manual fallback.
      var last = Number(window.sessionStorage.getItem(recoveryKey));
      if (last && Date.now() - last < 60000) return false;
      return !window.dispatchEvent(new window.CustomEvent("shinyhub:session-recovery", { cancelable: true }));
    } catch (e) { return false; }
  }

  function cancelRecoveryNotice() {
    if (recoveryTimer !== null) window.clearTimeout(recoveryTimer);
    if (recoveryDeadline !== null) window.clearTimeout(recoveryDeadline);
    if (restoreTimer !== null) window.clearTimeout(restoreTimer);
    recoveryTimer = null;
    recoveryDeadline = null;
    restoreTimer = null;
  }

  function quietDisconnectMarker() {
    var marker = document.getElementById(SHINY_OVERLAY_ID);
    if (!marker || marker === quietMarker) return;
    restoreDisconnectMarker();
    quietMarker = marker;
    markerOpacity = marker.style.getPropertyValue("opacity");
    markerOpacityPriority = marker.style.getPropertyPriority("opacity");
    markerAriaHidden = marker.getAttribute("aria-hidden");
    // Keep Shiny's pointer blocker in place while avoiding a grey flash.
    // Keyboard edits are still observed by the bookmark bridge and veto reload.
    marker.style.setProperty("opacity", "0", "important");
    marker.setAttribute("aria-hidden", "true");
  }

  function restoreDisconnectMarker() {
    if (!quietMarker) return;
    if (markerOpacity) quietMarker.style.setProperty("opacity", markerOpacity, markerOpacityPriority);
    else quietMarker.style.removeProperty("opacity");
    if (markerAriaHidden === null) quietMarker.removeAttribute("aria-hidden");
    else quietMarker.setAttribute("aria-hidden", markerAriaHidden);
    quietMarker = null;
  }

  function queueRestore() {
    stopPolling();
    // Give Shiny a short chance to reconnect its existing session before
    // replacing the page. Recheck the bookmark at the moment of navigation.
    restoreTimer = window.setTimeout(guard(function () {
      restoreTimer = null;
      if (!showing || currentState !== "recovering") return;
      if (window.navigator.onLine === false) { recoveryNotice(); schedule(); return; }
      if (!restoreView()) recovered();
    }), Math.max(0, 750 - (Date.now() - recoveryStarted)));
  }

  function restoreView() {
    if (currentState !== "recovering" || !canRestore()) return false;
    try { window.sessionStorage.setItem(recoveryKey, String(Date.now())); }
    catch (e) { return false; }
    cancelRecoveryNotice();
    cancelProbe();
    stopPolling();
    currentState = "restoring";
    // Reload preserves the complete current query and hash. Shiny restores the
    // registered filters using its normal bookmark lifecycle.
    window.location.reload();
    return true;
  }

  function recoveryNotice() {
    if (!ui) ui = build();
    ui.openLink.href = window.location.href;
    ui.root.className = "is-snapshot";
    ui.root.style.display = "flex";
    ui.root.style.inset = "auto 16px 16px";
    ui.root.style.padding = "0";
    ui.root.style.background = "transparent";
    ui.root.removeAttribute("aria-modal");
    ui.root.setAttribute("role", "status");
    ui.box.style.padding = "16px 20px";
    ui.box.style.display = "block";
    ui.box.style.maxWidth = "520px";
    ui.box.style.margin = "0";
    var offline = window.navigator.onLine === false;
    ui.title.textContent = offline ? "You’re offline" : "Reconnecting to your dashboard";
    ui.msg.textContent = offline
      ? "We’ll reconnect when you’re back online. Your last results are still here."
      : "We’ll restore your saved filters. You can also keep these results open.";
    ui.spinner.style.display = offline ? "none" : "block";
    ui.stateDot.style.display = offline ? "block" : "none";
    ui.reference.style.display = "none";
    ui.actions.style.display = "flex";
    ui.actions.style.marginTop = "12px";
    show(ui.openLink, false);
    show(ui.reloadButton, false);
    show(ui.restartButton, false);
    show(ui.keepButton, true);
    if (!ui.root.parentNode) document.body.appendChild(ui.root);
    // A status update must not take keyboard focus from the dashboard.
  }

  // The browser hides 101 response headers from JavaScript. Tag the Shiny
  // socket before it opens so the browser and proxy know the same ID. If this
  // script runs after Shiny has already opened its socket, omit the ID rather
  // than display a guess from another tab or connection.
  function installWebSocketTagger() {
    var NativeWebSocket = window.WebSocket;
    var suffix = "/.shinyhub/ready";
    if (typeof NativeWebSocket !== "function" || !window.URL ||
        !window.crypto || !window.crypto.getRandomValues ||
        readyURL.slice(-suffix.length) !== suffix) return;
    var appPath = readyURL.slice(0, -suffix.length);
    function TaggedWebSocket(url, protocols) {
      if (!(this instanceof TaggedWebSocket)) {
        throw new TypeError("WebSocket must be constructed with new");
      }
      var target = url;
      var id = "";
      try {
        var rawURL = String(url);
        var parsed = new window.URL(rawURL, window.location.href);
        if ((parsed.protocol === "ws:" || parsed.protocol === "wss:") &&
            parsed.host === window.location.host && !parsed.hash &&
            (parsed.pathname === appPath + "/websocket/" ||
             parsed.pathname === appPath + "/websocket")) {
          var bytes = new Uint8Array(12);
          window.crypto.getRandomValues(bytes);
          for (var i = 0; i < bytes.length; i++) {
            id += ("0" + bytes[i].toString(16)).slice(-2);
          }
          target = rawURL + (rawURL.indexOf("?") < 0 ? "?" : "&") +
            "shinyhub_cid=" + id;
        }
      } catch (e) {
        id = "";
        target = url;
      }
      var socket = arguments.length > 1
        ? new NativeWebSocket(target, protocols)
        : new NativeWebSocket(target);
      if (id && typeof socket.addEventListener === "function") {
        var opened = false;
        socket.addEventListener("open", function () {
          opened = true;
          activeConnectionID = id;
          endedConnectionID = "";
        });
        socket.addEventListener("close", function () {
          if (opened && activeConnectionID === id) {
            activeConnectionID = "";
            endedConnectionID = id;
          }
        });
      }
      return socket;
    }
    TaggedWebSocket.prototype = NativeWebSocket.prototype;
    if (Object.setPrototypeOf) Object.setPrototypeOf(TaggedWebSocket, NativeWebSocket);
    else {
      TaggedWebSocket.CONNECTING = NativeWebSocket.CONNECTING;
      TaggedWebSocket.OPEN = NativeWebSocket.OPEN;
      TaggedWebSocket.CLOSING = NativeWebSocket.CLOSING;
      TaggedWebSocket.CLOSED = NativeWebSocket.CLOSED;
    }
    window.WebSocket = TaggedWebSocket;
  }
  guard(installWebSocketTagger)();

  function capacityDeadline() {
    var deadline = memoryDeadline;
    try {
      deadline = parseInt(window.sessionStorage.getItem(capacityKey) || "0", 10) || deadline;
    } catch (e) {}
    if (!deadline) {
      deadline = Date.now() + capacityBudgetMs;
      try { window.sessionStorage.setItem(capacityKey, String(deadline)); } catch (e) {}
    }
    memoryDeadline = deadline;
    return deadline;
  }

  function clearCapacityDeadline() {
    memoryDeadline = 0;
    try { window.sessionStorage.removeItem(capacityKey); } catch (e) {}
  }

  function connected() {
    everConnected = true;
    endedConnectionID = "";
    incidentConnectionID = "";
    clearCapacityDeadline();
  }

  // R Shiny raises jQuery document events; native listeners also cover hosts
  // that dispatch the event through the DOM. Register before observing the
  // disconnect marker so a first-connect failure stays distinct from a drop.
  document.addEventListener("shiny:connected", guard(connected));
  guard(function () {
    if (typeof window.jQuery === "function") {
      window.jQuery(document).on("shiny:connected", guard(connected));
    }
  })();

  function toInt(raw, fallback) {
    var n = parseInt(raw, 10);
    return isFinite(n) && n > 0 ? n : fallback;
  }

  function guard(fn) {
    return function () {
      try {
        return fn.apply(null, arguments);
      } catch (e) {
        // Deliberately swallowed. The app is already disconnected; a failure to
        // explain that must not add a second fault on top of the first.
        return undefined;
      }
    };
  }

  // isShinyOverlay reports whether node is Shiny's disconnect marker in the
  // state that means a real disconnect. Shiny sets the "reloading" class when
  // it is tearing the page down on purpose (autoreload in development, a
  // session reload). Give that navigation a grace period before recovery.
  function isShinyOverlay(node) {
    return (
      node &&
      node.nodeType === 1 &&
      node.id === SHINY_OVERLAY_ID &&
      !(node.classList && node.classList.contains("reloading"))
    );
  }

  function el(tagName, styles, text) {
    var n = document.createElement(tagName);
    // Styles are assigned through the CSSOM rather than a style attribute or a
    // <style> block. CSP's style-src governs markup, not CSSOM writes, so this
    // keeps the whole feature inside a single script-src hash.
    for (var k in styles) {
      if (Object.prototype.hasOwnProperty.call(styles, k)) {
        n.style[k] = styles[k];
      }
    }
    if (text !== undefined) {
      n.textContent = text;
    }
    return n;
  }

  function actionStyles(kind) {
    var styles = {
      minHeight: "44px",
      boxSizing: "border-box",
      display: "none",
      alignItems: "center",
      justifyContent: "center",
      padding: "0 18px",
      borderRadius: "8px",
      cursor: "pointer",
      fontFamily: "inherit",
      fontSize: "0.875rem",
      fontWeight: "700",
      lineHeight: "1.2"
    };
    if (kind === "primary") {
      styles.background = "linear-gradient(135deg, #38BDF8, #60A5FA)";
      styles.color = "#030510";
      styles.border = "0";
      styles.textDecoration = "none";
      styles.boxShadow = "0 4px 14px rgba(56,189,248,0.25)";
    } else if (kind === "secondary") {
      styles.boxShadow = "none";
      styles.background = "#0E1426";
      styles.color = "#E8EEFF";
      styles.border = "1px solid #2B3A63";
    } else {
      styles.background = "transparent";
      styles.color = "#7DD3FC";
      styles.border = "1px solid transparent";
    }
    return styles;
  }

  function installStyles(spinner) {
    // CSSOM rules preserve the single script hash while covering interaction,
    // responsive layout, and user motion preferences that inline styles cannot
    // express. Failure leaves a complete, static, inline-styled interface.
    try {
      var style = document.createElement("style");
      document.head.appendChild(style);
      var rules = [
        "@keyframes shinyhub-spin { to { transform: rotate(360deg); } }",
        "@keyframes shinyhub-ready-pulse { 0% { transform: scale(1); box-shadow: 0 0 0 0 rgba(251,191,36,0.42); } 55% { transform: scale(1.08); box-shadow: 0 0 0 8px rgba(251,191,36,0); } 100% { transform: scale(1); box-shadow: 0 0 0 0 rgba(251,191,36,0); } }",
        "#" + OWN_ID + " a, #" + OWN_ID + " button { transition: background-color 160ms ease-out, border-color 160ms ease-out, color 160ms ease-out, box-shadow 160ms ease-out; }",
        "#" + OWN_ID + " a:hover, #" + OWN_ID + " button:hover { filter: brightness(1.08); }",
        "#" + OWN_ID + " a:focus-visible, #" + OWN_ID + " button:focus-visible { outline: 3px solid #38BDF8; outline-offset: 3px; }",
        "#" + OWN_ID + " ::selection { background: #38BDF8; color: #030510; }",
        "@media (max-width: 520px) { #" + OWN_ID + " { padding: 16px !important; align-items: flex-end !important; } #" + OWN_ID + " > div { padding: 22px 18px !important; } #" + OWN_ID + " [data-shinyhub-actions] { flex-direction: column; } #" + OWN_ID + " [data-shinyhub-actions] > * { width: 100%; } #" + OWN_ID + ".is-snapshot { inset: auto 8px 8px 8px !important; padding: 0 !important; } #" + OWN_ID + ".is-snapshot > div { display: block !important; padding: 16px !important; } #" + OWN_ID + ".is-snapshot [data-shinyhub-actions] { margin-top: 16px !important; } }",
        "@media (prefers-reduced-motion: reduce) { #" + OWN_ID + " * { transition: none !important; } #shinyhub-status-spinner, #" + DOT_ID + " { animation: none !important; } }",
        "@media (forced-colors: active) { #" + OWN_ID + " > div { border: 1px solid CanvasText; } #" + OWN_ID + " a, #" + OWN_ID + " button { border: 1px solid ButtonText; } }"
      ];
      for (var i = 0; i < rules.length; i++) {
        style.sheet.insertRule(rules[i], style.sheet.cssRules.length);
      }
    } catch (e) {
      spinner.style.animation = "";
    }
  }

  function build() {
    var root = el(
      "div",
      {
        position: "fixed",
        inset: "0",
        zIndex: "2147483647",
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        boxSizing: "border-box",
        padding: "24px",
        background: "rgba(3,5,16,0.60)",
        color: "#E8EEFF",
        fontFamily:
          'Manrope, -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif'
      },
      undefined
    );
    root.id = OWN_ID;
    root.tabIndex = -1;
    root.setAttribute("role", "dialog");
    root.setAttribute("aria-modal", "true");
    root.setAttribute("aria-labelledby", TITLE_ID);
    root.setAttribute("aria-describedby", MESSAGE_ID);
    root.setAttribute("aria-live", "polite");

    var box = el("div", {
      width: "100%",
      maxWidth: "520px",
      boxSizing: "border-box",
      padding: "28px",
      background: "#141B32",
      borderRadius: "14px",
      boxShadow: "0 32px 80px rgba(0,0,0,0.70)"
    });
    var header = el("div", {
      display: "flex",
      alignItems: "flex-start",
      gap: "16px"
    });
    var signal = el("div", {
      width: "40px",
      height: "40px",
      flex: "0 0 40px",
      display: "flex",
      alignItems: "center",
      justifyContent: "center",
      background: "#0E1426",
      borderRadius: "99px"
    });
    var spinner = el("div", {
      width: "20px",
      height: "20px",
      boxSizing: "border-box",
      border: "2px solid rgba(56,189,248,0.18)",
      borderTopColor: "#38BDF8",
      borderRadius: "50%",
      animation: "shinyhub-spin 0.8s linear infinite"
    });
    spinner.id = "shinyhub-status-spinner";
    spinner.setAttribute("aria-hidden", "true");
    var stateDot = el("div", {
      width: "10px",
      height: "10px",
      display: "none",
      borderRadius: "99px",
      background: "#FBBF24"
    });
    stateDot.id = DOT_ID;
    stateDot.setAttribute("aria-hidden", "true");
    var copy = el("div", {
      minWidth: "0",
      flex: "1"
    });
    var title = el("h1", {
      color: "#E8EEFF",
      fontSize: "1.05rem",
      margin: "0",
      fontWeight: "600",
      lineHeight: "1.3",
      letterSpacing: "-0.01em"
    });
    title.id = TITLE_ID;
    var msg = el("p", {
      color: "#A8B4D4",
      fontSize: "1rem",
      margin: "6px 0 0",
      lineHeight: "1.55",
      maxWidth: "64ch"
    });
    msg.id = MESSAGE_ID;
    var reference = el("p", {
      color: "#A8B4D4",
      fontSize: "0.75rem",
      margin: "12px 0 0",
      overflowWrap: "anywhere"
    });
    reference.id = "shinyhub-status-connection-id";

    var actions = el("div", {
      display: "flex",
      flexWrap: "wrap",
      alignItems: "center",
      gap: "8px",
      marginTop: "24px"
    });
    actions.setAttribute("data-shinyhub-actions", "");

    var openLink = el("a", actionStyles("primary"), "Start in a new tab");
    openLink.id = OPEN_ID;
    openLink.href = window.location.href;
    openLink.target = "_blank";
    openLink.rel = "noopener";
    openLink.setAttribute("aria-label", "Start a new app session in a new tab");

    var reloadButton = el(
      "button",
      actionStyles("primary"),
      "Restart now"
    );
    reloadButton.id = RELOAD_ID;
    reloadButton.type = "button";
    reloadButton.addEventListener(
      "click",
      guard(function () {
        window.location.reload();
      })
    );

    var restartButton = el(
      "button",
      actionStyles("secondary"),
      "Start in this tab"
    );
    restartButton.id = RESTART_ID;
    restartButton.type = "button";
    restartButton.addEventListener(
      "click",
      guard(function () {
        window.location.reload();
      })
    );
    openLink.addEventListener("click", guard(enterSnapshot));
    var keepButton = el("button", actionStyles("quiet"), "Keep these results");
    keepButton.id = "shinyhub-status-keep";
    keepButton.type = "button";
    keepButton.addEventListener("click", guard(enterSnapshot));

    signal.appendChild(spinner);
    signal.appendChild(stateDot);
    copy.appendChild(title);
    copy.appendChild(msg);
    copy.appendChild(reference);
    header.appendChild(signal);
    header.appendChild(copy);
    actions.appendChild(openLink);
    actions.appendChild(reloadButton);
    actions.appendChild(restartButton);
    actions.appendChild(keepButton);
    box.appendChild(header);
    box.appendChild(actions);
    root.appendChild(box);
    installStyles(spinner);

    root.addEventListener(
      "keydown",
      guard(function (event) {
        if (root.getAttribute("aria-modal") !== "true") {
          return;
        }
        if (event.key === "Escape" && everConnected) {
          event.preventDefault();
          enterSnapshot();
          return;
        }
        if (event.key !== "Tab") {
          return;
        }
        var controls = [openLink, reloadButton, restartButton, keepButton];
        var visible = [];
        for (var i = 0; i < controls.length; i++) {
          if (controls[i].style.display !== "none") {
            visible.push(controls[i]);
          }
        }
        if (!visible.length) {
          event.preventDefault();
          root.focus();
          return;
        }
        var first = visible[0];
        var last = visible[visible.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      })
    );

    root.addEventListener(
      "click",
      guard(function (event) {
        if (event.target === root && currentState === "ready") {
          enterSnapshot();
        }
      })
    );

    return {
      root: root,
      box: box,
      header: header,
      actions: actions,
      spinner: spinner,
      stateDot: stateDot,
      title: title,
      msg: msg,
      reference: reference,
      openLink: openLink,
      reloadButton: reloadButton,
      restartButton: restartButton,
      keepButton: keepButton
    };
  }

  var ui = null;

  function show(control, visible) {
    control.style.display = visible ? "inline-flex" : "none";
  }

  function render(state, titleText, msgText, reloadText) {
    cancelRecoveryNotice();
    restoreDisconnectMarker();
    if (!ui) {
      ui = build();
    }
    currentState = state;
    ui.root.className = "";
    ui.root.style.inset = "0";
    ui.root.style.padding = "24px";
    ui.root.style.display = "flex";
    ui.root.style.alignItems = "center";
    ui.root.style.justifyContent = "center";
    ui.root.style.background = "rgba(3,5,16,0.60)";
    ui.root.style.pointerEvents = "auto";
    ui.root.setAttribute("role", "dialog");
    ui.root.setAttribute("aria-modal", "true");
    ui.box.style.maxWidth = "520px";
    ui.box.style.margin = "0";
    ui.box.style.padding = "28px";
    ui.box.style.display = "block";
    ui.box.style.alignItems = "";
    ui.box.style.gap = "";
    ui.box.style.pointerEvents = "auto";
    ui.header.style.flex = "";
    ui.header.style.alignItems = "flex-start";
    ui.actions.style.flexWrap = "wrap";
    ui.actions.style.display = "flex";
    ui.actions.style.marginTop = "24px";
    ui.openLink.href = window.location.href;
    ui.title.textContent = titleText;
    ui.title.style.color = "#E8EEFF";
    ui.msg.textContent = msgText;
    ui.reference.textContent = incidentConnectionID
      ? "Connection ID: " + incidentConnectionID : "";
    ui.reference.style.display = incidentConnectionID ? "block" : "none";
    ui.spinner.style.display = state === "waiting" || state === "busy" ? "block" : "none";
    ui.stateDot.style.display = state === "waiting" || state === "busy" ? "none" : "block";
    ui.stateDot.style.background = state === "error" ? "#F87171" : "#FBBF24";
    ui.stateDot.style.animation =
      state === "ready"
        ? "shinyhub-ready-pulse 760ms cubic-bezier(0.16,1,0.3,1) 2"
        : "";
    Object.assign(ui.reloadButton.style, actionStyles(state === "waiting" || state === "unavailable" ? "secondary" : "primary"));
    show(ui.openLink, state === "ready" || state === "waiting" || state === "unavailable");
    show(ui.restartButton, state === "ready");
    show(ui.keepButton, everConnected);
    show(ui.reloadButton, !!reloadText);
    if (reloadText) {
      ui.reloadButton.textContent = reloadText;
    }
    if (!ui.root.parentNode) {
      document.body.appendChild(ui.root);
    }
    if (state === "ready" || state === "waiting" || state === "unavailable") {
      ui.openLink.focus();
    } else if (reloadText) {
      ui.reloadButton.focus();
    } else {
      ui.root.focus();
    }
  }

  function enterSnapshot() {
    if (!ui || !everConnected || currentState === "restoring" || currentState === "snapshot") {
      return;
    }
    incidentSerial++;
    cancelRecoveryNotice();
    cancelProbe();
    restoreDisconnectMarker();
    stopPolling();
    currentState = "snapshot";
    var marker = document.getElementById(SHINY_OVERLAY_ID);
    if (marker) {
      marker.style.display = "none";
      marker.setAttribute("aria-hidden", "true");
    }
    ui.root.className = "is-snapshot";
    ui.root.style.inset = "auto 16px 16px 16px";
    ui.root.style.padding = "0";
    ui.root.style.display = "block";
    ui.root.style.background = "transparent";
    ui.root.style.pointerEvents = "none";
    ui.root.removeAttribute("aria-modal");
    ui.root.setAttribute("role", "status");
    ui.box.style.maxWidth = "680px";
    ui.box.style.margin = "0 auto";
    ui.box.style.padding = "14px 16px";
    ui.box.style.display = "flex";
    ui.box.style.alignItems = "center";
    ui.box.style.gap = "16px";
    ui.box.style.pointerEvents = "auto";
    ui.header.style.flex = "1";
    ui.header.style.alignItems = "center";
    ui.actions.style.flexWrap = "nowrap";
    ui.actions.style.marginTop = "0";
    ui.title.textContent = "Previous results";
    ui.msg.textContent =
      "This snapshot may be out of date. Its controls no longer update; open a new session to continue.";
    ui.spinner.style.display = "none";
    ui.stateDot.style.display = "block";
    ui.stateDot.style.background = "#FBBF24";
    ui.stateDot.style.animation = "";
    show(ui.openLink, true);
    show(ui.reloadButton, false);
    show(ui.restartButton, true);
    show(ui.keepButton, false);
    if (publishSnapshot(true)) {
      ui.root.style.display = "none";
    } else {
      ui.root.focus();
    }
  }

  function publishSnapshot(moveFocus) {
    if (typeof window.CustomEvent !== "function") {
      return false;
    }
    var event = new window.CustomEvent(SESSION_EVENT, {
      cancelable: true,
      detail: {
        state: "snapshot",
        url: window.location.href,
        focus: !!moveFocus
      }
    });
    return window.dispatchEvent(event) === false;
  }

  function publishConnected() {
    if (typeof window.CustomEvent !== "function") {
      return;
    }
    window.dispatchEvent(
      new window.CustomEvent(SESSION_EVENT, { detail: { state: "connected" } })
    );
  }

  function stopPolling() {
    if (timer !== null) {
      window.clearTimeout(timer);
      timer = null;
    }
  }

  function teardown() {
    incidentSerial++;
    cancelRecoveryNotice();
    cancelProbe();
    restoreDisconnectMarker();
    stopPolling();
    connected();
    showing = false;
    polls = 0;
    publishConnected();
    currentState = null;
    incidentConnectionID = "";
    var heldFocus = ui && ui.root.contains(document.activeElement);
    if (ui && ui.root.parentNode) {
      ui.root.parentNode.removeChild(ui.root);
    }
    if (
      heldFocus && previousFocus &&
      previousFocus !== document.body &&
      document.documentElement.contains(previousFocus) &&
      typeof previousFocus.focus === "function"
    ) {
      previousFocus.focus();
    }
    previousFocus = null;
  }

  // A new tab is offered even before readiness: on single-node servers the
  // readiness probe waits for a WebSocket handshake from that new session.
  // Restart is offered immediately, not held back for the give-up state. The
  // ready probe is deliberately read-only: it never touches the upstream and
  // never triggers a wake. So for the most common cause of a mid-session drop,
  // an app hibernating under the visitor, polling can only ever report 503
  // until the budget runs out. Reloading is the thing that actually wakes it,
  // and making someone wait a minute to be offered it would be theatre.
  function waiting() {
    render(
      "waiting",
      "Connection interrupted",
      "We are checking whether the app is available again. Start in a new tab to keep these results available.",
      "Restart now"
    );
  }

  function busy() {
    render(
      "busy",
      "App is busy",
      "Your session has not started yet. This page will retry automatically.",
      null
    );
  }

  function retryFirstConnection() {
    if (Date.now() >= capacityDeadline()) {
      gaveUp();
      return;
    }
    stopPolling();
    timer = window.setTimeout(guard(function () {
      if (showing && !everConnected) {
        window.location.reload();
      }
    }), retryMs + Math.floor(Math.random() * 500));
  }

  // A recovered service is offered, never confused with a recovered session.
  // The safest continuation opens a new tab and turns this one into an offline
  // snapshot. Viewers can also inspect the snapshot first or explicitly choose
  // to replace it by restarting in this tab.
  function recovered() {
    cancelProbe();
    stopPolling();
    render(
      "ready",
      "Your dashboard is ready to reopen",
      "These results are still here, but no longer update. Use a new tab to keep these results available.",
      null
    );
  }

  function gone() {
    cancelProbe();
    stopPolling();
    render(
      "error",
      "This app is no longer available",
      "It is not on this server any more. It may have been deleted or renamed.",
      null
    );
  }

  function gaveUp() {
    cancelProbe();
    stopPolling();
    if (!everConnected) {
      clearCapacityDeadline();
      render(
        "error",
        "Still at capacity",
        "Your session did not start within 60 seconds. The app may still be busy.",
        "Try again"
      );
      return;
    }
    render(
      "unavailable",
      window.navigator.onLine === false ? "You’re still offline" : "We couldn’t reconnect yet",
      "Your previous results are still available. Keep them open, or try connecting again.",
      "Start in this tab"
    );
  }

  var poll = guard(function () {
    timer = null;
    if (!showing || currentState === "snapshot" || currentState === "restoring") {
      return;
    }
    var serial = incidentSerial;
    if (currentState === "recovering" && window.navigator.onLine === false) {
      recoveryNotice();
      schedule();
      return;
    }
    cancelProbe();
    var requestSerial = probeSerial;
    probeAbort = typeof window.AbortController === "function" ? new window.AbortController() : null;
    var options = { cache: "no-store", credentials: "same-origin", redirect: "error" };
    if (probeAbort) options.signal = probeAbort.signal;
    probeTimer = window.setTimeout(guard(function () {
      if (requestSerial !== probeSerial || serial !== incidentSerial || !showing) return;
      cancelProbe();
      schedule();
    }), 10000);
    window
      .fetch(readyURL, options)
      .then(function (res) {
        if (!showing || serial !== incidentSerial || requestSerial !== probeSerial || currentState === "snapshot") {
          return;
        }
        if (currentState === "recovering" && !canRestore()) waiting();
        if (currentState === "recovering" && (res.status === 200 ||
            (res.status === 503 && res.headers && res.headers.get("X-Shinyhub-Reject") === "app-not-ready"))) {
          // Validate the probe body too: an upstream sign-in page is not proof
          // that restoring the dashboard is safe. A cold app needs navigation
          // to wake it; polling alone cannot make its ready flag become true.
          return res.json().then(function (body) {
            if (!showing || serial !== incidentSerial || requestSerial !== probeSerial || currentState === "snapshot") return;
            if (!body || body.ready !== (res.status === 200)) throw new Error("Invalid readiness response");
            if (currentState === "recovering" && canRestore()) { queueRestore(); return; }
            waiting();
            if (res.status === 200) recovered();
            else schedule();
          });
        }
        if (res.status === 401 || res.status === 403) {
          cancelProbe();
          stopPolling();
          render("access", res.status === 401 ? "Sign in to reconnect" : "Access to this app has changed",
            "Open the app in a new tab to check your access. These results will stay here.", null);
          show(ui.openLink, true);
          ui.openLink.focus();
          return;
        }
        if (res.status === 200) {
          if (everConnected) recovered();
          else retryFirstConnection();
          return;
        }
        // A 404 is the ready probe's deliberate "no such app on this server",
        // kept distinct from 503 so a deleted app is never reported as a slow
        // restart. Treating them alike would leave a visitor waiting out the
        // full budget for something that is never coming back.
        if (res.status === 404) {
          gone();
          return;
        }
        schedule();
      })
      .catch(
        guard(function () {
          // A failed fetch is indistinguishable from a down server from here,
          // so it counts as a miss rather than as an error state.
          if (showing && serial === incidentSerial && requestSerial === probeSerial && currentState !== "snapshot") {
            schedule();
          }
        })
      ).finally(function () {
        if (requestSerial !== probeSerial) return;
        if (probeTimer !== null) window.clearTimeout(probeTimer);
        probeTimer = null;
        probeAbort = null;
      });
  });

  function schedule() {
    polls += 1;
    if (polls >= maxPolls || (!everConnected && Date.now() >= capacityDeadline())) {
      gaveUp();
      return;
    }
    stopPolling();
    timer = window.setTimeout(poll, pollMs);
  }

  var onLost = guard(function () {
    if (showing) {
      return;
    }
    showing = true;
    incidentSerial++;
    cancelProbe();
    incidentConnectionID = endedConnectionID;
    polls = 0;
    previousFocus = document.activeElement;
    if (canRestore()) {
      currentState = "recovering";
      recoveryStarted = Date.now();
      quietDisconnectMarker();
      recoveryDeadline = window.setTimeout(guard(function () {
        if (currentState !== "recovering") return;
        incidentSerial++;
        gaveUp();
      }), maxPolls * pollMs);
      recoveryTimer = window.setTimeout(guard(function () {
        recoveryTimer = null;
        if (currentState !== "recovering") return;
        if (canRestore()) recoveryNotice();
        else waiting();
      }), 1200);
    } else if (everConnected) waiting();
    else busy();
    poll();
  });

  // Python Shiny can leave a "reloading" marker indefinitely when its server
  // is restarted. Allow normal page navigation, then explain a stalled reload
  // without automatically replacing the visitor's last visible results.
  function checkMarker(node) {
    if (!node || node.nodeType !== 1 || node.id !== SHINY_OVERLAY_ID) return;
    if (isShinyOverlay(node)) {
      onLost();
      if (currentState === "recovering") quietDisconnectMarker();
    } else if (reloadTimer === null) {
      reloadTimer = window.setTimeout(guard(function () {
        reloadTimer = null;
        if (document.getElementById(SHINY_OVERLAY_ID)) onLost();
      }), 3000);
    }
  }

  var onBack = guard(function () {
    if (reloadTimer !== null) {
      window.clearTimeout(reloadTimer);
      reloadTimer = null;
    }
    if (showing) {
      teardown();
    }
  });

  window.addEventListener("online", guard(function () {
    if (!showing || currentState !== "recovering") return;
    // Invalidate any pre-offline response before checking again.
    incidentSerial++;
    cancelProbe();
    if (restoreTimer !== null) window.clearTimeout(restoreTimer);
    restoreTimer = null;
    recoveryNotice();
    stopPolling();
    poll();
  }));
  window.addEventListener("offline", guard(function () {
    if (!showing || currentState !== "recovering") return;
    incidentSerial++;
    cancelProbe();
    if (restoreTimer !== null) window.clearTimeout(restoreTimer);
    restoreTimer = null;
    recoveryNotice();
    stopPolling();
    schedule();
  }));

  window.addEventListener(SESSION_OWNER_EVENT, guard(function (event) {
    if (!ui || currentState !== "snapshot") {
      return;
    }
    var owner = event && event.detail ? event.detail.owner : "";
    if (owner === "overlay") {
      ui.root.style.display = "block";
      return;
    }
    if (owner === "switcher" && publishSnapshot(false)) {
      ui.root.style.display = "none";
    }
  }));

  var observer = new MutationObserver(
    guard(function (records) {
      for (var i = 0; i < records.length; i++) {
        var rec = records[i];
        for (var a = 0; a < rec.addedNodes.length; a++) {
          checkMarker(rec.addedNodes[a]);
        }
        for (var r = 0; r < rec.removedNodes.length; r++) {
          var node = rec.removedNodes[r];
          if (node && node.nodeType === 1 && node.id === SHINY_OVERLAY_ID) {
            // Shiny can replace its marker (remove the old one, add a fresh
            // one) within a single synchronous batch instead of just
            // removing it. All mutations in the batch have already landed in
            // the live document by the time this callback runs, so a live
            // re-check tells a genuine "gone for good" removal apart from a
            // same-batch replacement, the same way checkMarker re-checks
            // document.getElementById for an add.
            if (!document.getElementById(SHINY_OVERLAY_ID)) {
              onBack();
            }
          }
        }
      }
    })
  );

  guard(function () {
    observer.observe(document.body, { childList: true });
    // Shiny raises its overlay on a socket close, which cannot have happened
    // before this script runs. The initial check exists for the reverse case:
    // a cached or restored page that already carries the marker.
    checkMarker(document.getElementById(SHINY_OVERLAY_ID));
  })();
})();
