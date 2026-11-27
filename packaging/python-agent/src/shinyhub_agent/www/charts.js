/* Inline chart renderer. The server owns values; this module owns every Vega option. */
(function () {
  "use strict";
  var assetBase = new URL(".", document.currentScript.src), libraries;
  var vendor = window.shinyhubAgentChartVendor = window.shinyhubAgentChartVendor || Object.create(null);
  var instances = new Map();
  var MAX_RETAINED = 8, PAGE_SIZE = 50, LOAD_TIMEOUT = 10000, RENDER_TIMEOUT = 20000;
  function node(tag, cls, text) {
    var el = document.createElement(tag); if (cls) el.className = cls;
    if (text !== undefined) el.textContent = text;
    return el;
  }
  function load() {
    if (!libraries) libraries = ["vega", "vega-lite", "vega-embed"].reduce(function (chain, name) {
      return chain.then(function () { return new Promise(function (resolve, reject) {
        var script = document.createElement("script");
        script.src = new URL("vendor/" + name + ".min.js", assetBase).href;
        var timer = setTimeout(function () { finish(new Error("Chart library timed out")); }, LOAD_TIMEOUT);
        function finish(error) {
          clearTimeout(timer); script.onload = script.onerror = null;
          if (error) { script.remove(); reject(error); } else resolve();
        }
        script.onload = function () { finish(); };
        script.onerror = function () { finish(new Error("Chart library unavailable")); };
        document.head.append(script);
      }); });
    }, Promise.resolve()).catch(function (error) { libraries = null; throw error; });
    return libraries;
  }
  function spec(chart) {
    // Rename columns: Vega treats dots and brackets as field-path syntax.
    var names = Object.keys(chart.fields), keys = new Map(names.map(function (name, i) { return [name, "f" + i]; }));
    function field(name) { return { field: keys.get(name), type: chart.fields[name], title: name }; }
    // A date in a tooltip is the text the data holds: formatted as a time it
    // would follow the visitor's zone and could name another day than the axis.
    function written(name) {
      return chart.fields[name] === "temporal" ? { field: keys.get(name) + "w", type: "nominal", title: name } : field(name);
    }
    var encoding = { x: field(chart.x), y: Object.assign(field(chart.y), { stack: null }),
      tooltip: names.map(written) };
    if (chart.color) {
      encoding.color = Object.assign(field(chart.color), { scale: { scheme: "tableau10" } });
      if (chart.kind === "bar") encoding.xOffset = field(chart.color);
      if (chart.kind === "line" || chart.kind === "area") encoding.strokeDash = field(chart.color);
      if (chart.kind === "scatter") encoding.shape = field(chart.color);
    }
    if (chart.fields[chart.x] === "nominal") encoding.x.sort = null;
    if (chart.fields[chart.x] === "temporal") {
      var dates = Array.from(new Set(chart.rows.map(function (row) { return row[chart.x]; }).filter(Boolean)));
      encoding.x.scale = { type: "utc" };
      encoding.x.axis = { tickCount: 5, labelOverlap: true };
      if (dates.length <= 12) encoding.x.axis.values = dates.map(function (date) { return Date.parse(date.length === 10 ? date + "T00:00:00Z" : /(?:Z|[+-]\d{2}:\d{2})$/.test(date) ? date : date + "Z"); }).filter(Number.isFinite).sort(function (a, b) { return a - b; });
      if (dates.every(function (date) { return /^\d{4}-\d{2}-01$/.test(date); })) encoding.x.axis.format = "%b %Y";
    }
    return {
      width: "container", height: 240, autosize: { type: "fit", contains: "padding", resize: true },
      background: "#ffffff", padding: 12,
      data: { values: chart.rows.map(function (row) {
        var result = {}; names.forEach(function (name) {
          var value = row[name];
          // Interpret zone-free ISO values as UTC, consistently across browsers.
          if (chart.fields[name] === "temporal") result[keys.get(name) + "w"] = value;
          if (value != null && chart.fields[name] === "temporal") value = Date.parse(
            value.length === 10 ? value + "T00:00:00Z" : /(?:Z|[+-]\d{2}:\d{2})$/.test(value) ? value : value + "Z");
          result[keys.get(name)] = value;
        }); return result;
      }) },
      mark: { type: chart.kind === "scatter" ? "point" : chart.kind, tooltip: true,
        color: "#0369a1", opacity: chart.kind === "area" ? 0.45 : 1,
        point: chart.kind === "line", clip: true }, encoding: encoding,
      config: { font: "Manrope, system-ui, sans-serif", view: { stroke: null },
        axis: { labelColor: "#45526e", titleColor: "#16203a", gridColor: "#e8edf6",
          labelLimit: 100, titleLimit: 220, titlePadding: 12, labelFontSize: 11, titleFontSize: 12 },
        legend: { orient: "bottom", labelColor: "#45526e", titleColor: "#16203a", labelLimit: 180 } }
    };
  }
  // No model-supplied specs, expressions, links, loaders or embed options.
  function draw(container, chart) {
    return vendor.vegaEmbed(container, spec(chart), { actions: false, renderer: "svg", tooltip: true,
      loader: { load: function () { return Promise.reject(new Error("External chart data is disabled")); },
        sanitize: function () { return Promise.reject(new Error("Chart URLs are disabled")); } } });
  }
  function drawable(chart) {
    return chart && ["bar", "line", "area", "scatter"].includes(chart.kind) &&
      chart.fields && typeof chart.fields === "object" && !Array.isArray(chart.fields) &&
      Object.keys(chart.fields).length <= 16 &&
      Object.values(chart.fields).every(function (type) { return ["nominal", "quantitative", "temporal"].includes(type); }) &&
      Object.hasOwn(chart.fields, chart.x) && Object.hasOwn(chart.fields, chart.y) &&
      (!chart.color || Object.hasOwn(chart.fields, chart.color)) &&
      Array.isArray(chart.rows) && chart.rows.length > 0 && chart.rows.length <= 2000 &&
      JSON.stringify(chart).length <= 300000;
  }
  function valid(chart) {
    return chart && typeof chart.id === "string" && typeof chart.title === "string" &&
      chart.title.length <= 160 && drawable(chart);
  }
  // A bare plot for a caller that owns its own frame and lifetime. It is not
  // counted against the conversation's retained charts.
  function embed(container, chart) {
    if (!drawable(chart)) return null;
    var view, observer, disposed = false, timer, dropped = false;
    function drop(result) { if (result && !dropped) { dropped = true; result.view.finalize(); } }
    var ready = new Promise(function (resolve, reject) {
      timer = setTimeout(function () { reject(new Error("Chart timed out")); }, RENDER_TIMEOUT);
      var drawing = load().then(function () { return disposed ? null : draw(container, chart); });
      // A draw that outlives its view or the timeout still has to be released.
      drawing.then(function (result) { if (disposed) drop(result); }, function () {});
      drawing.then(resolve, reject);
    }).then(function (result) {
      clearTimeout(timer);
      // The view can be disposed between the draw completing and this handler.
      if (!result || disposed) { drop(result); return false; }
      view = result.view;
      if (window.ResizeObserver) {
        // The spec fixes the height, so only a width change needs a redraw.
        var width = container.clientWidth;
        observer = new ResizeObserver(function () {
          if (!view || container.clientWidth === width) return;
          width = container.clientWidth; view.resize().runAsync().catch(function () {});
        });
        observer.observe(container);
      }
      return true;
    }, function (error) { clearTimeout(timer); disposed = true; throw error; });
    return { ready: ready, dispose: function () {
      disposed = true; clearTimeout(timer); if (observer) observer.disconnect();
      if (view) { view.finalize(); view = null; }
    } };
  }
  function create(chart) {
    if (!valid(chart) || instances.has(chart.id)) return null;
    var figure = node("figure", "sh-agent-chart"), header = node("div", "sh-agent-chart-header");
    var title = node("figcaption", "sh-agent-chart-title", chart.title);
    var tools = node("div", "sh-agent-chart-tools");
    var expand = node("button", "sh-agent-chart-button", "Expand");
    expand.type = "button"; expand.setAttribute("aria-label", "Expand chart: " + chart.title);
    var download = node("button", "sh-agent-chart-button", "Download SVG"); download.type = "button"; download.disabled = true;
    var retry = node("button", "sh-agent-chart-button", "Retry chart"); retry.type = "button"; retry.hidden = true;
    tools.append(expand, download, retry); header.append(title, tools);
    var plot = node("div", "sh-agent-chart-plot"), status = node("p", "sh-agent-chart-status", "Loading chart…");
    status.setAttribute("role", "status"); plot.setAttribute("aria-label", chart.title);
    var meta = node("p", "sh-agent-chart-meta", (chart.source || "App data") + " · " + chart.rows.length.toLocaleString() + " rows · Snapshot");
    var details = node("details", "sh-agent-chart-data"), summary = node("summary", "", "View data");
    var scroller = node("div", "sh-agent-chart-table"), table = node("table"), caption = node("caption", "", chart.title + " — source data");
    var head = node("thead"), hr = node("tr"), fields = Object.keys(chart.fields);
    fields.forEach(function (field) { var th = node("th", "", field); th.scope = "col"; hr.append(th); });
    head.append(hr); var body = node("tbody");
    var page = 0, pages = node("div", "sh-agent-chart-pages");
    var previous = node("button", "sh-agent-chart-button", "Previous rows"), next = node("button", "sh-agent-chart-button", "Next rows");
    previous.type = next.type = "button";
    var range = node("span"); range.setAttribute("role", "status");
    function showPage() {
      body.replaceChildren();
      if (!details.open) return;
      var start = page * PAGE_SIZE, end = Math.min(start + PAGE_SIZE, chart.rows.length);
      chart.rows.slice(start, end).forEach(function (row) {
        var tr = node("tr"); fields.forEach(function (field) {
          tr.append(node("td", "", row[field] == null ? "—" : String(row[field])));
        }); body.append(tr);
      });
      range.textContent = "Rows " + (start + 1) + "–" + end + " of " + chart.rows.length;
      previous.disabled = page === 0; next.disabled = end === chart.rows.length;
      pages.hidden = chart.rows.length <= PAGE_SIZE;
    }
    previous.addEventListener("click", function () { if (page > 0) { page--; showPage(); scroller.scrollTop = 0; } });
    next.addEventListener("click", function () { if ((page + 1) * PAGE_SIZE < chart.rows.length) { page++; showPage(); scroller.scrollTop = 0; } });
    details.addEventListener("toggle", showPage);
    pages.append(previous, range, next); pages.hidden = true;
    table.append(caption, head, body); scroller.append(table); scroller.tabIndex = 0;
    scroller.setAttribute("role", "region"); scroller.setAttribute("aria-label", "Chart source data");
    details.append(summary, scroller, pages); figure.append(header, plot, status, meta, details);
    var view, observer, disposed = false, dialog, placeholder, attempt = 0, renderTimer;
    function resize() { if (view && !disposed) view.height(dialog ? Math.max(240, Math.min(520, window.innerHeight - 260)) : 240).resize().runAsync().catch(function () {}); }
    function restore() {
      if (!dialog) return;
      placeholder.replaceWith(figure); dialog.remove(); dialog = null;
      figure.classList.remove("sh-agent-chart-expanded"); expand.textContent = "Expand";
      expand.setAttribute("aria-label", "Expand chart: " + chart.title); expand.focus(); resize();
    }
    expand.addEventListener("click", function () {
      if (dialog) { dialog.close(); return; }
      dialog = node("dialog", "sh-agent-chart-dialog"); placeholder = document.createComment("chart");
      figure.replaceWith(placeholder); document.body.append(dialog); dialog.append(figure);
      figure.classList.add("sh-agent-chart-expanded"); expand.textContent = "Close expanded chart";
      expand.setAttribute("aria-label", "Close expanded chart");
      dialog.setAttribute("aria-label", chart.title); dialog.addEventListener("close", restore);
      dialog.showModal(); expand.focus(); resize();
    });
    download.addEventListener("click", function () {
      if (!view) return;
      download.disabled = true;
      view.toSVG().then(function (svg) {
        if (disposed) return;
        var url = URL.createObjectURL(new Blob([svg], { type: "image/svg+xml" }));
        var a = node("a"); a.href = url; a.download = "chart.svg"; a.click();
        setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
      }).catch(function () { status.hidden = false; status.textContent = "Download failed. Try again or use View data."; })
        .finally(function () { download.disabled = disposed; });
    });
    instances.set(chart.id, { element: figure, expire: function () { restore(); this.dispose(); }, owns: function (root) { return root.contains(figure) || (placeholder && root.contains(placeholder)); }, dispose: function () {
      disposed = true; attempt++; clearTimeout(renderTimer); if (observer) observer.disconnect(); if (view) view.finalize();
      if (dialog) { dialog.remove(); dialog = null; }
    } });
    while (instances.size > MAX_RETAINED) {
      var oldest = instances.entries().next().value, expired = node("p", "sh-agent-chart-meta",
        "Earlier chart cleared to keep this conversation responsive. Ask to recreate it.");
      var oldFigure = oldest[1].element;
      // Restore first so the expiry message stays in the conversation, even if expanded.
      oldest[1].expire(); oldFigure.replaceWith(expired); instances.delete(oldest[0]);
    }
    function render() {
      var token = ++attempt;
      retry.hidden = true; status.hidden = false; status.textContent = "Loading chart…";
      function failed() {
        if (disposed || token !== attempt) return;
        attempt++; clearTimeout(renderTimer); retry.hidden = false;
        status.textContent = "Chart could not load. View data to inspect the values.";
        plot.replaceChildren(); details.open = true; showPage();
      }
      renderTimer = setTimeout(failed, RENDER_TIMEOUT);
      load().then(function () {
        if (disposed || token !== attempt) return;
        var renderPlot = node("div"); plot.append(renderPlot);
        return draw(renderPlot, chart);
      }).then(function (result) {
        if (!result) return;
        if (disposed || token !== attempt) { result.view.finalize(); return; }
        clearTimeout(renderTimer); view = result.view; status.hidden = true; download.disabled = false;
        if (window.ResizeObserver) { observer = new ResizeObserver(resize); observer.observe(plot); }
      }).catch(failed);
    }
    retry.addEventListener("click", function () { plot.replaceChildren(); render(); });
    render();
    return figure;
  }
  window.shinyhubAgentCharts = { create: create, embed: embed, dispose: function (root) {
    instances.forEach(function (instance, id) {
      if (instance.owns(root) || !instance.element.isConnected) {
        instance.dispose(); instances.delete(id);
      }
    });
  } };
})();
