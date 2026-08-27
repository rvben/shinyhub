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
 *   1. It never patches a global or calls a framework internal. The only
 *      contract it depends on is Shiny appending a #shiny-disconnected-overlay
 *      div to <body> on disconnect and removing it on reconnect, which is a
 *      public, user-styleable DOM contract shared by R and Python Shiny. After
 *      an explicit viewer choice it may hide that marker so the retained page
 *      can be inspected as a clearly labelled offline snapshot.
 *   2. Every entry point is wrapped. A throw here must never escape into a page
 *      whose app is already in trouble.
 *   3. An app that never emits that div never sees this code do anything, so a
 *      non-Shiny app (Streamlit, Dash, a plain HTML page) degrades to exactly
 *      today's behaviour rather than to a broken one.
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
  var SNAPSHOT_ID = "shinyhub-status-snapshot";
  var RELOAD_ID = "shinyhub-status-reload";
  var RESTART_ID = "shinyhub-status-restart";

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

  var polls = 0;
  var timer = null;
  var showing = false;
  var currentState = null;
  var previousFocus = null;

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
  // session reload); that is not a fault and must not raise an overlay.
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
        "#" + OWN_ID + " a, #" + OWN_ID + " button { transition: background-color 160ms ease-out, border-color 160ms ease-out, color 160ms ease-out, box-shadow 160ms ease-out; }",
        "#" + OWN_ID + " a:hover, #" + OWN_ID + " button:hover { filter: brightness(1.08); }",
        "#" + OWN_ID + " a:focus-visible, #" + OWN_ID + " button:focus-visible { outline: 3px solid #38BDF8; outline-offset: 3px; }",
        "#" + OWN_ID + " ::selection { background: #38BDF8; color: #030510; }",
        "@media (max-width: 520px) { #" + OWN_ID + " { padding: 16px !important; align-items: flex-end !important; } #" + OWN_ID + " > div { padding: 22px 18px !important; } #" + OWN_ID + " [data-shinyhub-actions] { flex-direction: column; } #" + OWN_ID + " [data-shinyhub-actions] > * { width: 100%; } #" + OWN_ID + ".is-snapshot { inset: auto 8px 8px 8px !important; padding: 0 !important; } #" + OWN_ID + ".is-snapshot > div { padding: 16px !important; } }",
        "@media (prefers-reduced-motion: reduce) { #" + OWN_ID + " * { transition: none !important; } #shinyhub-status-spinner { animation: none !important; } }"
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
        background: "rgba(3,5,16,0.90)",
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
      background: "#4ADE80"
    });
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
      fontSize: "0.875rem",
      margin: "6px 0 0",
      lineHeight: "1.55",
      maxWidth: "64ch"
    });
    msg.id = MESSAGE_ID;

    var actions = el("div", {
      display: "flex",
      flexWrap: "wrap",
      alignItems: "center",
      gap: "8px",
      marginTop: "24px"
    });
    actions.setAttribute("data-shinyhub-actions", "");

    var openLink = el("a", actionStyles("primary"), "Open new session");
    openLink.id = OPEN_ID;
    openLink.href = window.location.href;
    openLink.target = "_blank";
    openLink.rel = "noopener";
    openLink.setAttribute("aria-label", "Open a new app session in a new tab");

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

    var snapshotButton = el(
      "button",
      actionStyles("secondary"),
      "View previous results"
    );
    snapshotButton.id = SNAPSHOT_ID;
    snapshotButton.type = "button";
    snapshotButton.addEventListener("click", guard(enterSnapshot));

    var restartButton = el(
      "button",
      actionStyles("quiet"),
      "Restart in this tab"
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

    signal.appendChild(spinner);
    signal.appendChild(stateDot);
    copy.appendChild(title);
    copy.appendChild(msg);
    header.appendChild(signal);
    header.appendChild(copy);
    actions.appendChild(openLink);
    actions.appendChild(reloadButton);
    actions.appendChild(snapshotButton);
    actions.appendChild(restartButton);
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
        if (event.key === "Escape" && currentState === "ready") {
          event.preventDefault();
          enterSnapshot();
          return;
        }
        if (event.key !== "Tab") {
          return;
        }
        var controls = [openLink, reloadButton, snapshotButton, restartButton];
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

    return {
      root: root,
      box: box,
      spinner: spinner,
      stateDot: stateDot,
      title: title,
      msg: msg,
      openLink: openLink,
      reloadButton: reloadButton,
      snapshotButton: snapshotButton,
      restartButton: restartButton
    };
  }

  var ui = null;

  function show(control, visible) {
    control.style.display = visible ? "inline-flex" : "none";
  }

  function render(state, titleText, msgText, reloadText) {
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
    ui.root.style.background = "rgba(3,5,16,0.90)";
    ui.root.style.pointerEvents = "auto";
    ui.root.setAttribute("role", "dialog");
    ui.root.setAttribute("aria-modal", "true");
    ui.box.style.maxWidth = "520px";
    ui.box.style.margin = "0";
    ui.box.style.padding = "28px";
    ui.box.style.pointerEvents = "auto";
    ui.title.textContent = titleText;
    ui.title.style.color = "#E8EEFF";
    ui.msg.textContent = msgText;
    ui.spinner.style.display = state === "waiting" ? "block" : "none";
    ui.stateDot.style.display = state === "waiting" ? "none" : "block";
    ui.stateDot.style.background = state === "error" ? "#F87171" : "#4ADE80";
    show(ui.openLink, state === "ready");
    show(ui.snapshotButton, state === "ready");
    show(ui.restartButton, state === "ready");
    show(ui.reloadButton, !!reloadText);
    if (reloadText) {
      ui.reloadButton.textContent = reloadText;
    }
    if (!ui.root.parentNode) {
      document.body.appendChild(ui.root);
    }
    if (state === "ready") {
      ui.openLink.focus();
    } else if (reloadText) {
      ui.reloadButton.focus();
    } else {
      ui.root.focus();
    }
  }

  function enterSnapshot() {
    if (!ui || currentState !== "ready") {
      return;
    }
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
    ui.box.style.maxWidth = "720px";
    ui.box.style.margin = "0 auto";
    ui.box.style.padding = "18px";
    ui.box.style.pointerEvents = "auto";
    ui.title.textContent = "Previous results";
    ui.msg.textContent =
      "This snapshot may be out of date. Its controls no longer update; open a new session to continue.";
    ui.spinner.style.display = "none";
    ui.stateDot.style.display = "block";
    ui.stateDot.style.background = "#FBBF24";
    show(ui.openLink, true);
    show(ui.reloadButton, false);
    show(ui.snapshotButton, false);
    show(ui.restartButton, true);
    ui.root.focus();
  }

  function stopPolling() {
    if (timer !== null) {
      window.clearTimeout(timer);
      timer = null;
    }
  }

  function teardown() {
    stopPolling();
    showing = false;
    polls = 0;
    currentState = null;
    if (ui && ui.root.parentNode) {
      ui.root.parentNode.removeChild(ui.root);
    }
    if (
      previousFocus &&
      previousFocus !== document.body &&
      document.documentElement.contains(previousFocus) &&
      typeof previousFocus.focus === "function"
    ) {
      previousFocus.focus();
    }
    previousFocus = null;
  }

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
      "We are checking whether the app is available again. Restarting now begins a new session.",
      "Restart now"
    );
  }

  // A recovered service is offered, never confused with a recovered session.
  // The safest continuation opens a new tab and turns this one into an offline
  // snapshot. Viewers can also inspect the snapshot first or explicitly choose
  // to replace it by restarting in this tab.
  function recovered() {
    stopPolling();
    render(
      "ready",
      "The app is available again",
      "Your previous session is still disconnected. Open a new session to continue, or keep these results for reference.",
      null
    );
  }

  function gone() {
    stopPolling();
    render(
      "error",
      "This app is no longer available",
      "It is not on this server any more. It may have been deleted or renamed.",
      null
    );
  }

  function gaveUp() {
    stopPolling();
    render(
      "error",
      "The app did not come back",
      "Gave up after " +
        Math.round((maxPolls * pollMs) / 1000) +
        " seconds. It may have failed to restart.",
      "Try again"
    );
  }

  var poll = guard(function () {
    timer = null;
    if (!showing) {
      return;
    }
    window
      .fetch(readyURL, { cache: "no-store", credentials: "same-origin" })
      .then(function (res) {
        if (!showing) {
          return;
        }
        if (res.status === 200) {
          recovered();
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
          if (showing) {
            schedule();
          }
        })
      );
  });

  function schedule() {
    polls += 1;
    if (polls >= maxPolls) {
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
    polls = 0;
    previousFocus = document.activeElement;
    waiting();
    poll();
  });

  var onBack = guard(function () {
    if (showing) {
      teardown();
    }
  });

  var observer = new MutationObserver(
    guard(function (records) {
      for (var i = 0; i < records.length; i++) {
        var rec = records[i];
        for (var a = 0; a < rec.addedNodes.length; a++) {
          if (isShinyOverlay(rec.addedNodes[a])) {
            onLost();
          }
        }
        for (var r = 0; r < rec.removedNodes.length; r++) {
          var node = rec.removedNodes[r];
          if (node && node.nodeType === 1 && node.id === SHINY_OVERLAY_ID) {
            onBack();
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
    if (isShinyOverlay(document.getElementById(SHINY_OVERLAY_ID))) {
      onLost();
    }
  })();
})();
