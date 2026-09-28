(function () {
  "use strict";

  var VERSION = 1;
  var chatSession = null;
  var activeRequest = null;
  var activeAnswer = null;
  var approval = null;
  var installed = false;
  var previousFocus = null;

  function make(tag, className, text) {
    var element = document.createElement(tag);
    if (className) element.className = className;
    if (text !== undefined) element.textContent = text;
    return element;
  }

  var launcher = make("button", "sh-agent-launcher", "Ask this app");
  launcher.type = "button";
  launcher.hidden = true;
  launcher.setAttribute("aria-controls", "shinyhub-agent-chat-panel");
  launcher.setAttribute("aria-expanded", "false");
  var panel = make("aside", "sh-agent-panel");
  panel.id = "shinyhub-agent-chat-panel";
  panel.hidden = true;
  panel.setAttribute("aria-labelledby", "shinyhub-agent-chat-heading");
  var header = make("header", "sh-agent-header");
  var heading = make("h2", "", "Ask this app");
  heading.id = "shinyhub-agent-chat-heading";
  var close = make("button", "sh-agent-icon-button", "Close");
  close.type = "button";
  close.setAttribute("aria-label", "Close assistant");
  header.append(heading, close);
  var description = make("p", "sh-agent-description", "Ask about this view or request a change. The app confirms actions before applying them.");
  var log = make("div", "sh-agent-log");
  log.setAttribute("role", "log");
  log.setAttribute("aria-label", "Conversation");
  var empty = make("p", "sh-agent-empty", "What would you like to know about this view?");
  log.append(empty);
  var status = make("div", "sh-agent-status");
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");
  var composer = make("form", "sh-agent-composer");
  var input = make("textarea", "sh-agent-input");
  input.rows = 2;
  input.maxLength = 2000;
  input.placeholder = "Ask a question…";
  input.setAttribute("aria-label", "Question for the app assistant");
  var controls = make("div", "sh-agent-controls");
  var reset = make("button", "sh-agent-secondary", "New chat");
  reset.type = "button";
  var stop = make("button", "sh-agent-secondary", "Stop");
  stop.type = "button";
  stop.hidden = true;
  var send = make("button", "sh-agent-send", "Send");
  send.type = "submit";
  controls.append(reset, stop, send);
  composer.append(input, controls);
  panel.append(header, description, log, status, composer);
  document.body.append(launcher, panel);

  function emit(payload) {
    window.Shiny.setInputValue(".shinyhub_agent_chat_request", payload, { priority: "event" });
  }

  function appendMessage(role, value) {
    if (empty.isConnected) empty.remove();
    var item = make("div", "sh-agent-message sh-agent-" + role);
    item.append(make("strong", "", role === "user" ? "You" : "Assistant"));
    var body = make("div", "sh-agent-message-body", value);
    item.append(body);
    log.append(item);
    log.scrollTop = log.scrollHeight;
    return body;
  }

  function setBusy(busy) {
    send.disabled = busy;
    reset.disabled = busy;
    stop.hidden = !busy;
    input.disabled = busy;
    if (!busy) input.focus({ preventScroll: true });
  }

  function openPanel() {
    previousFocus = document.activeElement;
    panel.hidden = false;
    launcher.setAttribute("aria-expanded", "true");
    if (matchMedia("(max-width: 680px)").matches) {
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
    if (previousFocus && previousFocus.isConnected) previousFocus.focus({ preventScroll: true });
    else launcher.focus({ preventScroll: true });
  }

  function clearApproval() {
    if (approval) approval.remove();
    approval = null;
  }

  function finish(message) {
    clearApproval();
    activeRequest = null;
    activeAnswer = null;
    setBusy(false);
    status.textContent = message || "";
  }

  function showApproval(event) {
    clearApproval();
    approval = make("section", "sh-agent-approval");
    approval.append(make("strong", "", "Confirm app action"));
    approval.append(make("p", "", event.message));
    var values = Object.values(event.arguments || {}).map(String).join(", ");
    if (values) approval.append(make("p", "sh-agent-approval-args", values));
    var actions = make("div", "sh-agent-approval-actions");
    var decline = make("button", "sh-agent-secondary", "Cancel action");
    var approve = make("button", "sh-agent-send", "Apply change");
    [decline, approve].forEach(function (button) { button.type = "button"; });
    function decide(approved) {
      window.Shiny.setInputValue(".shinyhub_agent_chat_decision", {
        version: VERSION, session: chatSession, approvalId: event.approvalId,
        approved: approved
      }, { priority: "event" });
      clearApproval();
      status.textContent = approved ? "Applying change" : "Action cancelled";
    }
    decline.addEventListener("click", function () { decide(false); });
    approve.addEventListener("click", function () { decide(true); });
    actions.append(decline, approve);
    approval.append(actions);
    log.append(approval);
    log.scrollTop = log.scrollHeight;
    approve.focus({ preventScroll: true });
  }

  launcher.addEventListener("click", openPanel);
  close.addEventListener("click", closePanel);
  panel.addEventListener("keydown", function (event) {
    if (event.key === "Escape") { closePanel(); return; }
    if (event.key !== "Tab" || !matchMedia("(max-width: 680px)").matches) return;
    var focusable = Array.from(panel.querySelectorAll("button,textarea")).filter(function (item) {
      return !item.disabled && !item.hidden;
    });
    var first = focusable[0], last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  });
  input.addEventListener("keydown", function (event) {
    if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); composer.requestSubmit(); }
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
    status.textContent = "Connecting to assistant";
    emit({ version: VERSION, session: chatSession, requestId: activeRequest, message: question });
  });
  stop.addEventListener("click", function () {
    if (activeRequest) emit({ version: VERSION, session: chatSession,
      requestId: crypto.randomUUID(), action: "cancel" });
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
      launcher.hidden = false;
    });
    window.Shiny.addCustomMessageHandler("shinyhub-agent-chat-event", function (event) {
      if (!event || event.version !== VERSION || event.session !== chatSession) return;
      if (event.type === "approval_required") { if (activeRequest) showApproval(event); return; }
      if (event.type === "reset") { log.replaceChildren(empty); status.textContent = "New chat started"; return; }
      if (event.requestId !== activeRequest) return;
      if (event.type === "status") status.textContent = event.text;
      else if (event.type === "delta") {
        activeAnswer.textContent += event.text;
        status.textContent = "";
        log.scrollTop = log.scrollHeight;
      } else if (event.type === "action_applied") {
        var receipt = make("p", "sh-agent-receipt", "App action applied");
        activeAnswer.parentNode.before(receipt);
        status.textContent = "Writing answer";
      } else if (event.type === "done") finish("");
      else if (event.type === "error") {
        if (activeAnswer && activeAnswer.textContent) activeAnswer.textContent += "\n\nAnswer incomplete.";
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
