(function () {
  "use strict";

  var VERSION = 1;
  var chatSession = null;
  var activeRequest = null;
  var activeAnswer = null;
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
  var panel = make("aside", "sh-agent-panel");
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
  header.append(brand, close);
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
  panel.append(header, logWrap, status, composer);
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
    input.focus({ preventScroll: true });
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
    clearApproval();
    if (message && lastApprovalCard && lastApprovalCard.dataset.result === "applying") {
      lastApprovalCard.dataset.result = "unknown";
      lastApprovalCard.querySelector(".sh-agent-approval-label").textContent = "Check the current view";
    }
    if (activeAnswer && activeAnswer.textContent.trim() && !message && navigator.clipboard) {
      var answer = activeAnswer;
      var copy = make("button", "sh-agent-copy", "Copy answer");
      copy.type = "button";
      copy.addEventListener("click", function () {
        navigator.clipboard.writeText(answer.textContent).then(function () {
          copy.textContent = "Copied";
          setTimeout(function () { copy.textContent = "Copy answer"; }, 1800);
        });
      });
      activeAnswer.parentNode.append(copy);
    }
    if (message && activeAnswer) activeAnswer.parentNode.append(make("p", "sh-agent-error", message));
    activeRequest = null;
    activeAnswer = null;
    pendingTools = [];
    setBusy(false);
    setStatus(message ? "Answer interrupted" : "", message ? "error" : "working");
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
  }

  function showApproval(event) {
    clearApproval();
    lastApprovalCard = null;
    var follow = nearBottom();
    approval = make("section", "sh-agent-approval");
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
    var focusable = Array.from(panel.querySelectorAll("button,textarea,summary")).filter(function (item) {
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
    appendMessage("user", question);
    activeAnswer = appendMessage("assistant", "");
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
        log.replaceChildren(empty);
        lastApprovalCard = null;
        undoRequests.clear();
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
        var follow = nearBottom();
        activeAnswer.textContent += event.text;
        setStatus("Writing answer");
        scrollAfterChange(follow);
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
