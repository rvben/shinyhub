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
 *   1. It observes, it does not patch. No global is replaced, no framework
 *      internal is called, nothing runs before the app's own bootstrap. The
 *      only contract it depends on is Shiny appending a #shiny-disconnected-
 *      overlay div to <body> on disconnect and removing it on reconnect, which
 *      is a public, user-styleable DOM contract shared by R and Python Shiny.
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
        background: "rgba(3,5,16,0.92)",
        color: "#E8EEFF",
        fontFamily:
          '-apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif'
      },
      undefined
    );
    root.id = OWN_ID;
    root.setAttribute("role", "status");
    root.setAttribute("aria-live", "polite");

    var box = el("div", {
      textAlign: "center",
      maxWidth: "420px",
      padding: "0 1rem"
    });
    var spinner = el("div", {
      width: "40px",
      height: "40px",
      border: "4px solid rgba(56,189,248,0.18)",
      borderTopColor: "#38BDF8",
      borderRadius: "50%",
      margin: "0 auto 1rem",
      animation: "shinyhub-spin 0.8s linear infinite"
    });
    var title = el("h1", {
      fontSize: "1.25rem",
      margin: "0",
      fontWeight: "600"
    });
    var msg = el("p", {
      color: "#6B7AA3",
      fontSize: "0.875rem",
      marginTop: "0.5rem",
      lineHeight: "1.4"
    });
    var button = el(
      "button",
      {
        marginTop: "1rem",
        padding: "0.5rem 1rem",
        fontSize: "0.875rem",
        background: "linear-gradient(135deg, #38BDF8, #2DD4BF)",
        color: "#030510",
        border: "0",
        borderRadius: "4px",
        cursor: "pointer",
        fontWeight: "600",
        display: "none"
      },
      "Reload"
    );
    button.addEventListener(
      "click",
      guard(function () {
        window.location.reload();
      })
    );

    // The keyframes cannot be expressed through the CSSOM style map, so they go
    // in a stylesheet built by insertRule, which is a CSSOM write and therefore
    // needs no style-src allowance either. A browser that refuses the rule
    // still gets a static ring, which is cosmetic.
    try {
      var sheet = document.createElement("style");
      document.head.appendChild(sheet);
      sheet.sheet.insertRule(
        "@keyframes shinyhub-spin { to { transform: rotate(360deg); } }",
        0
      );
    } catch (e) {
      spinner.style.animation = "";
    }

    box.appendChild(spinner);
    box.appendChild(title);
    box.appendChild(msg);
    box.appendChild(button);
    root.appendChild(box);
    return { root: root, spinner: spinner, title: title, msg: msg, button: button };
  }

  var ui = null;

  function render(state, titleText, msgText, buttonText) {
    if (!ui) {
      ui = build();
    }
    ui.title.textContent = titleText;
    ui.title.style.color = state === "error" ? "#F87171" : "#E8EEFF";
    ui.msg.textContent = msgText;
    ui.spinner.style.display = state === "waiting" ? "block" : "none";
    if (buttonText) {
      ui.button.textContent = buttonText;
      ui.button.style.display = "inline-block";
    } else {
      ui.button.style.display = "none";
    }
    if (!ui.root.parentNode) {
      document.body.appendChild(ui.root);
    }
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
    if (ui && ui.root.parentNode) {
      ui.root.parentNode.removeChild(ui.root);
    }
  }

  // Reload is offered immediately, not held back for the give-up state. The
  // ready probe is deliberately read-only: it never touches the upstream and
  // never triggers a wake. So for the most common cause of a mid-session drop,
  // an app hibernating under the visitor, polling can only ever report 503
  // until the budget runs out. Reloading is the thing that actually wakes it,
  // and making someone wait a minute to be offered it would be theatre.
  function waiting() {
    render(
      "waiting",
      "Lost connection to the app",
      "Checking whether it is coming back. You can reload now to restart it.",
      "Reload"
    );
  }

  // A recovered app is offered, not forced. The wait pages reload themselves
  // because there is nothing on screen to lose; here the visitor is mid-session
  // and the dead page still shows their last results, which a reload replaces
  // with a blank app. Deciding that for them is not ours to do.
  function recovered() {
    stopPolling();
    render(
      "ready",
      "The app is back online",
      "Reloading starts a new session and replaces what is on screen now.",
      "Reload"
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
      "Reload"
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
