/* Agent-generated page view. The server resolves every value; this module only draws them as text. */
(function () {
  "use strict";
  var DISMISS_INPUT = ".shinyhub_agent_view_dismiss", SELECTOR = ".shinyhub-agent-view";
  var mounted = new WeakMap();
  function node(tag, cls, text) {
    var el = document.createElement(tag); if (cls) el.className = cls;
    if (text !== undefined) el.textContent = text;
    return el;
  }
  function shown(value) {
    if (value == null) return "n/a";
    if (typeof value === "number") {
      // Two decimals, unless that would show a nonzero reading as zero.
      return value.toLocaleString(undefined, value !== 0 && Math.abs(value) < 0.005
        ? { maximumSignificantDigits: 3 } : { maximumFractionDigits: 2 });
    }
    return String(value);
  }
  function table(columns, rows, caption) {
    var scroller = node("div", "sh-agent-view-table"), el = node("table"), head = node("tr"), body = node("tbody");
    columns.forEach(function (column) { var th = node("th", "", column); th.scope = "col"; head.append(th); });
    rows.forEach(function (row) {
      var tr = node("tr");
      row.forEach(function (value) {
        var td = node("td", "", shown(value));
        if (typeof value === "number") td.className = "sh-agent-view-number";
        tr.append(td);
      });
      body.append(tr);
    });
    var thead = node("thead"); thead.append(head);
    el.append(node("caption", "", caption), thead, body); scroller.append(el);
    scroller.tabIndex = 0; scroller.setAttribute("role", "region"); scroller.setAttribute("aria-label", caption);
    return scroller;
  }
  function chartTile(tile, article, embeds) {
    var chart = tile.chart, columns = Object.keys(chart.fields);
    var plot = node("div", "sh-agent-view-plot"); plot.setAttribute("role", "img"); plot.setAttribute("aria-label", tile.title);
    var status = node("p", "sh-agent-view-note", "Loading chart…"); status.setAttribute("role", "status");
    var details = node("details", "sh-agent-view-data"), built = false;
    details.append(node("summary", "", "View data"));
    function showData() {
      if (built || !details.open) return;
      built = true;
      details.append(table(columns, chart.rows.map(function (row) {
        return columns.map(function (column) { return row[column]; });
      }), tile.title + ", source data"));
    }
    details.addEventListener("toggle", showData);
    article.append(plot, status, details);
    function failed() {
      plot.hidden = true; status.textContent = "Chart could not load. The values are in the table below.";
      details.open = true; showData();
    }
    var api = window.shinyhubAgentCharts, embed = api && api.embed ? api.embed(plot, chart) : null;
    if (!embed) { failed(); return; }
    embeds.push(embed);
    embed.ready.then(function (drawn) { if (drawn) status.hidden = true; }, failed);
  }
  function tileElement(tile, embeds) {
    var article = node("article", "sh-agent-view-tile");
    article.dataset.tile = tile.id; article.dataset.kind = tile.kind; article.dataset.state = tile.state;
    article.append(node("h3", "sh-agent-view-tile-title", tile.title));
    var scope = Array.isArray(tile.scope) ? tile.scope.filter(function (pair) {
      return Array.isArray(pair) && typeof pair[0] === "string" && typeof pair[1] === "string";
    }) : [];
    if (scope.length) {
      article.append(node("p", "sh-agent-view-note sh-agent-view-scope", "Only " +
        scope.map(function (pair) { return pair[0] + ": " + pair[1]; }).join(", ")));
    }
    if (tile.state === "empty") article.append(node("p", "sh-agent-view-note", "No data for the current selection."));
    else if (tile.state === "error") {
      article.append(node("p", "sh-agent-view-note sh-agent-view-problem",
        typeof tile.message === "string" ? tile.message : "This tile could not load."));
    } else if (tile.kind === "stat") {
      article.append(node("p", "sh-agent-view-value", shown(tile.value)), node("p", "sh-agent-view-note", tile.label));
    } else if (tile.kind === "table") article.append(table(tile.columns, tile.rows, tile.title));
    else chartTile(tile, article, embeds);
    return article;
  }
  function release(entry) { entry.embeds.forEach(function (embed) { embed.dispose(); }); }
  function dispose(el) {
    var state = mounted.get(el);
    if (state) state.tiles.forEach(release);
    mounted.delete(el);
  }
  var FOCUSABLE = ["summary", ".sh-agent-view-table", ".sh-agent-view-dismiss"];
  function tileOf(el, id) {
    return Array.prototype.find.call(el.querySelectorAll("article"), function (article) { return article.dataset.tile === id; });
  }
  // What the visitor has open and focused, so a live refresh does not take it away.
  function kept(el) {
    var active = document.activeElement, focus = null;
    if (active && el.contains(active)) {
      var article = active.closest("article"), selector = FOCUSABLE.find(function (candidate) { return active.matches(candidate); });
      if (selector) focus = { tile: article ? article.dataset.tile : null, selector: selector };
    }
    return { focus: focus, open: Array.prototype.filter.call(el.querySelectorAll("details"), function (details) { return details.open; })
      .map(function (details) { return details.closest("article").dataset.tile; }),
      scroll: Array.prototype.map.call(el.querySelectorAll(".sh-agent-view-table"), function (scroller) {
        return { tile: scroller.closest("article").dataset.tile, top: scroller.scrollTop, left: scroller.scrollLeft };
      }).filter(function (entry) { return entry.top || entry.left; }) };
  }
  function restore(el, state) {
    state.open.forEach(function (id) {
      var article = tileOf(el, id), details = article && article.querySelector("details");
      if (details && !details.open) { details.open = true; details.dispatchEvent(new Event("toggle")); }
    });
    state.scroll.forEach(function (entry) {
      var article = tileOf(el, entry.tile), scroller = article && article.querySelector(".sh-agent-view-table");
      if (scroller) { scroller.scrollTop = entry.top; scroller.scrollLeft = entry.left; }
    });
    if (!state.focus) return;
    var scope = state.focus.tile === null ? el : tileOf(el, state.focus.tile);
    var target = scope && scope.querySelector(state.focus.selector);
    if (target) target.focus({ preventScroll: true });
  }
  function render(el, data) {
    var view = data && data.version === 1 ? data.view : null, key = JSON.stringify(view);
    var state = mounted.get(el);
    // Shiny re-sends an unchanged value when an unrelated dependency settles.
    if (state && state.key === key) return;
    var before = kept(el);
    if (!view || !Array.isArray(view.tiles)) { dispose(el); el.replaceChildren(); el.hidden = true; return; }
    var previous = state ? state.tiles : new Map(), tiles = new Map();
    var section = node("section", "sh-agent-view"), header = node("header", "sh-agent-view-header");
    var heading = node("div", "sh-agent-view-heading");
    heading.append(node("p", "sh-agent-view-kicker", "Built by the assistant · Live app data"),
      node("h2", "sh-agent-view-title", view.title));
    var dismiss = node("button", "sh-agent-view-dismiss", "Dismiss"); dismiss.type = "button";
    dismiss.setAttribute("aria-label", "Dismiss the generated view: " + view.title);
    dismiss.addEventListener("click", function () {
      if (window.Shiny && typeof window.Shiny.setInputValue === "function") {
        dismiss.disabled = true;
        window.Shiny.setInputValue(DISMISS_INPUT, Date.now(), { priority: "event" });
      }
    });
    header.append(heading, dismiss);
    var grid = node("div", "sh-agent-view-grid");
    view.tiles.forEach(function (tile) {
      var tileKey = JSON.stringify(tile), entry = previous.get(tile.id);
      // An unchanged tile keeps its element, so its chart is not drawn again.
      if (entry && entry.key === tileKey) previous.delete(tile.id);
      else { var embeds = []; entry = { key: tileKey, article: tileElement(tile, embeds), embeds: embeds }; }
      tiles.set(tile.id, entry); grid.append(entry.article);
    });
    previous.forEach(release);
    section.setAttribute("aria-label", "Generated view: " + view.title);
    section.append(header, grid);
    el.hidden = false; el.replaceChildren(section);
    mounted.set(el, { key: key, tiles: tiles });
    restore(el, before);
  }
  window.shinyhubAgentViews = { render: render, dispose: dispose };

  var bound = false;
  function install() {
    var shiny = window.Shiny;
    if (bound || !shiny || !shiny.OutputBinding || !shiny.outputBindings) return bound;
    bound = true;
    var binding = new shiny.OutputBinding();
    binding.find = function (scope) { return window.jQuery(scope).find(SELECTOR); };
    binding.renderValue = render;
    binding.renderError = function (el) {
      render(el, { version: 1, view: { title: "Generated view", tiles: [
        { id: "error", kind: "stat", title: "View unavailable", state: "error", message: "The generated view could not load." }] } });
    };
    binding.clearError = function () {};
    shiny.outputBindings.register(binding, "shinyhub.agentView");
    return true;
  }
  if (!install()) {
    // Shiny loaded after this script: register, then bind outputs it already passed over.
    document.addEventListener("DOMContentLoaded", function () {
      if (install() && typeof window.Shiny.bindAll === "function" && window.Shiny.shinyapp) window.Shiny.bindAll(document.body);
    });
  }
})();
