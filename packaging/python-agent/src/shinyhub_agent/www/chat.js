(function () {
  "use strict";

  var VERSION = 1;
  var chatSession = null;
  var activeRequest = null;
  var activeAnswer = null;
  var answerSource = "";
  var answerFrame = null;
  var approval = null;
  var lastApprovalCard = null;
  var installed = false;
  var previousFocus = null;
  var pendingTools = [];
  var undoRequests = new Map();
  var toolbarAvailable = false;
  var toolbarSuspended = false;
  var nativeChat = false;

  function announceChat(name, detail) {
    if (typeof window.CustomEvent !== "function") return;
    window.dispatchEvent(new window.CustomEvent("shinyhub:chat:" + name, {
      detail: Object.assign({ version: VERSION }, detail)
    }));
  }

  function make(tag, className, text) {
    var element = document.createElement(tag);
    if (className) element.className = className;
    if (text !== undefined) element.textContent = text;
    return element;
  }

  // A deliberately bounded Markdown dialect. All tags and attributes originate
  // here; untrusted content is only ever placed in text nodes. No HTML parsing,
  // URL handling, or syntax highlighting occurs, even inside code or tables.
  function literalEnd(source, start) {
    if (source[start] === "<" && /^<(?:\/?[A-Za-z][A-Za-z0-9-]*(?:\s|\/?>|$)|[!?]|[A-Za-z][A-Za-z0-9+.-]*:|[^<>\s]*@)/.test(source.slice(start))) {
      var end = source.indexOf(">", start + 1);
      return end < 0 ? source.length : end + 1;
    }
    if (source[start] !== "[" && source[start] !== "!") return start;
    var rest = source.slice(start);
    var link = /^!?\[[^\]\n]*\](?:\([^\n]*?\)|\[[^\]\n]*\])/.exec(rest);
    return link ? start + link[0].length : start;
  }

  function inlineMarkdown(parent, source, depth) {
    depth = depth || 0;
    var text = "";
    function flush() {
      if (text) text.split("\n").forEach(function (line, index) {
        if (index) parent.append(make("br"));
        if (line) parent.append(document.createTextNode(line));
      });
      text = "";
    }
    for (var i = 0; i < source.length;) {
      var char = source[i];
      if (char === "\\" && /[!"#$%&'()*+,\-./:;<=>?@[\\\]^_`{|}~]/.test(source[i + 1] || "")) {
        text += source[i + 1]; i += 2; continue;
      }
      var literal = literalEnd(source, i);
      if (literal > i) {
        text += source.slice(i, literal); i = literal; continue;
      }
      if (char === "\n") { flush(); parent.append(make("br")); i++; continue; }
      if (char === "`") {
        var ticks = /^`+/.exec(source.slice(i))[0];
        var close = i + ticks.length;
        while ((close = source.indexOf(ticks, close)) >= 0) {
          if (source[close - 1] !== "`" && source[close + ticks.length] !== "`") break;
          close += ticks.length;
        }
        if (close >= 0) {
          flush();
          parent.append(make("code", "", source.slice(i + ticks.length, close)));
          i = close + ticks.length; continue;
        }
        text += ticks; i += ticks.length; continue;
      }
      if (char === "*" && depth < 4) {
        var marker = /^\*+/.exec(source.slice(i))[0];
        var count = marker.length;
        var finish = -1;
        if (count <= 3 && /\S/.test(source[i + count] || "")) {
          var nestedMarkers = [];
          for (var j = i + count; j < source.length;) {
            if (source[j] === "\\") { j += 2; continue; }
            var opaque = literalEnd(source, j);
            if (opaque > j) { j = opaque; continue; }
            if (source[j] === "`") {
              var run = /^`+/.exec(source.slice(j))[0];
              var endCode = source.indexOf(run, j + run.length);
              j = endCode < 0 ? j + run.length : endCode + run.length; continue;
            }
            if (source[j] === "*") {
              var stars = /^\*+/.exec(source.slice(j))[0];
              var remaining = stars.length, consumed = 0;
              if (/\S/.test(source[j - 1])) {
                while (nestedMarkers.length && remaining >= nestedMarkers[nestedMarkers.length - 1]) {
                  var matched = nestedMarkers.pop(); remaining -= matched; consumed += matched;
                }
                if (!nestedMarkers.length && remaining === count) { finish = j + consumed; break; }
              }
              if (remaining <= 3 && remaining && /\S/.test(source[j + stars.length] || "")) nestedMarkers.push(remaining);
              j += stars.length;
            } else j++;
          }
        }
        if (finish >= 0) {
          flush();
          var emphasis = make(count === 1 ? "em" : "strong");
          var content = count === 3 ? make("em") : emphasis;
          inlineMarkdown(content, source.slice(i + count, finish), depth + 1);
          if (count === 3) emphasis.append(content);
          parent.append(emphasis);
          i = finish + count; continue;
        }
        text += marker; i += count; continue;
      }
      text += char; i++;
    }
    flush();
  }

  function tableCells(line) {
    var cells = [], cell = "", pipes = 0;
    for (var i = 0; i < line.length; i++) {
      if (line[i] === "\\" && i + 1 < line.length) {
        // Pipe escaping belongs to table structure, including inside code spans.
        cell += line[i + 1] === "|" ? line[++i] : line[i] + line[++i];
      }
      else if (line[i] === "|") { cells.push(cell.trim()); cell = ""; pipes++; }
      else cell += line[i];
    }
    if (!pipes) return null;
    cells.push(cell.trim());
    if (/^\s*\|/.test(line)) cells.shift();
    if (/\|\s*$/.test(line) && cells[cells.length - 1] === "") cells.pop();
    return cells;
  }

  function listItem(line) {
    var match = /^( *)([-+*]|\d{1,9}\.)[ \t]+(.*)$/.exec(line);
    if (!match || /^\[[ xX]\](?:\s|$)/.test(match[3])) return null;
    return { indent: match[1].length, ordered: /\d/.test(match[2]),
      number: parseInt(match[2], 10), text: match[3] };
  }

  function markdownTree(source, final) {
    var root = document.createDocumentFragment();
    var lines = source.replace(/\r\n?/g, "\n").split("\n");
    function complete(index) { return final || index < lines.length - 1; }
    function paragraph(text, className) {
      var node = make("p", className || "");
      inlineMarkdown(node, text); return node;
    }
    function fence(index) {
      return complete(index) && /^ {0,3}(`{3,}|~{3,})([^`]*)$/.exec(lines[index]);
    }
    function heading(index) { return complete(index) && /^ {0,3}#{1,6}[ \t]+(.+?)(?:[ \t]+#+)?[ \t]*$/.exec(lines[index]); }
    function table(index) {
      if (index + 1 >= lines.length || !complete(index + 1)) return null;
      var headers = tableCells(lines[index]), separators = tableCells(lines[index + 1]);
      if (!headers || !separators || !headers.length || headers.length !== separators.length ||
          !separators.every(function (cell) { return /^:?-{3,}:?$/.test(cell); })) return null;
      return { headers: headers, alignment: separators.map(function (cell) {
        return cell[0] === ":" ? (cell.endsWith(":") ? "center" : "left") : (cell.endsWith(":") ? "right" : "left");
      }) };
    }
    function parseList(start, indent, depth) {
      var first = listItem(lines[start]);
      var list = make(first.ordered ? "ol" : "ul");
      if (first.ordered && first.number !== 1) list.setAttribute("start", first.number);
      var i = start, item = null;
      while (i < lines.length) {
        if (!lines[i].trim()) {
          var next = i;
          while (next < lines.length && !lines[next].trim()) next++;
          var continuation = next < lines.length && listItem(lines[next]);
          if (!continuation || !(continuation.indent === indent && continuation.ordered === first.ordered ||
              continuation.indent > indent && item)) break;
          i = next;
        }
        var marker = listItem(lines[i]);
        if (marker && marker.indent === indent && marker.ordered === first.ordered) {
          item = make("li");
          inlineMarkdown(item, marker.text); list.append(item); i++;
        } else if (marker && marker.indent > indent && depth < 2 && item) {
          var nested = parseList(i, marker.indent, depth + 1);
          item.append(nested.node); i = nested.end;
        } else if (marker && marker.indent > indent && depth === 2 && item) {
          item.append(make("br"), document.createTextNode(lines[i++]));
        } else if (!marker && item && /^ +\S/.test(lines[i]) &&
                   lines[i].search(/\S/) > indent && !/^\s*[-+*] /.test(lines[i])) {
          item.append(make("br")); inlineMarkdown(item, lines[i].trimStart()); i++;
        } else break;
      }
      return { node: list, end: i };
    }
    for (var i = 0; i < lines.length;) {
      if (!lines[i].trim()) { i++; continue; }
      var opening = fence(i);
      if (opening) {
        var code = [], marker = opening[1], closed = false;
        i++;
        while (i < lines.length) {
          var closing = /^ {0,3}(`+|~+)[ \t]*$/.exec(lines[i]);
          if (closing && complete(i) && closing[1][0] === marker[0] && closing[1].length >= marker.length) {
            closed = true; i++; break;
          }
          code.push(lines[i++]);
        }
        var pre = make("pre", "sh-agent-code-block");
        pre.tabIndex = 0;
        pre.setAttribute("aria-label", "Code block");
        pre.append(make("code", "", code.join("\n") + (closed && code.length ? "\n" : "")));
        root.append(pre); continue;
      }
      var title = heading(i);
      if (title) { root.append(paragraph(title[1], "sh-agent-markdown-heading")); i++; continue; }
      var spec = table(i);
      if (spec) {
        var wrapper = make("div", "sh-agent-table-scroll");
        wrapper.tabIndex = 0;
        wrapper.setAttribute("role", "region");
        wrapper.setAttribute("aria-label", "Answer table");
        var grid = make("table"), head = make("thead"), body = make("tbody");
        function row(cells, header) {
          var tr = make("tr");
          cells.forEach(function (value, column) {
            var td = make(header ? "th" : "td", "sh-agent-align-" + spec.alignment[column]);
            if (header) td.setAttribute("scope", "col");
            inlineMarkdown(td, value);
            // Wrap prose, but keep a formatted measurement together.
            if (!header && /^\(?[+−-]?(?:[$€£¥]\s*)?\d[\d,.]*(?:\s?(?:%|[KMBT]|USD|EUR|GBP))?\)?$/i.test(td.textContent.trim())) {
              td.classList.add("sh-agent-cell-value");
            } else if (!header && td.textContent.length > 32) {
              td.classList.add("sh-agent-cell-description");
            }
            tr.append(td);
          });
          return tr;
        }
        head.append(row(spec.headers, true)); i += 2;
        while (i < lines.length && complete(i)) {
          var followingList = listItem(lines[i]);
          if (fence(i) || heading(i) || followingList && followingList.indent <= 3 ||
              /^ {0,3}(?:>|[-+*][ \t]+\[[ xX]\](?:\s|$))/.test(lines[i])) break;
          var cells = tableCells(lines[i]);
          // Never silently drop a model-produced value or invent an empty cell.
          if (!cells || cells.length !== spec.headers.length) break;
          body.append(row(cells, false)); i++;
        }
        grid.append(head, body); wrapper.append(grid); root.append(wrapper); continue;
      }
      var bullet = listItem(lines[i]);
      if (bullet && bullet.indent <= 3) {
        var parsed = parseList(i, bullet.indent, 1);
        root.append(parsed.node); i = parsed.end; continue;
      }
      var content = [lines[i++]];
      while (i < lines.length && lines[i].trim() && !fence(i) && !heading(i) && !table(i) &&
             !(listItem(lines[i]) && listItem(lines[i]).indent <= 3)) content.push(lines[i++]);
      root.append(paragraph(content.join("\n")));
    }
    return root;
  }

  // Reconcile fixed structural nodes rather than replacing the answer on every
  // frame. In particular table scroll containers, focus and completed rows stay.
  function reconcile(parent, desired) {
    var children = Array.from(desired.childNodes);
    children.forEach(function (next, index) {
      var current = parent.childNodes[index];
      if (!current || current.nodeName !== next.nodeName) {
        if (current) parent.replaceChild(next, current);
        else parent.append(next);
      } else if (next.nodeType === 3) {
        if (current.nodeValue !== next.nodeValue) current.nodeValue = next.nodeValue;
      } else {
        Array.from(current.attributes).forEach(function (attr) {
          if (!next.hasAttribute(attr.name)) current.removeAttribute(attr.name);
        });
        Array.from(next.attributes).forEach(function (attr) {
          if (current.getAttribute(attr.name) !== attr.value) current.setAttribute(attr.name, attr.value);
        });
        reconcile(current, next);
      }
    });
    while (parent.childNodes.length > children.length) parent.lastChild.remove();
  }

  function selectedAnswer(body) {
    var selection = window.getSelection();
    if (!selection || !selection.rangeCount || selection.isCollapsed ||
        !body.contains(selection.anchorNode) || !body.contains(selection.focusNode)) return null;
    var range = selection.getRangeAt(0), before = range.cloneRange();
    before.selectNodeContents(body); before.setEnd(range.startContainer, range.startOffset);
    return { text: selection.toString(), start: before.toString().length,
      backward: selection.anchorNode === range.endContainer && selection.anchorOffset === range.endOffset };
  }

  function restoreSelection(body, saved) {
    if (!saved) return;
    var text = body.textContent, start = saved.start;
    if (text.slice(start, start + saved.text.length) !== saved.text) start = text.indexOf(saved.text);
    if (start < 0) return;
    var walker = document.createTreeWalker(body, 4), node, offset = 0, anchor, focus;
    while ((node = walker.nextNode())) {
      var end = offset + node.nodeValue.length;
      if (!anchor && start <= end) anchor = { node: node, offset: start - offset };
      if (start + saved.text.length <= end) {
        focus = { node: node, offset: start + saved.text.length - offset }; break;
      }
      offset = end;
    }
    if (anchor && focus) {
      var selection = window.getSelection(), first = saved.backward ? focus : anchor, last = saved.backward ? anchor : focus;
      selection.setBaseAndExtent(first.node, first.offset, last.node, last.offset);
    }
  }

  function renderAnswer(final) {
    if (!activeAnswer) return;
    var follow = nearBottom(), selected = selectedAnswer(activeAnswer);
    reconcile(activeAnswer, markdownTree(answerSource, final));
    restoreSelection(activeAnswer, selected);
    scrollAfterChange(follow);
  }

  function cancelAnswerFrame() {
    if (answerFrame !== null) window.cancelAnimationFrame(answerFrame);
    answerFrame = null;
  }

  function scheduleAnswer() {
    if (answerFrame !== null) return;
    answerFrame = window.requestAnimationFrame(function () {
      answerFrame = null; renderAnswer(false);
    });
  }

  function spokenAnswer(node) {
    if (node.nodeType === 3) return node.nodeValue;
    if (node.nodeName === "BR") return "\n";
    var text = Array.from(node.childNodes).map(spokenAnswer).join("");
    if (/^(TH|TD)$/.test(node.nodeName)) return text + "; ";
    if (/^(P|LI|TR|PRE)$/.test(node.nodeName)) return text + "\n";
    return text;
  }

  function icon(paths) {
    var svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("width", "18");
    svg.setAttribute("height", "18");
    svg.setAttribute("fill", "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "1.8");
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");
    svg.setAttribute("aria-hidden", "true");
    paths.forEach(function (path) {
      var part = document.createElementNS("http://www.w3.org/2000/svg", "path");
      part.setAttribute("d", path);
      svg.append(part);
    });
    return svg;
  }

  var sparkle = ["M12 2 14.1 8.1 20 10 14.1 11.9 12 18 9.9 11.9 4 10 9.9 8.1 12 2Z", "M19 17v4", "M17 19h4"];

  var launcher = make("button", "sh-agent-launcher");
  launcher.type = "button";
  launcher.hidden = true;
  launcher.append(icon(sparkle), make("span", "", "Ask this app"));
  launcher.setAttribute("aria-controls", "shinyhub-agent-chat-panel");
  launcher.setAttribute("aria-expanded", "false");
  // A section supports the mobile dialog role and keeps its header local.
  var panel = make("section", "sh-agent-panel");
  panel.id = "shinyhub-agent-chat-panel";
  panel.hidden = true;
  panel.setAttribute("aria-labelledby", "shinyhub-agent-chat-heading");
  var header = make("header", "sh-agent-header");
  var brand = make("div", "sh-agent-brand");
  var emblem = make("span", "sh-agent-emblem");
  emblem.append(icon(sparkle));
  var titles = make("div", "sh-agent-titles");
  var heading = make("h2", "", "Ask this app");
  heading.id = "shinyhub-agent-chat-heading";
  titles.append(heading, make("p", "", "Explore the current view"));
  brand.append(emblem, titles);
  var close = make("button", "sh-agent-icon-button");
  close.type = "button";
  close.setAttribute("aria-label", "Close assistant");
  close.append(icon(["M5 5l14 14", "M19 5 5 19"]));
  var expand = make("button", "sh-agent-icon-button sh-agent-expand");
  expand.type = "button";
  expand.append(icon(["M8 3H3v5", "m3 3 6 6", "M16 21h5v-5", "m21 21-6-6"]));
  var headerActions = make("div", "sh-agent-header-actions");
  headerActions.append(expand, close);
  header.append(brand, headerActions);
  var resizeGrip = make("div", "sh-agent-resize");
  var logWrap = make("div", "sh-agent-log-wrap");
  var log = make("div", "sh-agent-log");
  log.setAttribute("role", "log");
  log.setAttribute("aria-label", "Conversation");
  log.setAttribute("aria-live", "off");
  var empty = make("div", "sh-agent-empty");
  var emptyIcon = make("span", "sh-agent-empty-icon");
  emptyIcon.append(icon(sparkle));
  empty.append(emptyIcon, make("h3", "", "Start with this view"),
    make("p", "", "Ask about what you see, or find out which changes this app can make."));
  var suggestions = make("div", "sh-agent-suggestions");
  ["What can you tell me about this view?", "What can I change here?"].forEach(function (question) {
    var suggestion = make("button", "sh-agent-suggestion", question);
    suggestion.type = "button";
    suggestion.addEventListener("click", function () {
      input.value = question;
      updateSend();
      composer.requestSubmit();
    });
    suggestions.append(suggestion);
  });
  empty.append(suggestions);
  log.append(empty);
  var jump = make("button", "sh-agent-jump", "Latest messages");
  jump.type = "button";
  jump.hidden = true;
  logWrap.append(log, jump);
  var status = make("div", "sh-agent-status");
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");
  status.hidden = true;
  status.append(make("span", "sh-agent-status-dot"), make("span", "sh-agent-status-text"));
  // The log stays silent while an answer streams, so screen readers do not
  // announce every fragment; the finished answer is announced once from here.
  var announcer = make("div", "sh-agent-sr-only");
  announcer.setAttribute("aria-live", "polite");
  announcer.setAttribute("aria-atomic", "true");
  var composer = make("form", "sh-agent-composer");
  var input = make("textarea", "sh-agent-input");
  input.rows = 2;
  input.maxLength = 2000;
  input.placeholder = "Ask about this app…";
  input.setAttribute("aria-label", "Question for the app assistant");
  var controls = make("div", "sh-agent-controls");
  var reset = make("button", "sh-agent-reset", "New chat");
  reset.type = "button";
  var stop = make("button", "sh-agent-secondary", "Stop");
  stop.type = "button";
  stop.hidden = true;
  var send = make("button", "sh-agent-send");
  send.type = "submit";
  send.append(icon(["M12 19V5", "m6 11 6-6 6 6"]));
  send.setAttribute("aria-label", "Send question");
  var hint = make("span", "sh-agent-hint", "Enter to send · Shift+Enter for a new line");
  controls.append(reset, hint, stop, send);
  composer.append(input, controls);
  panel.append(header, resizeGrip, logWrap, status, announcer, composer);
  document.body.append(launcher, panel);

  function emit(payload) {
    window.Shiny.setInputValue(".shinyhub_agent_chat_request", payload, { priority: "event" });
  }

  function appendMessage(role, value) {
    var follow = nearBottom();
    if (empty.isConnected) empty.remove();
    var item = make("div", "sh-agent-message sh-agent-" + role);
    item.append(make("strong", "sh-agent-speaker", role === "user" ? "You" : "Assistant"));
    var body = make("div", "sh-agent-message-body", value);
    item.append(body);
    log.append(item);
    scrollAfterChange(follow);
    return body;
  }

  function nearBottom() {
    return log.scrollHeight - log.scrollTop - log.clientHeight < 90;
  }

  function scrollAfterChange(follow) {
    if (follow) log.scrollTop = log.scrollHeight;
    jump.hidden = nearBottom();
  }

  function updateSend() {
    send.disabled = !chatSession || !!activeRequest || !input.value.trim();
  }

  function setStatus(message, mode) {
    status.hidden = !message;
    status.querySelector(".sh-agent-status-text").textContent = message || "";
    status.dataset.mode = mode || "working";
  }

  function setBusy(busy) {
    reset.disabled = busy;
    announceChat("busy", { busy: busy });
    stop.hidden = !busy;
    stop.disabled = false;
    updateSend();
    panel.querySelectorAll(".sh-agent-undo").forEach(function (button) {
      if (!button.dataset.pending) button.disabled = busy;
    });
    if (!busy && !panel.hidden) input.focus({ preventScroll: true });
  }

  function openPanel() {
    previousFocus = document.activeElement;
    panel.hidden = false;
    launcher.setAttribute("aria-expanded", "true");
    announceChat("state", { open: true });
    setPanelRole();
    input.focus({ preventScroll: true });
  }

  function setPanelRole() {
    if (nativeChat) {
      panel.setAttribute("role", "region");
      panel.setAttribute("aria-label", "Conversation");
      panel.removeAttribute("aria-labelledby");
      panel.removeAttribute("aria-modal");
    } else if (matchMedia("(max-width: 680px)").matches) {
      panel.setAttribute("role", "dialog");
      panel.setAttribute("aria-modal", "true");
    } else {
      panel.setAttribute("role", "complementary");
      panel.removeAttribute("aria-modal");
    }
  }

  function closePanel() {
    panel.hidden = true;
    launcher.setAttribute("aria-expanded", "false");
    announceChat("state", { open: false, focus: toolbarAvailable });
    if (!toolbarAvailable && !toolbarSuspended) {
      if (previousFocus && previousFocus.isConnected) previousFocus.focus({ preventScroll: true });
      else launcher.focus({ preventScroll: true });
    }
  }

  function clearApproval() {
    if (approval) approval.remove();
    approval = null;
  }

  function finish(message) {
    var follow = nearBottom();
    cancelAnswerFrame();
    renderAnswer(true);
    clearApproval();
    if (message && lastApprovalCard && lastApprovalCard.dataset.result === "applying") {
      lastApprovalCard.dataset.result = "unknown";
      lastApprovalCard.querySelector(".sh-agent-approval-label").textContent = "Check the current view";
    }
    announcer.textContent = activeAnswer && !message && answerSource.trim()
      ? "Answer: " + spokenAnswer(activeAnswer).trim() : "";
    if (activeAnswer && answerSource.trim() && !message && navigator.clipboard) {
      var source = answerSource;
      var copy = make("button", "sh-agent-copy", "Copy answer");
      copy.type = "button";
      copy.addEventListener("click", function () {
        navigator.clipboard.writeText(source).then(function () {
          copy.textContent = "Copied";
          setTimeout(function () { copy.textContent = "Copy answer"; }, 1800);
        }).catch(function () { copy.textContent = "Copy failed · Retry"; });
      });
      activeAnswer.parentNode.append(copy);
    }
    if (message && activeAnswer) activeAnswer.parentNode.append(make("p", "sh-agent-error", message));
    activeRequest = null;
    activeAnswer = null;
    answerSource = "";
    pendingTools = [];
    setBusy(false);
    setStatus(message ? "Answer interrupted" : "", message ? "error" : "working");
    scrollAfterChange(follow);
  }

  function toolLabel(name) {
    return String(name || "App tool").replace(/[_-]+/g, " ").replace(/\b\w/g, function (letter) {
      return letter.toUpperCase();
    });
  }

  function showTool(event) {
    var follow = nearBottom();
    var item = make("details", "sh-agent-tool");
    var summary = make("summary", "sh-agent-tool-summary");
    var state = make("span", "sh-agent-tool-state", event.readOnly ? "Reading" : "Preparing");
    summary.append(make("span", "sh-agent-tool-dot"),
      make("span", "sh-agent-tool-name", toolLabel(event.name)), state);
    item.append(summary);
    if (event.description) item.append(make("p", "sh-agent-tool-detail", event.description));
    if (activeAnswer) activeAnswer.parentNode.before(item);
    else log.append(item);
    pendingTools.push({ name: event.name, item: item, state: state });
    scrollAfterChange(follow);
  }

  function finishTool(event) {
    var index = pendingTools.findIndex(function (entry) { return entry.name === event.name; });
    if (index < 0) return;
    var entry = pendingTools.splice(index, 1)[0];
    entry.item.dataset.result = event.ok ? "done" : "error";
    entry.state.textContent = event.ok ? "Done" : "Unavailable";
    if (event.ok) return;
    // A write tool that fails ends its approval without ending the answer, so
    // the card has to be settled here rather than by finish().
    if (approval && approval.dataset.tool === event.name) {
      approval.querySelector(".sh-agent-approval-actions").remove();
      approval.dataset.result = "expired";
      approval.querySelector(".sh-agent-approval-label").textContent = "Not approved in time";
      lastApprovalCard = approval;
      approval = null;
    } else if (lastApprovalCard && lastApprovalCard.dataset.result === "applying" &&
        lastApprovalCard.dataset.tool === event.name) {
      lastApprovalCard.dataset.result = "failed";
      lastApprovalCard.querySelector(".sh-agent-approval-label").textContent = "Change failed";
    }
  }

  function showApproval(event) {
    clearApproval();
    lastApprovalCard = null;
    var follow = nearBottom();
    approval = make("section", "sh-agent-approval");
    approval.dataset.tool = event.name || "";
    approval.append(make("span", "sh-agent-approval-label", "Approval needed"));
    approval.append(make("h3", "", event.message || "Apply this change to the app?"));
    var values = Object.entries(event.arguments || {});
    if (values.length) {
      var details = make("dl", "sh-agent-approval-values");
      values.forEach(function (entry) {
        details.append(make("dt", "", toolLabel(entry[0])), make("dd", "", String(entry[1])));
      });
      approval.append(details);
    }
    var actions = make("div", "sh-agent-approval-actions");
    var decline = make("button", "sh-agent-secondary", "Keep current view");
    var approve = make("button", "sh-agent-apply", "Apply change");
    [decline, approve].forEach(function (button) { button.type = "button"; });
    function decide(approved) {
      window.Shiny.setInputValue(".shinyhub_agent_chat_decision", {
        version: VERSION, session: chatSession, approvalId: event.approvalId,
        approved: approved
      }, { priority: "event" });
      actions.remove();
      approval.dataset.result = approved ? "applying" : "declined";
      approval.querySelector(".sh-agent-approval-label").textContent = approved ? "Applying change" : "Change declined";
      lastApprovalCard = approval;
      approval = null;
      setStatus(approved ? "Applying change to this view" : "Continuing without the change");
    }
    decline.addEventListener("click", function () { decide(false); });
    approve.addEventListener("click", function () { decide(true); });
    actions.append(decline, approve);
    approval.append(actions);
    if (activeAnswer) activeAnswer.parentNode.before(approval);
    else log.append(approval);
    scrollAfterChange(follow);
    approve.focus({ preventScroll: true });
    setStatus("Waiting for your approval");
  }

  function showReceipt(event) {
    var follow = nearBottom();
    var receipt = lastApprovalCard && lastApprovalCard.dataset.result === "applying"
      ? lastApprovalCard : make("div", "sh-agent-receipt");
    receipt.className = "sh-agent-receipt";
    receipt.removeAttribute("data-result");
    var mark = make("span", "sh-agent-receipt-mark");
    mark.append(icon(["M5 12l4 4L19 6"]));
    var copy = make("span", "sh-agent-receipt-copy");
    var title = make("strong", "sh-agent-receipt-title", event.receipt || "Change applied to this view");
    var detail = make("span", "sh-agent-receipt-detail");
    detail.hidden = true;
    copy.append(title, detail);
    receipt.replaceChildren(mark, copy);
    var toolIndex = pendingTools.findIndex(function (entry) { return entry.name === event.name; });
    if (toolIndex >= 0) pendingTools.splice(toolIndex, 1)[0].item.remove();
    panel.querySelectorAll(".sh-agent-undo").forEach(function (button) { button.remove(); });
    if (typeof event.actionId === "string") {
      var undo = make("button", "sh-agent-undo", "Undo");
      undo.type = "button";
      undo.disabled = !!activeRequest;
      undo.setAttribute("aria-label", "Undo: " + title.textContent);
      undo.addEventListener("click", function () {
        var requestId = crypto.randomUUID();
        undo.disabled = true;
        undo.dataset.pending = "true";
        undo.textContent = "Undoing…";
        undoRequests.set(requestId, { receipt: receipt, title: title, detail: detail, button: undo });
        emit({ version: VERSION, session: chatSession, requestId: requestId,
          action: "undo", actionId: event.actionId });
      });
      receipt.append(undo);
    }
    if (!receipt.isConnected && activeAnswer) activeAnswer.parentNode.before(receipt);
    lastApprovalCard = null;
    scrollAfterChange(follow);
  }

  // Both standalone bundles implement this small sizing contract so either
  // panel works with older hosts/helpers. Browser tests exercise their parity.
  function installChatSizing(surface, expandButton, grip, mobileWidth, margin, changing, unavailable) {
    var key = "shinyhub:chat:size";
    var width = 440, expanded = false, drag = null;
    function load() {
      try {
        var saved = JSON.parse(window.sessionStorage.getItem(key));
        if (saved && typeof saved.width === "number" && isFinite(saved.width) &&
            saved.width >= 360 && saved.width <= 960) {
          width = saved.width; expanded = saved.expanded === true;
        }
      } catch (e) { /* Storage is optional in embedded apps and privacy modes. */ }
    }
    load();

    grip.tabIndex = 0;
    grip.setAttribute("role", "separator");
    grip.setAttribute("aria-orientation", "vertical");
    grip.setAttribute("aria-label", "Resize assistant");
    grip.setAttribute("aria-controls", surface.id);
    grip.title = "Drag to resize. Left/Right arrows adjust width; Home/End choose the smallest/largest width.";

    function maximum() { return Math.min(960, window.innerWidth - 2 * margin()); }
    function clamp(value) { return Math.round(Math.max(360, Math.min(maximum(), value))); }
    function save() {
      try { window.sessionStorage.setItem(key, JSON.stringify({ width: width, expanded: expanded })); }
      catch (e) { /* Resizing still works without storage. */ }
    }
    function update() {
      var mobile = window.innerWidth <= mobileWidth || unavailable && unavailable();
      changing(true);
      var actual = mobile ? window.innerWidth : clamp(expanded ? maximum() : width);
      surface.style.setProperty("--sh-chat-width", actual + "px");
      var focused = surface.getRootNode().activeElement;
      var moveFocus = mobile && (focused === grip || focused === expandButton);
      grip.hidden = expandButton.hidden = mobile;
      grip.setAttribute("aria-valuemin", "360");
      grip.setAttribute("aria-valuemax", String(Math.max(360, maximum())));
      grip.setAttribute("aria-valuenow", String(actual));
      grip.setAttribute("aria-valuetext", actual + " pixels wide");
      var label = expanded ? "Restore assistant width" : "Expand assistant";
      expandButton.setAttribute("aria-label", label);
      expandButton.setAttribute("aria-pressed", String(expanded));
      expandButton.title = label;
      var paths = expandButton.querySelectorAll("path");
      var drawing = expanded ? ["M3 8h5V3", "m3 3 5 5", "M21 16h-5v5", "m21 21-5-5"] :
        ["M8 3H3v5", "m3 3 6 6", "M16 21h5v-5", "m21 21-6-6"];
      for (var i = 0; i < paths.length; i++) paths[i].setAttribute("d", drawing[i]);
      changing(false, moveFocus);
    }
    function finish(cancel) {
      if (!drag) return;
      var previous = drag; drag = null;
      if (cancel) { width = previous.width; expanded = previous.expanded; }
      surface.classList.remove("chat-resizing");
      if (typeof grip.hasPointerCapture === "function" && grip.hasPointerCapture(previous.id)) {
        grip.releasePointerCapture(previous.id);
      }
      update();
      if (!cancel) save();
    }
    expandButton.addEventListener("click", function () {
      expanded = !expanded; update(); save();
    });
    grip.addEventListener("pointerdown", function (event) {
      if (event.button !== 0 || grip.hidden || drag) return;
      event.preventDefault();
      grip.focus({ preventScroll: true });
      drag = { id: event.pointerId, x: event.clientX,
        actual: clamp(expanded ? maximum() : width), width: width, expanded: expanded };
      surface.classList.add("chat-resizing");
      if (typeof grip.setPointerCapture === "function") grip.setPointerCapture(event.pointerId);
    });
    grip.addEventListener("pointermove", function (event) {
      if (!drag || drag.id !== event.pointerId) return;
      width = clamp(drag.actual + drag.x - event.clientX);
      expanded = false; update();
    });
    grip.addEventListener("pointerup", function (event) {
      if (drag && drag.id === event.pointerId) finish(false);
    });
    grip.addEventListener("pointercancel", function (event) {
      if (drag && drag.id === event.pointerId) finish(true);
    });
    grip.addEventListener("lostpointercapture", function () { finish(false); });
    grip.addEventListener("keydown", function (event) {
      if (event.key === "Escape" && drag) {
        event.preventDefault(); event.stopPropagation(); finish(true); return;
      }
      if (grip.hidden ||
          ["ArrowLeft", "ArrowRight", "Home", "End"].indexOf(event.key) < 0) return;
      event.preventDefault(); event.stopPropagation();
      var step = event.shiftKey ? 64 : 16;
      width = event.key === "Home" ? 360 : event.key === "End" ? maximum() :
        clamp(expanded ? maximum() : width) + (event.key === "ArrowLeft" ? step : -step);
      width = clamp(width); expanded = false; update(); save();
    });
    window.addEventListener("blur", function () { finish(true); });
    window.addEventListener("resize", function () { finish(true); update(); });
    update();
    return function () { load(); update(); };
  }

  var sizingFollow = false;
  var updateSizing = installChatSizing(panel, expand, resizeGrip, 680, function () {
    return toolbarAvailable ? 12 : 24;
  }, function (before, moveFocus) {
    if (nativeChat) return;
    if (before) sizingFollow = nearBottom();
    else {
      if (!panel.hidden) setPanelRole();
      scrollAfterChange(sizingFollow);
      if (moveFocus && !panel.hidden) input.focus({ preventScroll: true });
    }
  }, function () { return nativeChat; });
  window.addEventListener("shinyhub:chat:layout", function (event) {
    if (!nativeChat || !event.detail || event.detail.version !== VERSION) return;
    if (event.detail.before) sizingFollow = nearBottom();
    else {
      scrollAfterChange(sizingFollow);
      if (event.detail.focusInput && !panel.hidden) input.focus({ preventScroll: true });
    }
  });
  launcher.addEventListener("click", openPanel);
  close.addEventListener("click", closePanel);
  window.addEventListener("shinyhub:chat:discover", function (event) {
    if (event.detail && event.detail.version === VERSION && chatSession) {
      announceChat("capabilities", { enabled: true, panel: panel, title: "Ask this app" });
    }
  });
  window.addEventListener("shinyhub:chat:host", function (event) {
    if (!event.detail || event.detail.version !== VERSION) return;
    toolbarAvailable = !!event.detail.available;
    toolbarSuspended = !!event.detail.suspended;
    nativeChat = !!event.detail.native;
    document.body.classList.toggle("sh-agent-toolbar-host", toolbarAvailable);
    document.body.classList.toggle("sh-agent-native-chat", nativeChat);
    updateSizing();
    launcher.hidden = !chatSession || toolbarAvailable || toolbarSuspended;
  });
  window.addEventListener("shinyhub:chat:toggle", function (event) {
    if (!event.detail || event.detail.version !== VERSION || !chatSession) return;
    if (panel.hidden) openPanel();
    else closePanel();
  });
  window.addEventListener("shinyhub:chat:new", function (event) {
    if (event.detail && event.detail.version === VERSION) reset.click();
  });
  jump.addEventListener("click", function () { log.scrollTop = log.scrollHeight; jump.hidden = true; });
  log.addEventListener("scroll", function () { jump.hidden = nearBottom(); });
  input.addEventListener("input", updateSend);
  panel.addEventListener("keydown", function (event) {
    if (event.key === "Escape") { closePanel(); return; }
    if (event.key !== "Tab" || nativeChat || !matchMedia("(max-width: 680px)").matches) return;
    var focusable = Array.from(panel.querySelectorAll("button,textarea,summary,[tabindex='0']")).filter(function (item) {
      return !item.disabled && !item.hidden && item.getClientRects().length;
    });
    var first = focusable[0], last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  });
  input.addEventListener("keydown", function (event) {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
      event.preventDefault(); composer.requestSubmit();
    }
  });
  composer.addEventListener("submit", function (event) {
    event.preventDefault();
    if (!chatSession || activeRequest || !input.value.trim()) return;
    var question = input.value.trim();
    input.value = "";
    announcer.textContent = "";
    appendMessage("user", question);
    activeAnswer = appendMessage("assistant", "");
    answerSource = "";
    activeRequest = crypto.randomUUID();
    setBusy(true);
    setStatus("Connecting to assistant");
    emit({ version: VERSION, session: chatSession, requestId: activeRequest, message: question });
  });
  stop.addEventListener("click", function () {
    if (activeRequest) {
      stop.disabled = true;
      setStatus("Stopping the answer");
      emit({ version: VERSION, session: chatSession,
        requestId: crypto.randomUUID(), action: "cancel" });
    }
  });
  reset.addEventListener("click", function () {
    if (!chatSession || activeRequest) return;
    emit({ version: VERSION, session: chatSession,
      requestId: crypto.randomUUID(), action: "reset" });
  });

  function install() {
    if (installed || !window.Shiny || typeof window.Shiny.addCustomMessageHandler !== "function") return false;
    installed = true;
    window.Shiny.addCustomMessageHandler("shinyhub-agent-chat-capabilities", function (message) {
      if (!message || message.version !== VERSION || !message.enabled ||
          typeof message.session !== "string") return;
      if (chatSession && chatSession !== message.session) {
        log.replaceChildren(empty);
        finish("App session reconnected. Start a new question.");
      }
      chatSession = message.session;
      launcher.hidden = toolbarAvailable || toolbarSuspended;
      announceChat("capabilities", { enabled: true, panel: panel, title: "Ask this app" });
      updateSend();
    });
    window.Shiny.addCustomMessageHandler("shinyhub-agent-chat-event", function (event) {
      if (!event || event.version !== VERSION || event.session !== chatSession) return;
      if (event.type === "approval_required") { if (activeRequest) showApproval(event); return; }
      if (event.type === "reset") {
        cancelAnswerFrame();
        activeAnswer = null;
        activeRequest = null;
        answerSource = "";
        pendingTools = [];
        clearApproval();
        log.replaceChildren(empty);
        announcer.textContent = "";
        lastApprovalCard = null;
        undoRequests.clear();
        setBusy(false);
        setStatus("New chat started");
        return;
      }
      if (event.type === "undo_result") {
        var pendingUndo = undoRequests.get(event.requestId);
        if (!pendingUndo) return;
        undoRequests.delete(event.requestId);
        pendingUndo.button.remove();
        if (event.ok) {
          pendingUndo.receipt.dataset.result = "undone";
          pendingUndo.title.textContent = "Change undone";
          pendingUndo.detail.textContent = (event.detail || "Previous view restored") +
            ". The answer describes the view before Undo.";
          pendingUndo.detail.hidden = false;
        } else {
          pendingUndo.receipt.dataset.result = "unavailable";
          pendingUndo.detail.textContent = event.message || "Undo is unavailable.";
          pendingUndo.detail.hidden = false;
        }
        setStatus(event.ok ? "Previous view restored" : "Check the current view", event.ok ? "working" : "error");
        return;
      }
      if (event.requestId !== activeRequest) return;
      if (event.type === "status") setStatus(event.text);
      else if (event.type === "tool_started") showTool(event);
      else if (event.type === "tool_finished") finishTool(event);
      else if (event.type === "delta") {
        if (!activeAnswer || typeof event.text !== "string") return;
        answerSource += event.text;
        scheduleAnswer();
        setStatus("Writing answer");
      } else if (event.type === "action_applied") {
        showReceipt(event);
        setStatus("Writing answer");
      } else if (event.type === "done") finish("");
      else if (event.type === "error") {
        finish(event.message || "The assistant could not finish.");
      }
    });
    return true;
  }
  if (!install()) {
    var attempts = 0;
    var timer = setInterval(function () {
      attempts += 1;
      if (install() || attempts >= 100) clearInterval(timer);
    }, 100);
  }
})();
