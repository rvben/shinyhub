/*
 * App switcher: gives a visitor who is inside an app a way back to the others.
 *
 * Opening an app is a one-way door today. The dashboard has a sidebar listing
 * every app the visitor can reach, but the moment they open one, that sidebar
 * is gone: the app owns the whole viewport and the only route to a second app
 * is the browser's back button or a URL they were never shown. This puts the
 * dashboard's own app list back on screen as a rail the app cannot see.
 *
 * The same properties that keep the status overlay from becoming a fleet-wide
 * outage apply here, and one more that is specific to chrome:
 *
 *   1. It observes, it does not patch. No global is replaced, no framework
 *      internal is called, nothing runs before the app's own bootstrap.
 *   2. Every entry point is wrapped. A throw here must never escape into the
 *      app's page.
 *   3. Everything it renders lives inside a shadow root, so the app's CSS
 *      cannot restyle the rail and the rail's CSS cannot reach the app. An
 *      injected stylesheet without that boundary would be a fleet-wide
 *      restyling of applications this server did not write.
 *   4. It occupies a fixed 26px strip and nothing else. An app whose own
 *      controls sit at the far left is the reason the rail is dismissible.
 *
 * Styles are installed through the CSSOM (a constructed stylesheet, or
 * insertRule into an empty one) rather than as a <style> block with text in
 * it. CSP's style-src governs markup; CSSOM writes are not markup. That keeps
 * the whole feature inside a single script-src hash and means an app with a
 * strict style-src still gets a correctly styled rail.
 *
 * Every value that varies per page rides on the script tag's data-* attributes
 * and never in this body. A CSP hash covers script text only, so keeping the
 * text byte-identical everywhere means one hash admits the switcher on every
 * app and every surface.
 */
(function () {
  "use strict";

  var TAG_ID = "shinyhub-app-nav";
  var DISMISS_KEY = "shinyhub-app-nav:dismissed";
  var FILTER_THRESHOLD = 8;

  var tag = document.currentScript || document.getElementById(TAG_ID);
  if (!tag) {
    return;
  }

  // A framed app is furniture inside someone else's page, not a destination.
  // Floating our rail over an embed would put ShinyHub's chrome in a layout it
  // knows nothing about. A cross-origin parent throws on access, which is
  // itself proof we are framed.
  try {
    if (window.top !== window.self) {
      return;
    }
  } catch (e) {
    return;
  }

  if (
    typeof window.fetch !== "function" ||
    !document.body ||
    typeof document.body.attachShadow !== "function"
  ) {
    return;
  }

  var navURL = tag.getAttribute("data-nav-url");
  if (!navURL) {
    return;
  }
  var currentSlug = tag.getAttribute("data-current-slug") || "";
  var homeURL = tag.getAttribute("data-home-url") || "/";

  function guard(fn) {
    return function () {
      try {
        return fn.apply(null, arguments);
      } catch (e) {
        // Deliberately swallowed. Navigation chrome failing is a missing
        // convenience; the same failure escaping into the page would be the
        // app breaking.
        return undefined;
      }
    };
  }

  // sessionStorage is unavailable in some privacy modes and throws on access
  // rather than returning null, so both sides are guarded. A tab that cannot
  // remember a dismissal simply shows the rail again, which is the safe way to
  // be wrong.
  function dismissed() {
    try {
      return window.sessionStorage.getItem(DISMISS_KEY) === "1";
    } catch (e) {
      return false;
    }
  }

  function rememberDismissal() {
    try {
      window.sessionStorage.setItem(DISMISS_KEY, "1");
    } catch (e) {
      /* not remembering is acceptable; showing it again is not a fault */
    }
  }

  if (dismissed()) {
    return;
  }

  /* ---------------------------------------------------------------------
   * Grouping. This is a deliberate copy of the rule in
   * internal/ui/static/views/project-groups.js: ungrouped apps first, then
   * named projects by display name with the project slug as a tiebreak so the
   * order is total; apps sorted by display name within each group.
   *
   * It is copied rather than imported because an injected inline script cannot
   * import an ES module, and it is copied into JAVASCRIPT rather than
   * reimplemented in Go on the server precisely so both orderings run the same
   * localeCompare. A Go port would order "apple" against "Banana" by byte and
   * silently disagree with the sidebar the visitor just came from.
   * internal/ui/jstests/app-nav.test.js pins the two against one fixture.
   * ------------------------------------------------------------------- */

  var UNGROUPED = "";

  function projectKeyOf(app) {
    var raw = app && app.project_slug ? String(app.project_slug) : "";
    return raw.trim();
  }

  function projectDisplayName(key, name) {
    if (key === UNGROUPED) {
      return "";
    }
    var n = (name || "").trim();
    return n || key;
  }

  function compareGroups(a, b) {
    var au = a.project === UNGROUPED;
    var bu = b.project === UNGROUPED;
    if (au !== bu) {
      return au ? -1 : 1;
    }
    var byName = String(a.name || "").localeCompare(String(b.name || ""));
    return byName !== 0
      ? byName
      : String(a.project).localeCompare(String(b.project));
  }

  function byDisplayName(a, b) {
    return String((a && (a.name || a.slug)) || "").localeCompare(
      String((b && (b.name || b.slug)) || "")
    );
  }

  function groupApps(apps) {
    var buckets = {};
    var order = [];
    var list = apps || [];
    for (var i = 0; i < list.length; i++) {
      var app = list[i];
      if (!app || !app.slug) {
        continue;
      }
      var key = projectKeyOf(app);
      // Prefixed so a project slug of "constructor" or "__proto__" cannot
      // collide with an Object.prototype member.
      var bucketKey = "p:" + key;
      if (!Object.prototype.hasOwnProperty.call(buckets, bucketKey)) {
        buckets[bucketKey] = [];
        order.push(key);
      }
      buckets[bucketKey].push(app);
    }
    var groups = [];
    for (var g = 0; g < order.length; g++) {
      var k = order[g];
      var members = buckets["p:" + k];
      var first = members[0] || {};
      groups.push({
        project: k,
        name: projectDisplayName(k, first.project_name),
        iconEmoji: k === UNGROUPED ? "" : first.project_icon_emoji || "",
        apps: members.slice().sort(byDisplayName)
      });
    }
    return groups.sort(compareGroups);
  }

  /* ---------------------------------------------------------------------
   * Chrome.
   * ------------------------------------------------------------------- */

  // The palette, radii and type below are ShinyHub's dark control-surface
  // tokens from DESIGN.md, declared once here as custom properties and
  // referenced by name from every rule.
  //
  // They are literals rather than references to the dashboard's stylesheet
  // because there is no stylesheet to reference: this runs inside an
  // application's document, in a closed shadow root, where neither the
  // dashboard's variables nor the app's own are in scope. Declaring them on
  // .root rather than :host also means an app that happens to define
  // --sh-anything cannot reach in and repaint our chrome.
  //
  // Sizes are px, never rem: rem resolves against the host document's root
  // font size, which the app owns, so an app setting html { font-size: 10px }
  // would silently shrink ShinyHub's chrome to two thirds.
  var CSS = [
    ":host { all: initial; }",
    ".root {" +
      "  --sh-deep: #030510;" +
      "  --sh-surface: #0E1426;" +
      "  --sh-raised: #141B32;" +
      "  --sh-hover: #1B2444;" +
      "  --sh-line: #1E2A4A;" +
      "  --sh-line-strong: #2B3A63;" +
      "  --sh-text: #E8EEFF;" +
      "  --sh-soft: #A8B4D4;" +
      "  --sh-muted: #6B7AA3;" +
      "  --sh-signal: #38BDF8;" +
      "  --sh-coral: #F87171;" +
      "  --sh-r-sm: 4px; --sh-r-md: 8px; --sh-r-lg: 14px; --sh-r-pill: 99px;" +
      "  position: fixed; inset: 0 auto 0 0; z-index: 2147483646;" +
      "  font-family: Manrope, -apple-system, BlinkMacSystemFont, system-ui, 'Segoe UI', sans-serif;" +
      "  font-size: 14px; line-height: 1.55; letter-spacing: -0.005em; color: var(--sh-text);" +
      "  pointer-events: none;" +
      "}",
    // The rail is the only thing that overlaps the app while closed, so it is
    // kept to a strip and fades up on approach rather than sitting at full
    // strength over someone's controls.
    ".rail {" +
      "  position: absolute; top: 50%; left: 0; transform: translateY(-50%);" +
      "  display: flex; flex-direction: column; align-items: center; gap: 4px;" +
      "  padding: 8px 4px; pointer-events: auto;" +
      "  background: var(--sh-surface);" +
      "  border: 1px solid var(--sh-line); border-left: 0;" +
      "  border-radius: 0 var(--sh-r-md) var(--sh-r-md) 0;" +
      "  opacity: 0.6; transition: opacity 140ms ease, border-color 140ms ease;" +
      "}",
    ".rail:hover, .rail:focus-within { opacity: 1; border-color: var(--sh-line-strong); }",
    // The rail sits above the panel, so leaving it visible once the panel is
    // open lays the strip across whichever rows happen to fall at mid-height.
    // The panel carries its own close control, so the rail has nothing left to
    // offer while it is open.
    ".root.open .rail { opacity: 0; pointer-events: none; }",
    ".railbtn {" +
      "  appearance: none; -webkit-appearance: none; background: transparent;" +
      "  border: 0; margin: 0; padding: 6px 4px; cursor: pointer; color: var(--sh-soft);" +
      "  border-radius: var(--sh-r-sm); display: block;" +
      "}",
    ".railbtn:hover { color: var(--sh-signal); background: var(--sh-hover); }",
    ".railbtn:focus-visible { outline: 2px solid var(--sh-signal); outline-offset: 1px; }",
    ".railbtn svg { display: block; width: 16px; height: 16px; }",
    ".rule { width: 12px; height: 1px; background: var(--sh-line-strong); }",
    ".close { color: var(--sh-muted); padding: 4px; }",
    ".close svg { width: 12px; height: 12px; }",
    ".close:hover { color: var(--sh-coral); background: var(--sh-raised); }",

    ".scrim {" +
      "  position: absolute; inset: 0; background: var(--sh-deep);" +
      "  opacity: 0; pointer-events: none; transition: opacity 160ms ease;" +
      "}",
    ".root.open .scrim { opacity: 0.66; pointer-events: auto; }",

    ".panel {" +
      "  position: absolute; top: 0; bottom: 0; left: 0; width: 292px; max-width: 86vw;" +
      "  display: flex; flex-direction: column; pointer-events: auto;" +
      "  background: var(--sh-surface); border-right: 1px solid var(--sh-line-strong);" +
      "  transform: translateX(-100%); transition: transform 180ms cubic-bezier(0.22, 1, 0.36, 1);" +
      "  visibility: hidden;" +
      "}",
    ".root.open .panel { transform: translateX(0); visibility: visible; }",
    // The panel takes focus itself while the list loads, so it needs a ring for
    // the seconds it holds it - otherwise a keyboard visitor is somewhere with
    // nothing on screen saying where. :focus-visible keeps it to the visitors
    // who arrived by keyboard; a mouse click never draws it.
    ".panel:focus { outline: none; }",
    ".panel:focus-visible { outline: none; box-shadow: inset 0 0 0 2px var(--sh-signal); }",

    ".head {" +
      "  display: flex; align-items: center; gap: 8px;" +
      "  padding: 16px 16px 8px; border-bottom: 1px solid var(--sh-line);" +
      "}",
    ".title {" +
      "  font-size: 12px; font-weight: 600; line-height: 1.35; letter-spacing: 0.02em;" +
      "  text-transform: uppercase; color: var(--sh-soft);" +
      "}",
    ".count {" +
      "  font-size: 12px; font-weight: 600; color: var(--sh-muted); background: var(--sh-raised);" +
      "  border-radius: var(--sh-r-pill); padding: 4px 10px; font-variant-numeric: tabular-nums;" +
      "}",
    ".spacer { flex: 1; }",
    ".headclose {" +
      "  appearance: none; -webkit-appearance: none; background: transparent;" +
      "  border: 0; margin: 0; padding: 4px; cursor: pointer; color: var(--sh-muted);" +
      "  border-radius: var(--sh-r-sm); display: block;" +
      "}",
    ".headclose:hover { color: var(--sh-text); background: var(--sh-raised); }",
    ".headclose:focus-visible { outline: 2px solid var(--sh-signal); outline-offset: 1px; }",
    ".headclose svg { display: block; width: 14px; height: 14px; }",
    ".filterwrap { padding: 8px 16px 4px; }",
    ".filter {" +
      "  width: 100%; box-sizing: border-box; appearance: none; -webkit-appearance: none;" +
      "  background: var(--sh-deep); border: 1px solid var(--sh-line);" +
      "  border-radius: var(--sh-r-md); padding: 9px 12px; color: var(--sh-text);" +
      "  font-family: inherit; font-size: 14px;" +
      "}",
    ".filter::placeholder { color: var(--sh-muted); }",
    ".filter:focus { outline: none; border-color: var(--sh-signal); }",

    ".list { flex: 1; overflow-y: auto; overscroll-behavior: contain; padding: 8px; }",
    ".grouphead {" +
      "  display: flex; align-items: center; gap: 8px; padding: 16px 8px 4px;" +
      "  font-size: 12px; font-weight: 600; line-height: 1.35; letter-spacing: 0.02em;" +
      "  text-transform: uppercase; color: var(--sh-muted);" +
      "}",
    ".item {" +
      "  display: flex; align-items: center; gap: 8px; padding: 8px;" +
      "  border-radius: var(--sh-r-md); color: var(--sh-soft); text-decoration: none;" +
      "  border: 1px solid transparent;" +
      "}",
    ".item:hover { background: var(--sh-hover); color: var(--sh-text); }",
    ".item:focus-visible { outline: 2px solid var(--sh-signal); outline-offset: -1px; }",
    ".item.current { background: var(--sh-raised); border-color: var(--sh-signal); color: var(--sh-text); }",
    // Dimming is how "you cannot open this" reads at a glance, but dimming
    // alone is a colour-only signal, so every dimmed row also carries the word
    // Unavailable in its own text.
    //
    // A dimmed row is not a link (see itemFor), so it must not answer the
    // pointer like one either: no cursor, no hover lift. Leaving those on
    // invites the click the row cannot honour.
    ".item.down { opacity: 0.55; cursor: default; }",
    ".item.down:hover { background: transparent; color: var(--sh-soft); }",
    ".icon {" +
      "  flex: none; width: 24px; height: 24px; border-radius: var(--sh-r-sm); display: flex;" +
      "  align-items: center; justify-content: center; font-size: 12px; font-weight: 600;" +
      "  background: var(--sh-raised); color: var(--sh-soft);" +
      "}",
    ".item.current .icon { background: var(--sh-hover); color: var(--sh-signal); }",
    ".label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }",
    ".flag, .here {" +
      "  flex: none; font-size: 12px; font-weight: 600; letter-spacing: 0.02em;" +
      "  text-transform: uppercase;" +
      "}",
    ".flag { color: var(--sh-muted); }",
    ".here { color: var(--sh-signal); }",

    ".note { padding: 16px 8px; color: var(--sh-muted); }",
    ".more { padding: 8px 8px 16px; color: var(--sh-muted); font-size: 12px; line-height: 1.35; }",
    ".retry {" +
      "  margin-top: 16px; appearance: none; -webkit-appearance: none; font-family: inherit;" +
      "  font-size: 12px; font-weight: 600; letter-spacing: 0.02em;" +
      "  background: var(--sh-signal); color: var(--sh-deep); border: 0;" +
      "  border-radius: var(--sh-r-md); padding: 8px 18px; cursor: pointer; display: block;" +
      "}",
    ".retry:focus-visible { outline: 2px solid var(--sh-signal); outline-offset: 2px; }",

    ".foot {" +
      "  border-top: 1px solid var(--sh-line); padding: 12px 16px;" +
      "  display: flex; align-items: center; gap: 8px;" +
      "}",
    ".home { color: var(--sh-signal); text-decoration: none; }",
    ".home:hover { text-decoration: underline; }",
    ".home:focus-visible { outline: 2px solid var(--sh-signal); outline-offset: 2px; }",
    ".who {" +
      "  margin-left: auto; font-size: 12px; color: var(--sh-muted); max-width: 45%;" +
      "  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;" +
      "}",

    "@media (prefers-reduced-motion: reduce) {" +
      "  .panel, .scrim, .rail { transition: none; }" +
      "}"
  ];

  // Two paths, both CSSOM. Constructed stylesheets are the modern one; an
  // empty <style> filled by insertRule covers browsers without them. Neither
  // puts style text into markup, so neither needs a style-src allowance.
  function installStyles(shadow) {
    try {
      var sheet = new CSSStyleSheet();
      sheet.replaceSync(CSS.join("\n"));
      shadow.adoptedStyleSheets = [sheet];
      return;
    } catch (e) {
      /* fall through */
    }
    try {
      var el = document.createElement("style");
      shadow.appendChild(el);
      for (var i = 0; i < CSS.length; i++) {
        try {
          el.sheet.insertRule(CSS[i], el.sheet.cssRules.length);
        } catch (inner) {
          /* one unsupported rule must not cost the rest of the stylesheet */
        }
      }
    } catch (e2) {
      /* unstyled chrome is still usable chrome */
    }
  }

  function svg(paths, size) {
    var NS = "http://www.w3.org/2000/svg";
    var s = document.createElementNS(NS, "svg");
    s.setAttribute("viewBox", "0 0 " + size + " " + size);
    s.setAttribute("fill", "none");
    s.setAttribute("stroke", "currentColor");
    s.setAttribute("stroke-width", "2");
    s.setAttribute("stroke-linecap", "round");
    s.setAttribute("stroke-linejoin", "round");
    s.setAttribute("aria-hidden", "true");
    for (var i = 0; i < paths.length; i++) {
      var p = document.createElementNS(NS, "path");
      p.setAttribute("d", paths[i]);
      s.appendChild(p);
    }
    return s;
  }

  function div(cls, text) {
    var n = document.createElement("div");
    if (cls) {
      n.className = cls;
    }
    if (text !== undefined) {
      n.textContent = text;
    }
    return n;
  }

  // Monogram fallback matches the dashboard's own: the emoji when the app has
  // one, otherwise the first character of the display name.
  function iconFor(app) {
    var node = div("icon");
    var emoji = (app.icon_emoji || "").trim();
    if (emoji) {
      node.textContent = emoji;
    } else {
      var name = String(app.name || app.slug || "?");
      node.textContent = name.charAt(0).toUpperCase();
    }
    node.setAttribute("aria-hidden", "true");
    return node;
  }

  var host = document.createElement("div");
  host.id = TAG_ID + "-host";
  var shadow = host.attachShadow({ mode: "closed" });
  installStyles(shadow);

  var root = div("root");
  var scrim = div("scrim");
  var panel = document.createElement("nav");
  panel.className = "panel";
  panel.setAttribute("aria-label", "ShinyHub apps");
  panel.setAttribute("aria-hidden", "true");
  // Focus has to land somewhere the moment the panel opens, and on a first open
  // the list is still loading, so there is no control yet to land on. The panel
  // holds focus itself until one arrives. -1 keeps it out of the Tab order, so
  // it is a landing place and never a stop.
  panel.setAttribute("tabindex", "-1");

  var rail = div("rail");
  var openBtn = document.createElement("button");
  openBtn.type = "button";
  openBtn.className = "railbtn";
  openBtn.setAttribute("aria-label", "Show all apps");
  openBtn.setAttribute("aria-expanded", "false");
  openBtn.title = "All apps";
  openBtn.appendChild(
    svg(
      [
        "M3 3h6v6H3z",
        "M11 3h6v6h-6z",
        "M3 11h6v6H3z",
        "M11 11h6v6h-6z"
      ],
      20
    )
  );

  var closeBtn = document.createElement("button");
  closeBtn.type = "button";
  closeBtn.className = "railbtn close";
  closeBtn.setAttribute("aria-label", "Hide the app switcher for this tab");
  // A dismissal is remembered in sessionStorage, so it outlives a reload and
  // ends when the tab does. Wording it as "until this tab is reloaded" would
  // promise a way back that reloading does not deliver.
  closeBtn.title = "Hide the switcher in this tab";
  closeBtn.appendChild(svg(["M4 4l12 12", "M16 4L4 16"], 20));

  rail.appendChild(openBtn);
  rail.appendChild(div("rule"));
  rail.appendChild(closeBtn);

  var head = div("head");
  var title = div("title", "Apps");
  var countBadge = div("count", "");
  countBadge.setAttribute("aria-hidden", "true");
  var headClose = document.createElement("button");
  headClose.type = "button";
  headClose.className = "headclose";
  headClose.setAttribute("aria-label", "Close the app list");
  headClose.title = "Close";
  headClose.appendChild(svg(["M4 4l12 12", "M16 4L4 16"], 20));

  head.appendChild(title);
  head.appendChild(countBadge);
  head.appendChild(div("spacer"));
  head.appendChild(headClose);

  var filterWrap = div("filterwrap");
  filterWrap.hidden = true;
  var filter = document.createElement("input");
  filter.className = "filter";
  filter.type = "search";
  filter.placeholder = "Filter apps";
  filter.setAttribute("aria-label", "Filter apps");
  filterWrap.appendChild(filter);

  var list = div("list");
  var foot = div("foot");
  var homeLink = document.createElement("a");
  homeLink.className = "home";
  homeLink.href = homeURL;
  homeLink.textContent = "All apps";
  var who = div("who", "");
  foot.appendChild(homeLink);
  foot.appendChild(who);

  panel.appendChild(head);
  panel.appendChild(filterWrap);
  panel.appendChild(list);
  panel.appendChild(foot);
  root.appendChild(scrim);
  root.appendChild(panel);
  root.appendChild(rail);
  shadow.appendChild(root);
  document.body.appendChild(host);

  /* ---------------------------------------------------------------------
   * State + rendering.
   * ------------------------------------------------------------------- */

  var open = false;
  var loaded = false;
  var loading = false;
  var payload = null;
  var lastFocus = null;

  function clear(node) {
    while (node.firstChild) {
      node.removeChild(node.firstChild);
    }
  }

  function renderNote(text, withRetry) {
    clear(list);
    var note = div("note", text);
    if (withRetry) {
      var btn = document.createElement("button");
      btn.type = "button";
      btn.className = "retry";
      btn.textContent = "Try again";
      btn.addEventListener("click", guard(function () {
        loaded = false;
        load();
      }));
      note.appendChild(btn);
    }
    list.appendChild(note);
  }

  function matches(app, needle) {
    if (!needle) {
      return true;
    }
    var hay = (String(app.name || "") + " " + String(app.slug || "")).toLowerCase();
    return hay.indexOf(needle) !== -1;
  }

  // A clipped list must say it is clipped. Rendering the first page of a large
  // fleet with nothing to mark the edge tells the visitor their app is gone,
  // and "no apps match that filter" is the same claim in a worse place: the
  // filter only ever sees the apps that arrived.
  function appendTruncationNote(needle) {
    if (!payload || !payload.truncated) {
      return;
    }
    var n = (payload.apps || []).length;
    list.appendChild(
      div(
        "more",
        needle
          ? "Only the first " + n + " apps are searchable here. The dashboard lists them all."
          : "Showing the first " + n + " apps. The dashboard lists them all."
      )
    );
  }

  function renderList() {
    clear(list);
    if (!payload) {
      return;
    }
    var needle = filter.value.trim().toLowerCase();
    var visible = [];
    for (var i = 0; i < (payload.apps || []).length; i++) {
      if (matches(payload.apps[i], needle)) {
        visible.push(payload.apps[i]);
      }
    }
    if (!visible.length) {
      renderNote(
        needle ? "No apps match that filter." : "No apps are available to you yet.",
        false
      );
      appendTruncationNote(needle);
      return;
    }

    var groups = groupApps(visible);
    for (var g = 0; g < groups.length; g++) {
      var group = groups[g];
      // A single ungrouped bucket needs no heading; once projects exist, the
      // loose bucket is named so its place in the tree is unambiguous. Same
      // rule the dashboard sidebar applies.
      if (group.project || groups.length > 1) {
        var label = group.project ? group.name : "Other apps";
        var gh = div("grouphead");
        if (group.iconEmoji) {
          var ge = document.createElement("span");
          ge.textContent = group.iconEmoji;
          ge.setAttribute("aria-hidden", "true");
          gh.appendChild(ge);
        }
        gh.appendChild(document.createTextNode(label));
        list.appendChild(gh);
      }
      for (var a = 0; a < group.apps.length; a++) {
        list.appendChild(itemFor(group.apps[a]));
      }
    }
    appendTruncationNote(needle);
  }

  function itemFor(app) {
    var here = app.slug === currentSlug;
    var openable = app.openable !== false;

    // An app that cannot be opened is not a link. A row that reads Unavailable
    // and still navigates makes a promise the switcher cannot keep, and the
    // visitor pays for it twice: they land on an error page, and they lose the
    // working app they were in to get there. The dashboard's launchpad has
    // always dropped the anchor for these (views/launchpad.js), so a row that
    // is dead in one place is dead in both.
    var link = document.createElement(openable ? "a" : "div");
    link.className =
      "item" + (here ? " current" : "") + (openable ? "" : " down");
    if (openable) {
      link.href = "/app/" + encodeURIComponent(app.slug) + "/";
    }
    link.setAttribute("data-slug", app.slug);
    if (here) {
      link.setAttribute("aria-current", "page");
    }

    var name = String(app.name || app.slug);
    link.appendChild(iconFor(app));
    var label = div("label", name);
    label.title = name;
    link.appendChild(label);

    if (here) {
      // One interactive stop, so the status belongs in the accessible name
      // rather than trailing after it as a loose fragment.
      link.appendChild(div("here", "Here"));
      link.setAttribute("aria-label", name + ", current app");
    } else if (!openable) {
      // No aria-label to match: this row is not interactive any more, so a
      // screen reader reads its text in order and Unavailable is already in
      // it. aria-label on an element with no role is honoured inconsistently,
      // and here it would be overriding a name that is already correct.
      link.appendChild(div("flag", "Unavailable"));
    }
    return link;
  }

  function render() {
    if (!payload) {
      return;
    }
    var total = (payload.apps || []).length;
    countBadge.textContent = String(total) + (payload.truncated ? "+" : "");
    filterWrap.hidden = total <= FILTER_THRESHOLD;
    who.textContent = payload.username ? "Signed in as " + payload.username : "";
    renderList();
    // The list has arrived. If focus is still parked on the panel waiting for
    // something worth landing on, hand it over now. The open check is what
    // stops a slow load from yanking focus back into a panel the visitor gave
    // up on and closed.
    if (open && pendingFocus) {
      pendingFocus = !placeFocus();
    }
  }

  var load = guard(function () {
    if (loaded || loading) {
      return;
    }
    loading = true;
    renderNote("Loading apps…", false);
    window
      .fetch(navURL, { cache: "no-store", credentials: "same-origin" })
      .then(function (res) {
        if (!res.ok) {
          throw new Error("nav " + res.status);
        }
        return res.json();
      })
      .then(
        guard(function (body) {
          loading = false;
          loaded = true;
          payload = body && typeof body === "object" ? body : { apps: [] };
          if (!payload.apps || typeof payload.apps.length !== "number") {
            payload.apps = [];
          }
          render();
        })
      )
      .catch(
        guard(function () {
          loading = false;
          loaded = false;
          renderNote("Could not load the app list.", true);
        })
      );
  });

  function focusable() {
    var out = [];
    var candidates = panel.querySelectorAll("a[href], button, input");
    for (var i = 0; i < candidates.length; i++) {
      var node = candidates[i];
      if (!node.hasAttribute("disabled") && node.offsetParent !== null) {
        out.push(node);
      }
    }
    return out;
  }

  // Where focus goes when the panel opens. The list is not there yet on a first
  // open - the fetch is still in flight - so this runs twice: the panel itself
  // holds focus while the list loads, and the first real control takes over as
  // soon as one exists. Leaving focus on the rail button instead would strand a
  // keyboard visitor on a control the open panel has just hidden.
  //
  // The order below is deliberate and is not DOM order. The filter is what a
  // visitor with a long list opened the panel to use; the first app is what a
  // visitor with a short one opened it to pick. The close button and the
  // dashboard link are focusable too and both sit in the panel, but landing on
  // either turns the visitor's first Enter into "leave" - the opposite of what
  // opening the panel asked for.
  var pendingFocus = false;

  function placeFocus() {
    var wanted = [];
    if (!filterWrap.hidden) {
      wanted.push(filter);
    }
    wanted.push(list.querySelector("a.item"));

    for (var i = 0; i < wanted.length; i++) {
      if (wanted[i] && wanted[i].offsetParent !== null) {
        wanted[i].focus();
        return true;
      }
    }
    // Loading, empty or failed: nothing here is worth landing on yet, so the
    // panel holds focus itself until something is.
    panel.focus();
    return false;
  }

  var setOpen = guard(function (next) {
    if (open === next) {
      return;
    }
    open = next;
    root.classList.toggle("open", open);
    panel.setAttribute("aria-hidden", open ? "false" : "true");
    openBtn.setAttribute("aria-expanded", open ? "true" : "false");
    if (open) {
      // Focus inside a shadow root is reported by the root, not by the
      // document: document.activeElement retargets to the host, which here is
      // a plain div that cannot take focus back. Reading the root first is
      // what makes the rail button restorable, and it is the only way to see
      // it at all - a keyboard visitor opens the panel from that button, so
      // the document's answer is the host every single time. Falling through
      // to the document covers the visitor who was in the app when they
      // clicked, whose focus is genuinely outside the root.
      lastFocus = shadow.activeElement || document.activeElement;
      load();
      pendingFocus = !placeFocus();
    } else if (lastFocus && typeof lastFocus.focus === "function") {
      // Returning focus to the app is not politeness, it is the difference
      // between a keyboard user resuming where they were and being dropped at
      // the top of a document they had already navigated into.
      lastFocus.focus();
      lastFocus = null;
    }
  });

  openBtn.addEventListener("click", guard(function () {
    setOpen(!open);
  }));

  closeBtn.addEventListener("click", guard(function () {
    setOpen(false);
    rememberDismissal();
    if (host.parentNode) {
      host.parentNode.removeChild(host);
    }
  }));

  headClose.addEventListener("click", guard(function () {
    setOpen(false);
  }));

  scrim.addEventListener("click", guard(function () {
    setOpen(false);
  }));

  filter.addEventListener("input", guard(function () {
    renderList();
  }));

  // Key handling is bound on the shadow root, not the document: an app that
  // listens for its own shortcuts on document must not see the visitor typing
  // into our filter box, and we must not see theirs.
  root.addEventListener("keydown", guard(function (ev) {
    if (!open) {
      return;
    }
    if (ev.key === "Escape") {
      ev.stopPropagation();
      setOpen(false);
      openBtn.focus();
      return;
    }
    if (ev.key !== "Tab") {
      return;
    }
    var items = focusable();
    if (!items.length) {
      return;
    }
    var firstItem = items[0];
    var lastItem = items[items.length - 1];
    var active = shadow.activeElement;
    if (ev.shiftKey && active === firstItem) {
      ev.preventDefault();
      lastItem.focus();
    } else if (!ev.shiftKey && active === lastItem) {
      ev.preventDefault();
      firstItem.focus();
    }
  }));
})();
