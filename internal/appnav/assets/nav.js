/*
 * App switcher: gives a visitor who is inside an app a way back to the others.
 *
 * Opening an app is a one-way door today. The dashboard has a sidebar listing
 * every app the visitor can reach, but the moment they open one, that sidebar
 * is gone: the app owns the whole viewport and the only route to a second app
 * is the browser's back button or a URL they were never shown. This puts the
 * dashboard's own app list back on screen as a compact bar the app cannot see.
 *
 * The same properties that keep the status overlay from becoming a fleet-wide
 * outage apply here, and one more that is specific to chrome:
 *
 *   1. It observes, it does not patch. No global is replaced, no framework
 *      internal is called, nothing runs before the app's own bootstrap.
 *   2. Every entry point is wrapped. A throw here must never escape into the
 *      app's page.
 *   3. Everything it renders lives inside a shadow root, so the app's CSS
 *      cannot restyle the switcher and the switcher's CSS cannot reach the app. An
 *      injected stylesheet without that boundary would be a fleet-wide
 *      restyling of applications this server did not write.
 *   4. It starts at the top right, then lets the visitor snap it to another
 *      viewport edge or reduce it to a restore tab. Apps own their layouts;
 *      collision recovery therefore belongs in the control, not in a host
 *      page heuristic that guesses where an app put its important controls.
 *
 * Styles are installed through the CSSOM (a constructed stylesheet, or
 * insertRule into an empty one) rather than as a <style> block with text in
 * it. CSP's style-src governs markup; CSSOM writes are not markup. That keeps
 * the whole feature inside a single script-src hash and means an app with a
 * strict style-src still gets correctly styled navigation chrome.
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
  var POSITION_KEY = "shinyhub-app-nav:position";
  var LEGACY_POSITION_KEY = POSITION_KEY + ":";
  var FILTER_THRESHOLD = 8;
  var POSITIONS = ["top-center", "top-right", "left-center", "right-center"];

  var tag = document.currentScript || document.getElementById(TAG_ID);
  if (!tag) {
    return;
  }

  // A framed app is furniture inside someone else's page, not a destination.
  // Floating our switcher over an embed would put ShinyHub's chrome in a layout it
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
  // remember a dismissal simply shows the full switcher again, which is the safe way to
  // be wrong.
  function dismissed() {
    try {
      return window.sessionStorage.getItem(DISMISS_KEY) === "1";
    } catch (e) {
      return false;
    }
  }

  function rememberDismissal(next) {
    try {
      if (next) {
        window.sessionStorage.setItem(DISMISS_KEY, "1");
      } else {
        window.sessionStorage.removeItem(DISMISS_KEY);
      }
    } catch (e) {
      /* not remembering is acceptable; showing it again is not a fault */
    }
  }

  function rememberedPosition() {
    try {
      var saved = window.localStorage.getItem(POSITION_KEY);
      // Placements used to be scoped to each app. Adopt the current app's old
      // choice once, then keep it hub-wide so following a switcher link does
      // not move the control to a different edge on the destination app.
      if (POSITIONS.indexOf(saved) === -1 && currentSlug) {
        saved = window.localStorage.getItem(LEGACY_POSITION_KEY + currentSlug);
        if (POSITIONS.indexOf(saved) !== -1) {
          window.localStorage.setItem(POSITION_KEY, saved);
        }
      }
      return POSITIONS.indexOf(saved) === -1 ? "top-right" : saved;
    } catch (e) {
      return "top-right";
    }
  }

  function rememberPosition(next) {
    try {
      window.localStorage.setItem(POSITION_KEY, next);
    } catch (e) {
      /* placement persistence is convenience, never a page requirement */
    }
  }

  var initiallyDismissed = dismissed();

  /* ---------------------------------------------------------------------
   * Grouping. This is a deliberate copy of the rule in
   * internal/ui/static/views/project-groups.js: ungrouped apps first, then
   * named projects by display name with the project slug as a tiebreak so the
   * order is total; apps sorted by display name within each group. The
   * switcher layers one contextual rule on that stable base: the current
   * project comes first, with the current app first inside it.
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

  function prioritizeCurrentContext(groups, allApps) {
    var currentProject = null;
    var foundCurrent = false;
    var source = allApps || [];
    for (var i = 0; i < source.length; i++) {
      if (source[i] && source[i].slug === currentSlug) {
        currentProject = projectKeyOf(source[i]);
        foundCurrent = true;
        break;
      }
    }
    if (!foundCurrent) {
      return groups;
    }

    var prioritized = groups.slice();
    var groupIndex = -1;
    for (var g = 0; g < prioritized.length; g++) {
      if (prioritized[g].project === currentProject) {
        groupIndex = g;
        break;
      }
    }
    // A filter can remove every app in the current project. In that case
    // there is no contextual group left to promote.
    if (groupIndex === -1) {
      return prioritized;
    }

    var currentGroup = prioritized[groupIndex];
    var members = currentGroup.apps.slice();
    var appIndex = -1;
    for (var a = 0; a < members.length; a++) {
      if (members[a].slug === currentSlug) {
        appIndex = a;
        break;
      }
    }
    if (appIndex > 0) {
      members.unshift(members.splice(appIndex, 1)[0]);
      currentGroup = {
        project: currentGroup.project,
        name: currentGroup.name,
        iconEmoji: currentGroup.iconEmoji,
        apps: members
      };
    }
    if (groupIndex > 0) {
      prioritized.splice(groupIndex, 1);
      prioritized.unshift(currentGroup);
    } else {
      prioritized[0] = currentGroup;
    }
    return prioritized;
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
      "  --sh-deep: #030510; --sh-surface: #0E1426; --sh-raised: #141B32;" +
      "  --sh-hover: #1B2444; --sh-line: #1E2A4A; --sh-line-strong: #2B3A63;" +
      "  --sh-text: #E8EEFF; --sh-soft: #A8B4D4; --sh-muted: #6B7AA3;" +
      "  --sh-signal: #38BDF8; --sh-coral: #F87171;" +
      "  --sh-r-sm: 4px; --sh-r-md: 8px; --sh-r-lg: 14px; --sh-r-pill: 99px;" +
      "  position: fixed; inset: 0; z-index: 2147483646;" +
      "  font-family: Manrope, -apple-system, BlinkMacSystemFont, system-ui, 'Segoe UI', sans-serif;" +
      "  font-size: 14px; line-height: 1.55; letter-spacing: -0.005em; color: var(--sh-text);" +
      "  pointer-events: none; user-select: none; -webkit-user-select: none;" +
      "}",
    ".bar {" +
      "  position: absolute; width: min(264px, calc(100vw - 24px)); height: 40px; box-sizing: border-box;" +
      "  display: flex; align-items: center; pointer-events: auto; overflow: hidden;" +
      "  color: var(--sh-text); background: var(--sh-surface); border: 1px solid var(--sh-line-strong);" +
      "  border-radius: var(--sh-r-md); box-shadow: 0 10px 30px -16px rgba(0,0,0,0.8);" +
      "  transition: border-color 140ms ease, box-shadow 140ms ease, opacity 140ms ease;" +
      "}",
    ".bar:hover, .bar:focus-within, .root.open .bar, .root.placing .bar {" +
      "  border-color: var(--sh-signal); box-shadow: 0 10px 30px -16px rgba(0,0,0,0.8);" +
      "}",
    ".root.dismissed .bar { display: none; }",
    ".root[data-position='top-center'] .bar { top: 12px; left: 50%; transform: translateX(-50%); }",
    ".root[data-position='top-right'] .bar { top: 12px; right: 12px; }",
    ".root[data-position='left-center'] .bar { top: 50%; left: 12px; transform: translateY(-50%); }",
    ".root[data-position='right-center'] .bar { top: 50%; right: 12px; transform: translateY(-50%); }",
    ".root[data-position='left-center'] .bar, .root[data-position='right-center'] .bar {" +
      "  width: 40px; height: 104px; flex-direction: column;" +
      "}",
    ".control {" +
      "  appearance: none; -webkit-appearance: none; border: 0; margin: 0; padding: 0;" +
      "  height: 38px; display: flex; align-items: center; justify-content: center;" +
      "  color: var(--sh-soft); background: transparent; font: inherit; cursor: pointer;" +
      "}",
    ".control:hover { color: var(--sh-text); background: var(--sh-hover); }",
    ".control:focus-visible { outline: 2px solid var(--sh-signal); outline-offset: -3px; }",
    ".control svg { width: 16px; height: 16px; display: block; }",
    ".move { width: 30px; flex: none; cursor: grab; color: var(--sh-muted); touch-action: none; }",
    ".root[data-position='left-center'] .move, .root[data-position='right-center'] .move { width: 38px; height: 30px; }",
    ".root.dragging .move { cursor: grabbing; color: var(--sh-signal); }",
    ".move svg { width: 14px; height: 14px; }",
    ".switch {" +
      "  min-width: 0; flex: 1; justify-content: flex-start; gap: 8px; padding: 0 8px;" +
      "  border-left: 1px solid var(--sh-line); border-right: 1px solid var(--sh-line);" +
      "}",
    ".root[data-position='left-center'] .switch, .root[data-position='right-center'] .switch {" +
      "  width: 38px; height: 40px; flex: none; justify-content: center; padding: 0;" +
      "  border: 0; border-top: 1px solid var(--sh-line); border-bottom: 1px solid var(--sh-line);" +
      "}",
    ".switchmark {" +
      "  width: 24px; height: 24px; flex: none; display: flex; align-items: center; justify-content: center;" +
      "  color: var(--sh-signal); background: var(--sh-raised); border-radius: var(--sh-r-sm);" +
      "}",
    ".switchmark svg { width: 14px; height: 14px; }",
    ".root[data-position='left-center'] .switchmark, .root[data-position='right-center'] .switchmark { background: transparent; }",
    ".current-meta { min-width: 0; flex: 1; text-align: left; }",
    ".compact-label { display: none; color: var(--sh-text); font-size: 12px; font-weight: 600; }",
    ".current-label {" +
      "  display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;" +
      "  color: var(--sh-text); font-size: 13px; font-weight: 600; line-height: 1.2;" +
      "}",
    ".current-action { display: block; color: var(--sh-soft); font-size: 12px; line-height: 1.2; }",
    ".root[data-position='left-center'] .current-meta, .root[data-position='right-center'] .current-meta," +
      " .root[data-position='left-center'] .chevron, .root[data-position='right-center'] .chevron { display: none; }",
    ".chevron { flex: none; transition: transform 140ms ease; }",
    ".root.open .chevron { transform: rotate(180deg); }",
    ".close { width: 32px; flex: none; color: var(--sh-muted); }",
    ".root[data-position='left-center'] .close, .root[data-position='right-center'] .close { width: 38px; height: 32px; }",
    ".close:hover { color: var(--sh-coral); background: var(--sh-raised); }",
    ".close svg { width: 13px; height: 13px; }",
    ".scrim {" +
      "  position: absolute; inset: 0; background: var(--sh-deep); opacity: 0;" +
      "  pointer-events: none; transition: opacity 160ms ease;" +
      "}",
    ".root.open .scrim { opacity: 0.14; pointer-events: auto; }",
    ".root.placing .scrim { opacity: 0; pointer-events: auto; }",

    ".panel {" +
      "  position: absolute; width: 320px; max-width: calc(100vw - 24px);" +
      "  max-height: min(560px, calc(100vh - 72px)); box-sizing: border-box;" +
      "  display: flex; flex-direction: column; pointer-events: auto; overflow: hidden;" +
      "  color: var(--sh-text); background: var(--sh-surface);" +
      "  border: 1px solid var(--sh-line-strong); border-radius: var(--sh-r-lg);" +
      "  box-shadow: 0 32px 80px rgba(0,0,0,0.7);" +
      "  opacity: 0; visibility: hidden; transform: translateY(-8px) scale(0.98);" +
      "  transform-origin: top center; transition: opacity 150ms ease, transform 180ms cubic-bezier(0.22,1,0.36,1), visibility 180ms;" +
      "}",
    ".root[data-position='top-center'] .panel { top: 60px; left: 50%; transform-origin: top center; }",
    ".root[data-position='top-right'] .panel { top: 60px; right: 12px; transform-origin: top right; }",
    ".root[data-position='left-center'] .panel { top: 50%; left: 60px; transform-origin: left center; }",
    ".root[data-position='right-center'] .panel { top: 50%; right: 60px; transform-origin: right center; }",
    ".root.open[data-position='top-center'] .panel { transform: translateX(-50%) translateY(0) scale(1); }",
    ".root.open[data-position='top-right'] .panel { transform: translateY(0) scale(1); }",
    ".root.open[data-position='left-center'] .panel { transform: translateY(-50%) scale(1); }",
    ".root.open[data-position='right-center'] .panel { transform: translateY(-50%) scale(1); }",
    ".root.open .panel { opacity: 1; visibility: visible; }",
    // The panel takes focus itself while the list loads, so it needs a ring for
    // the seconds it holds it - otherwise a keyboard visitor is somewhere with
    // nothing on screen saying where. :focus-visible keeps it to the visitors
    // who arrived by keyboard; a mouse click never draws it.
    ".panel:focus { outline: none; }",
    ".panel:focus-visible { outline: none; box-shadow: inset 0 0 0 2px var(--sh-signal); }",

    ".head {" +
      "  display: flex; align-items: center; gap: 8px;" +
      "  padding: 14px 16px 10px; border-bottom: 1px solid var(--sh-line);" +
      "}",
    ".title {" +
      "  min-width: 0; max-width: 210px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;" +
      "  font-size: 12px; font-weight: 600; line-height: 1.35; letter-spacing: 0.02em;" +
      "  text-transform: uppercase; color: var(--sh-soft);" +
      "}",
    ".count {" +
      "  font-size: 12px; font-weight: 600; color: var(--sh-soft); background: var(--sh-raised);" +
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
    ".filter::placeholder { color: var(--sh-soft); }",
    ".filter:focus { outline: none; border-color: var(--sh-signal); }",

    ".list { flex: 1; min-height: 0; overflow-y: auto; overscroll-behavior: contain; padding: 8px; }",
    ".list { scrollbar-color: var(--sh-line-strong) transparent; scrollbar-width: thin; }",
    ".grouphead {" +
      "  display: flex; align-items: center; gap: 8px; padding: 16px 8px 4px;" +
      "  font-size: 12px; font-weight: 600; line-height: 1.35; letter-spacing: 0.02em;" +
      "  text-transform: uppercase; color: var(--sh-soft);" +
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
    ".flag { color: var(--sh-soft); }",
    ".here { color: var(--sh-signal); }",
    ".opening-state {" +
      "  flex: none; display: flex; align-items: center; gap: 6px; color: var(--sh-signal);" +
      "  font-size: 12px; font-weight: 600; letter-spacing: 0.02em; text-transform: uppercase;" +
      "}",
    ".opening-spinner {" +
      "  width: 12px; height: 12px; box-sizing: border-box; border-radius: 50%;" +
      "  border: 2px solid var(--sh-line-strong); border-top-color: var(--sh-signal);" +
      "  animation: sh-nav-spin 700ms linear infinite;" +
      "}",
    ".root.navigating .panel { cursor: progress; }",
    ".root.navigating .item, .root.navigating .home, .root.navigating .headclose," +
      " .root.navigating .filter { pointer-events: none; }",
    ".root.navigating .item:not(.opening-item), .root.navigating .home," +
      " .root.navigating .filterwrap, .root.navigating .headclose { opacity: 0.45; }",
    ".root.navigating .item.opening-item {" +
      "  background: var(--sh-hover); border-color: var(--sh-signal); color: var(--sh-text);" +
      "}",
    ".root.navigating .scrim, .root.navigating .bar { pointer-events: none; }",
    ".announcer {" +
      "  position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px;" +
      "  overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0;" +
      "}",
    "@keyframes sh-nav-spin { to { transform: rotate(360deg); } }",

    ".note { padding: 16px 8px; color: var(--sh-soft); }",
    ".more { padding: 8px 8px 16px; color: var(--sh-soft); font-size: 12px; line-height: 1.35; }",
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
      "  margin-left: auto; font-size: 12px; color: var(--sh-soft); max-width: 45%;" +
      "  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;" +
      "}",

    ".position-menu {" +
      "  position: absolute; width: 212px; box-sizing: border-box; padding: 8px;" +
      "  pointer-events: auto; color: var(--sh-text); background: var(--sh-surface);" +
      "  border: 1px solid var(--sh-line-strong); border-radius: var(--sh-r-lg);" +
      "  box-shadow: 0 32px 80px rgba(0,0,0,0.7);" +
      "  opacity: 0; visibility: hidden; transform: translateY(-6px);" +
      "  transition: opacity 140ms ease, transform 160ms cubic-bezier(0.22,1,0.36,1), visibility 160ms;" +
      "}",
    ".root.placing .position-menu { opacity: 1; visibility: visible; transform: none; }",
    ".root[data-position='top-center'] .position-menu { top: 60px; left: 50%; margin-left: -132px; }",
    ".root[data-position='top-right'] .position-menu { top: 60px; right: 12px; }",
    ".root[data-position='left-center'] .position-menu { top: 50%; left: 60px; margin-top: -106px; }",
    ".root[data-position='right-center'] .position-menu { top: 50%; right: 60px; margin-top: -106px; }",
    ".menu-title { padding: 4px 8px 8px; color: var(--sh-soft); font-size: 12px; font-weight: 600; }",
    ".place-option {" +
      "  appearance: none; -webkit-appearance: none; width: 100%; min-height: 36px;" +
      "  display: flex; align-items: center; gap: 10px; margin: 0; padding: 7px 8px;" +
      "  border: 0; border-radius: var(--sh-r-md); background: transparent; color: var(--sh-soft);" +
      "  font: inherit; font-size: 13px; text-align: left; cursor: pointer;" +
      "}",
    ".place-option:hover { color: var(--sh-text); background: var(--sh-hover); }",
    ".place-option:focus-visible { outline: 2px solid var(--sh-signal); outline-offset: -2px; }",
    ".place-option[aria-checked='true'] { color: var(--sh-text); background: var(--sh-raised); }",
    ".place-map {" +
      "  position: relative; width: 24px; height: 16px; flex: none; box-sizing: border-box;" +
      "  border: 1px solid var(--sh-line-strong); border-radius: 3px;" +
      "}",
    ".place-map::after {" +
      "  content: ''; position: absolute; width: 8px; height: 3px; border-radius: var(--sh-r-sm); background: var(--sh-signal);" +
      "}",
    ".place-option[data-position='top-center'] .place-map::after { top: 2px; left: 7px; }",
    ".place-option[data-position='top-right'] .place-map::after { top: 2px; right: 2px; }",
    ".place-option[data-position='left-center'] .place-map::after { top: 6px; left: 2px; }",
    ".place-option[data-position='right-center'] .place-map::after { top: 6px; right: 2px; }",

    ".snap-guides { position: absolute; inset: 0; opacity: 0; visibility: hidden; transition: opacity 100ms ease; }",
    ".root.dragging .snap-guides { opacity: 1; visibility: visible; }",
    ".guide {" +
      "  position: absolute; width: 74px; height: 18px; box-sizing: border-box;" +
      "  border: 1px dashed var(--sh-line-strong); border-radius: var(--sh-r-md);" +
      "  background: rgba(14,20,38,0.58); transition: border-color 100ms ease, background 100ms ease;" +
      "}",
    ".guide.nearest { border-color: var(--sh-signal); background: rgba(56,189,248,0.18); }",
    ".guide[data-position='top-center'] { top: 12px; left: 50%; transform: translateX(-50%); }",
    ".guide[data-position='top-right'] { top: 12px; right: 12px; }",
    ".guide[data-position='left-center'] { top: 50%; left: 12px; transform: translateY(-50%) rotate(90deg); }",
    ".guide[data-position='right-center'] { top: 50%; right: 12px; transform: translateY(-50%) rotate(90deg); }",

    ".restore {" +
      "  position: absolute; width: 30px; height: 30px; box-sizing: border-box;" +
      "  display: none; align-items: center; justify-content: center; pointer-events: auto;" +
      "  appearance: none; -webkit-appearance: none; margin: 0; padding: 0; cursor: pointer;" +
      "  color: var(--sh-signal); background: var(--sh-surface); border: 1px solid var(--sh-line-strong);" +
      "  box-shadow: 0 10px 30px -16px rgba(0,0,0,0.8);" +
      "}",
    ".root.dismissed .restore { display: flex; }",
    ".restore:hover { color: var(--sh-text); background: var(--sh-hover); }",
    ".restore:focus-visible { outline: 2px solid var(--sh-signal); outline-offset: 2px; }",
    ".restore svg { width: 14px; height: 14px; }",
    ".root[data-position='top-center'] .restore { top: 0; left: 50%; transform: translateX(-50%); border-top: 0; border-radius: 0 0 var(--sh-r-md) var(--sh-r-md); }",
    ".root[data-position='top-right'] .restore { top: 0; right: 12px; border-top: 0; border-radius: 0 0 var(--sh-r-md) var(--sh-r-md); }",
    ".root[data-position='left-center'] .restore { top: 50%; left: 0; transform: translateY(-50%); border-left: 0; border-radius: 0 var(--sh-r-md) var(--sh-r-md) 0; }",
    ".root[data-position='right-center'] .restore { top: 50%; right: 0; transform: translateY(-50%); border-right: 0; border-radius: var(--sh-r-md) 0 0 var(--sh-r-md); }",

    "@media (max-width: 520px) {" +
      " .bar { width: min(240px, calc(100vw - 24px)); }" +
      " .root.compact[data-position='top-center'] .bar, .root.compact[data-position='top-right'] .bar { width: 140px; }" +
      " .root.compact[data-position='top-center'] .current-meta, .root.compact[data-position='top-right'] .current-meta," +
      " .root.compact[data-position='top-center'] .chevron, .root.compact[data-position='top-right'] .chevron { display: none; }" +
      " .root.compact[data-position='top-center'] .compact-label, .root.compact[data-position='top-right'] .compact-label { display: block; }" +
      " .root.compact[data-position='top-center'] .switch, .root.compact[data-position='top-right'] .switch { justify-content: center; }" +
      " .panel, .root[data-position='top-center'] .panel, .root[data-position='top-right'] .panel," +
      " .root[data-position='left-center'] .panel, .root[data-position='right-center'] .panel {" +
      "   top: 60px; right: auto; bottom: auto; left: 12px; width: calc(100vw - 24px);" +
      "   max-width: none; max-height: calc(100vh - 72px); margin: 0; transform-origin: top center;" +
      " }" +
      " .root.open[data-position='top-center'] .panel, .root.open[data-position='top-right'] .panel," +
      " .root.open[data-position='left-center'] .panel, .root.open[data-position='right-center'] .panel { transform: none; }" +
      " .position-menu, .root[data-position='top-center'] .position-menu, .root[data-position='top-right'] .position-menu," +
      " .root[data-position='left-center'] .position-menu, .root[data-position='right-center'] .position-menu {" +
      "   top: 60px; right: 12px; left: auto; margin: 0;" +
      " }" +
      "}",

    "@media (prefers-reduced-motion: reduce) {" +
      "  .panel, .scrim, .bar, .position-menu, .snap-guides, .guide, .chevron { transition: none; }" +
      "  .opening-spinner { animation: none; border-color: var(--sh-signal); }" +
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

  function readableSlug(slug) {
    var text = String(slug || "").replace(/[-_]+/g, " ").trim();
    return text ? text.charAt(0).toUpperCase() + text.slice(1) : "Current app";
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
  var position = rememberedPosition();
  root.setAttribute("data-position", position);
  root.classList.toggle("dismissed", initiallyDismissed);
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

  var bar = div("bar");
  bar.setAttribute("role", "toolbar");
  bar.setAttribute("aria-label", "ShinyHub app navigation");

  var moveBtn = document.createElement("button");
  moveBtn.type = "button";
  moveBtn.className = "control move";
  moveBtn.setAttribute("aria-label", "Move the app switcher");
  moveBtn.setAttribute("aria-haspopup", "menu");
  moveBtn.setAttribute("aria-expanded", "false");
  moveBtn.title = "Move switcher";
  moveBtn.appendChild(
    svg(
      [
        "M7 5h.01", "M13 5h.01",
        "M7 10h.01", "M13 10h.01",
        "M7 15h.01", "M13 15h.01"
      ],
      20
    )
  );

  var openBtn = document.createElement("button");
  openBtn.type = "button";
  openBtn.className = "control switch";
  openBtn.setAttribute("aria-label", "Switch apps");
  openBtn.setAttribute("aria-expanded", "false");
  openBtn.title = "Switch app";
  var switchMark = div("switchmark");
  switchMark.appendChild(
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
  var current = div("current-meta");
  var compactLabel = div("compact-label", "Apps");
  var currentLabel = div("current-label", readableSlug(currentSlug));
  var currentAction = div("current-action", "Switch app");
  current.appendChild(currentLabel);
  current.appendChild(currentAction);
  var chevron = div("chevron");
  chevron.appendChild(svg(["M6 8l4 4 4-4"], 20));
  openBtn.appendChild(switchMark);
  openBtn.appendChild(compactLabel);
  openBtn.appendChild(current);
  openBtn.appendChild(chevron);
  openBtn.setAttribute(
    "aria-label",
    "Switch apps, current app " + (currentLabel.textContent || currentSlug || "unknown")
  );

  var closeBtn = document.createElement("button");
  closeBtn.type = "button";
  closeBtn.className = "control close";
  closeBtn.setAttribute("aria-label", "Hide the app switcher for this tab");
  // A dismissal is remembered in sessionStorage, so it outlives a reload and
  // ends when the tab does. Wording it as "until this tab is reloaded" would
  // promise a way back that reloading does not deliver.
  closeBtn.title = "Hide the switcher in this tab";
  closeBtn.appendChild(svg(["M4 4l12 12", "M16 4L4 16"], 20));

  bar.appendChild(moveBtn);
  bar.appendChild(openBtn);
  bar.appendChild(closeBtn);

  var positionMenu = div("position-menu");
  positionMenu.setAttribute("role", "menu");
  positionMenu.setAttribute("aria-label", "App switcher position");
  positionMenu.setAttribute("aria-hidden", "true");
  positionMenu.appendChild(div("menu-title", "Move switcher"));
  var positionNames = {
    "top-center": "Top centre",
    "top-right": "Top right",
    "left-center": "Left centre",
    "right-center": "Right centre"
  };
  var positionButtons = [];
  for (var pi = 0; pi < POSITIONS.length; pi++) {
    var optionPosition = POSITIONS[pi];
    var option = document.createElement("button");
    option.type = "button";
    option.className = "place-option";
    option.setAttribute("role", "menuitemradio");
    option.setAttribute("data-position", optionPosition);
    option.setAttribute("aria-checked", optionPosition === position ? "true" : "false");
    option.setAttribute("tabindex", optionPosition === position ? "0" : "-1");
    option.appendChild(div("place-map"));
    option.appendChild(document.createTextNode(positionNames[optionPosition]));
    positionMenu.appendChild(option);
    positionButtons.push(option);
  }

  var snapGuides = div("snap-guides");
  var guides = [];
  for (var gi = 0; gi < POSITIONS.length; gi++) {
    var guide = div("guide");
    guide.setAttribute("data-position", POSITIONS[gi]);
    snapGuides.appendChild(guide);
    guides.push(guide);
  }

  var restoreBtn = document.createElement("button");
  restoreBtn.type = "button";
  restoreBtn.className = "restore";
  restoreBtn.setAttribute("aria-label", "Show the app switcher");
  restoreBtn.title = "Show app switcher";
  restoreBtn.appendChild(
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

  var head = div("head");
  var title = div("title", "Switch app");
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

  // This status sits outside the panel's aria-busy subtree, so assistive
  // technology announces a switch immediately rather than waiting for the
  // destination page to finish loading.
  var announcer = div("announcer");
  announcer.setAttribute("role", "status");
  announcer.setAttribute("aria-live", "polite");
  announcer.setAttribute("aria-atomic", "true");
  root.appendChild(scrim);
  root.appendChild(panel);
  root.appendChild(announcer);
  root.appendChild(positionMenu);
  root.appendChild(snapGuides);
  root.appendChild(bar);
  root.appendChild(restoreBtn);
  shadow.appendChild(root);
  document.body.appendChild(host);

  /* ---------------------------------------------------------------------
   * State + rendering.
   * ------------------------------------------------------------------- */

  var open = false;
  var placing = false;
  var loaded = false;
  var loading = false;
  var payload = null;
  var lastFocus = null;
  var compactTimer = null;
  var navigating = false;

  function mobileViewport() {
    try {
      return typeof window.matchMedia === "function" && window.matchMedia("(max-width: 520px)").matches;
    } catch (e) {
      return false;
    }
  }

  function revealBar(schedule) {
    if (compactTimer !== null) {
      window.clearTimeout(compactTimer);
      compactTimer = null;
    }
    root.classList.remove("compact");
    if (!schedule || !mobileViewport() || root.classList.contains("dismissed")) {
      return;
    }
    compactTimer = window.setTimeout(
      guard(function () {
        compactTimer = null;
        if (
          mobileViewport() &&
          !open &&
          !placing &&
          !drag &&
          !root.classList.contains("dismissed") &&
          !(shadow.activeElement && bar.contains(shadow.activeElement))
        ) {
          root.classList.add("compact");
        }
      }),
      8000
    );
  }

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

    var groups = prioritizeCurrentContext(groupApps(visible), payload.apps);
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
      if (!here) {
        link.addEventListener("click", guard(function (ev) {
          if (!sameTabNavigation(ev, link)) {
            return;
          }
          if (navigating) {
            ev.preventDefault();
            return;
          }
          beginNavigation(link, String(app.name || app.slug));
        }));
      }
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

  // New-tab and modified clicks deliberately do not put this tab into a busy
  // state: its page is staying put. For an ordinary activation, the native
  // anchor navigation remains in charge while the switcher immediately makes
  // the destination and the in-progress transition visible.
  function sameTabNavigation(ev, link) {
    if (
      (ev.button !== undefined && ev.button !== 0) ||
      ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey
    ) {
      return false;
    }
    var explicitTarget = link.getAttribute("target");
    var base = document.querySelector("base[target]");
    var target = explicitTarget || (base && base.getAttribute("target")) || "";
    return !target || target === "_self" || target === "_top" || target === "_parent";
  }

  function beginNavigation(link, name) {
    navigating = true;
    root.classList.add("navigating");
    panel.setAttribute("aria-busy", "true");
    title.textContent = "Opening " + name + "\u2026";
    title.title = "Opening " + name;
    announcer.textContent = "Opening " + name;
    countBadge.hidden = true;
    filter.disabled = true;
    headClose.disabled = true;
    homeLink.setAttribute("aria-disabled", "true");
    homeLink.setAttribute("tabindex", "-1");

    var links = list.querySelectorAll("a.item");
    for (var i = 0; i < links.length; i++) {
      links[i].setAttribute("aria-disabled", "true");
      links[i].setAttribute("tabindex", "-1");
    }

    link.classList.add("opening-item");
    link.setAttribute("aria-label", name + ", opening");
    var state = div("opening-state");
    var spinner = div("opening-spinner");
    spinner.setAttribute("aria-hidden", "true");
    state.appendChild(spinner);
    state.appendChild(document.createTextNode("Opening"));
    link.appendChild(state);
  }

  // A page restored from the back-forward cache keeps this exact DOM. Clear
  // the departed-page state so returning to it never leaves a frozen switcher.
  function resetNavigation() {
    if (!navigating) {
      return;
    }
    navigating = false;
    root.classList.remove("navigating");
    panel.removeAttribute("aria-busy");
    title.textContent = "Switch app";
    title.removeAttribute("title");
    announcer.textContent = "";
    countBadge.hidden = false;
    filter.disabled = false;
    headClose.disabled = false;
    homeLink.removeAttribute("aria-disabled");
    homeLink.removeAttribute("tabindex");

    var links = list.querySelectorAll("a.item");
    for (var i = 0; i < links.length; i++) {
      links[i].removeAttribute("aria-disabled");
      links[i].removeAttribute("tabindex");
      if (links[i].classList.contains("opening-item")) {
        links[i].classList.remove("opening-item");
        links[i].removeAttribute("aria-label");
      }
    }
    var state = list.querySelector(".opening-state");
    if (state && state.parentNode) {
      state.parentNode.removeChild(state);
    }
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
  // soon as one exists. Leaving focus on the bar button instead would strand a
  // keyboard visitor on a control the open panel has just hidden.
  //
  // The order below is deliberate and is not DOM order. The filter is what a
  // visitor with a long list opened the panel to use; the first app is what a
  // visitor with a short one opened it to pick. The close button and the
  // dashboard link are focusable too and both sit in the panel, but landing on
  // either turns the visitor's first Enter into "leave" - the opposite of what
  // opening the panel asked for.
  var pendingFocus = false;

  function updatePositionControls() {
    for (var i = 0; i < positionButtons.length; i++) {
      positionButtons[i].setAttribute(
        "aria-checked",
        positionButtons[i].getAttribute("data-position") === position ? "true" : "false"
      );
      positionButtons[i].setAttribute(
        "tabindex",
        positionButtons[i].getAttribute("data-position") === position ? "0" : "-1"
      );
    }
  }

  function setPosition(next, persist) {
    if (POSITIONS.indexOf(next) === -1) {
      return;
    }
    position = next;
    root.setAttribute("data-position", position);
    updatePositionControls();
    if (persist !== false) {
      rememberPosition(position);
    }
  }

  function setPlacing(next) {
    placing = !!next;
    revealBar(!placing);
    root.classList.toggle("placing", placing);
    positionMenu.setAttribute("aria-hidden", placing ? "false" : "true");
    moveBtn.setAttribute("aria-expanded", placing ? "true" : "false");
    if (placing && open) {
      setOpen(false);
    }
  }

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
    revealBar(!open);
    if (open && placing) {
      setPlacing(false);
    }
    root.classList.toggle("open", open);
    panel.setAttribute("aria-hidden", open ? "false" : "true");
    openBtn.setAttribute("aria-expanded", open ? "true" : "false");
    if (open) {
      // Focus inside a shadow root is reported by the root, not by the
      // document: document.activeElement retargets to the host, which here is
      // a plain div that cannot take focus back. Reading the root first is
      // what makes the bar button restorable, and it is the only way to see
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
    setPlacing(false);
    rememberDismissal(true);
    root.classList.add("dismissed");
    restoreBtn.focus();
  }));

  restoreBtn.addEventListener("click", guard(function () {
    rememberDismissal(false);
    root.classList.remove("dismissed");
    revealBar(true);
    openBtn.focus();
  }));

  moveBtn.addEventListener("click", guard(function () {
    if (suppressMoveClick) {
      suppressMoveClick = false;
      return;
    }
    setPlacing(!placing);
    if (placing && positionButtons.length) {
      for (var i = 0; i < positionButtons.length; i++) {
        if (positionButtons[i].getAttribute("aria-checked") === "true") {
          positionButtons[i].focus();
          break;
        }
      }
    }
  }));

  for (var pbi = 0; pbi < positionButtons.length; pbi++) {
    positionButtons[pbi].addEventListener("click", guard(function (ev) {
      setPosition(ev.currentTarget.getAttribute("data-position"));
      setPlacing(false);
      moveBtn.focus();
    }));
  }

  var drag = null;
  var suppressMoveClick = false;

  function nearestPosition(x, y) {
    var width = window.innerWidth || document.documentElement.clientWidth || 1;
    var height = window.innerHeight || document.documentElement.clientHeight || 1;
    var barWidth = bar.offsetWidth || Math.min(264, Math.max(1, width - 24));
    var half = barWidth / 2;
    var points = {
      "top-center": [width / 2, 32],
      "top-right": [width - 12 - half, 32],
      "left-center": [12 + half, height / 2],
      "right-center": [width - 12 - half, height / 2]
    };
    var closest = POSITIONS[0];
    var best = Infinity;
    for (var i = 0; i < POSITIONS.length; i++) {
      var point = points[POSITIONS[i]];
      var dx = x - point[0];
      var dy = y - point[1];
      var distance = dx * dx + dy * dy;
      if (distance < best) {
        best = distance;
        closest = POSITIONS[i];
      }
    }
    return closest;
  }

  function markNearest(next) {
    for (var i = 0; i < guides.length; i++) {
      guides[i].classList.toggle(
        "nearest",
        guides[i].getAttribute("data-position") === next
      );
    }
  }

  moveBtn.addEventListener("pointerdown", guard(function (ev) {
    if (ev.button !== undefined && ev.button !== 0) {
      return;
    }
    drag = {
      id: ev.pointerId,
      startX: ev.clientX,
      startY: ev.clientY,
      x: ev.clientX,
      y: ev.clientY,
      moved: false
    };
    setPlacing(false);
    setOpen(false);
    revealBar(false);
    if (typeof moveBtn.setPointerCapture === "function") {
      moveBtn.setPointerCapture(ev.pointerId);
    }
  }));

  moveBtn.addEventListener("pointermove", guard(function (ev) {
    if (!drag || (ev.pointerId !== undefined && ev.pointerId !== drag.id)) {
      return;
    }
    drag.x = ev.clientX;
    drag.y = ev.clientY;
    var dx = drag.x - drag.startX;
    var dy = drag.y - drag.startY;
    if (!drag.moved && dx * dx + dy * dy < 25) {
      return;
    }
    drag.moved = true;
    ev.preventDefault();
    var width = window.innerWidth || document.documentElement.clientWidth || 1;
    var height = window.innerHeight || document.documentElement.clientHeight || 1;
    var halfWidth = (bar.offsetWidth || 264) / 2;
    var halfHeight = 20;
    var x = Math.max(12 + halfWidth, Math.min(width - 12 - halfWidth, drag.x));
    var y = Math.max(12 + halfHeight, Math.min(height - 12 - halfHeight, drag.y));
    root.classList.add("dragging");
    bar.style.top = String(y - halfHeight) + "px";
    bar.style.right = "auto";
    bar.style.left = String(x - halfWidth) + "px";
    bar.style.transform = "none";
    markNearest(nearestPosition(drag.x, drag.y));
  }));

  function finishDrag(ev) {
    if (!drag || (ev.pointerId !== undefined && ev.pointerId !== drag.id)) {
      return;
    }
    if (drag.moved) {
      suppressMoveClick = true;
      setPosition(nearestPosition(drag.x, drag.y));
    }
    root.classList.remove("dragging");
    bar.style.top = "";
    bar.style.right = "";
    bar.style.left = "";
    bar.style.transform = "";
    markNearest("");
    drag = null;
    revealBar(true);
  }

  moveBtn.addEventListener("pointerup", guard(finishDrag));
  moveBtn.addEventListener("pointercancel", guard(finishDrag));

  bar.addEventListener("pointerenter", guard(function () {
    revealBar(false);
  }));
  bar.addEventListener("pointerleave", guard(function () {
    revealBar(true);
  }));
  bar.addEventListener("focusin", guard(function () {
    revealBar(false);
  }));
  bar.addEventListener("focusout", guard(function () {
    revealBar(true);
  }));
  window.addEventListener("resize", guard(function () {
    revealBar(true);
  }));
  window.addEventListener("pageshow", guard(resetNavigation));

  headClose.addEventListener("click", guard(function () {
    setOpen(false);
  }));

  scrim.addEventListener("click", guard(function () {
    if (placing) {
      setPlacing(false);
      moveBtn.focus();
      return;
    }
    setOpen(false);
  }));

  filter.addEventListener("input", guard(function () {
    renderList();
  }));

  // Key handling is bound on the shadow root, not the document: an app that
  // listens for its own shortcuts on document must not see the visitor typing
  // into our filter box, and we must not see theirs.
  root.addEventListener("keydown", guard(function (ev) {
    if (navigating) {
      return;
    }
    if (placing) {
      if (ev.key === "Escape") {
        ev.stopPropagation();
        setPlacing(false);
        moveBtn.focus();
        return;
      }
      if (ev.key === "Tab") {
        setPlacing(false);
        return;
      }
      var activePosition = positionButtons.indexOf(shadow.activeElement);
      if (activePosition === -1) {
        return;
      }
      var nextPosition = activePosition;
      if (ev.key === "ArrowDown" || ev.key === "ArrowRight") {
        nextPosition = (activePosition + 1) % positionButtons.length;
      } else if (ev.key === "ArrowUp" || ev.key === "ArrowLeft") {
        nextPosition = (activePosition - 1 + positionButtons.length) % positionButtons.length;
      } else if (ev.key === "Home") {
        nextPosition = 0;
      } else if (ev.key === "End") {
        nextPosition = positionButtons.length - 1;
      } else {
        return;
      }
      ev.preventDefault();
      ev.stopPropagation();
      for (var mi = 0; mi < positionButtons.length; mi++) {
        positionButtons[mi].setAttribute("tabindex", mi === nextPosition ? "0" : "-1");
      }
      positionButtons[nextPosition].focus();
      return;
    }
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

  revealBar(true);
})();
