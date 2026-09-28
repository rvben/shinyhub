(function () {
  "use strict";

  var VERSION = 1;
  var capabilities = null;
  var pending = new Map();
  var registered = new Set();
  var installed = false;
  var confirmationOpen = false;

  function confirmAction(message) {
    if (confirmationOpen) return Promise.reject(new Error("Another action is awaiting confirmation."));
    confirmationOpen = true;
    return new Promise(function (resolve) {
      var previousFocus = document.activeElement;
      var backdrop = document.createElement("div");
      backdrop.className = "sh-agent-confirm-backdrop";
      var dialog = document.createElement("section");
      dialog.className = "sh-agent-confirm";
      dialog.setAttribute("role", "dialog");
      dialog.setAttribute("aria-modal", "true");
      dialog.setAttribute("aria-labelledby", "sh-agent-confirm-title");
      var title = document.createElement("h2");
      title.id = "sh-agent-confirm-title";
      title.textContent = "Confirm app action";
      var detail = document.createElement("p");
      detail.textContent = message;
      var actions = document.createElement("div");
      actions.className = "sh-agent-confirm-actions";
      var cancel = document.createElement("button");
      cancel.type = "button";
      cancel.textContent = "Cancel";
      var apply = document.createElement("button");
      apply.type = "button";
      apply.textContent = "Apply change";
      apply.className = "sh-agent-confirm-apply";
      actions.append(cancel, apply);
      dialog.append(title, detail, actions);
      backdrop.append(dialog);
      document.body.append(backdrop);
      var timeout = setTimeout(function () { finish(false); }, 30000);
      function finish(allowed) {
        clearTimeout(timeout);
        backdrop.remove();
        confirmationOpen = false;
        if (previousFocus && previousFocus.isConnected) previousFocus.focus({ preventScroll: true });
        resolve(allowed);
      }
      cancel.addEventListener("click", function () { finish(false); });
      apply.addEventListener("click", function () { finish(true); });
      backdrop.addEventListener("keydown", function (event) {
        if (event.key === "Escape") { event.preventDefault(); finish(false); }
        if (event.key === "Tab") {
          if (event.shiftKey && document.activeElement === cancel) { event.preventDefault(); apply.focus(); }
          else if (!event.shiftKey && document.activeElement === apply) { event.preventDefault(); cancel.focus(); }
        }
      });
      apply.focus({ preventScroll: true });
    });
  }

  function rejectPending(message) {
    pending.forEach(function (entry) {
      clearTimeout(entry.timer);
      entry.reject(new Error(message));
    });
    pending.clear();
  }

  function dispatch(name, detail) {
    window.dispatchEvent(new CustomEvent(name, { detail: detail }));
  }

  function validCapabilities(value) {
    return value && value.version === VERSION &&
      typeof value.session === "string" && value.session.length > 10 &&
      Array.isArray(value.tools);
  }

  function findTool(name) {
    return capabilities && capabilities.tools.find(function (tool) {
      return tool.name === name;
    });
  }

  async function invoke(name, argumentsValue) {
    var tool = findTool(name);
    if (!tool) return Promise.reject(new Error("This tool is unavailable in the current app session."));
    if (pending.size >= 4) return Promise.reject(new Error("Too many actions are waiting for the app."));
    if (!tool.readOnly && !(await confirmAction(tool.confirmation))) {
      throw new Error("Action cancelled.");
    }
    var requestId = crypto.randomUUID();
    return new Promise(function (resolve, reject) {
      var timer = setTimeout(function () {
        pending.delete(requestId);
        reject(new Error("The app did not confirm the action in time."));
      }, 10000);
      pending.set(requestId, { resolve: resolve, reject: reject, timer: timer });
      try {
        window.Shiny.setInputValue(".shinyhub_agent_request", {
          version: VERSION,
          session: capabilities.session,
          requestId: requestId,
          name: name,
          arguments: argumentsValue || {}
        }, { priority: "event" });
      } catch (error) {
        clearTimeout(timer);
        pending.delete(requestId);
        reject(new Error("The app session is unavailable."));
      }
    });
  }

  function registerWebMCP() {
    var context = document.modelContext;
    if (!context || typeof context.registerTool !== "function") {
      dispatch("shinyhub:agent:webmcp", { available: false });
      return;
    }
    capabilities.tools.forEach(function (tool) {
      if (registered.has(tool.name)) return;
      try {
        Promise.resolve(context.registerTool({
          name: tool.name,
          description: tool.description,
          inputSchema: tool.inputSchema,
          annotations: { readOnlyHint: tool.readOnly },
          execute: function (args) { return invoke(tool.name, args); }
        })).then(function () {
          registered.add(tool.name);
          dispatch("shinyhub:agent:webmcp", { available: true });
        }).catch(function () {
          dispatch("shinyhub:agent:webmcp", { available: false, name: tool.name });
        });
      } catch (error) {
        dispatch("shinyhub:agent:webmcp", { available: false, name: tool.name });
      }
    });
  }

  function install() {
    if (installed || !window.Shiny || typeof window.Shiny.addCustomMessageHandler !== "function") return false;
    installed = true;
    window.Shiny.addCustomMessageHandler("shinyhub-agent-capabilities", function (message) {
      if (!validCapabilities(message)) return;
      if (capabilities && capabilities.session !== message.session) rejectPending("The app session changed.");
      capabilities = message;
      registerWebMCP();
      dispatch("shinyhub:agent:capabilities", message);
    });
    window.Shiny.addCustomMessageHandler("shinyhub-agent-result", function (message) {
      if (!capabilities || !message || message.version !== VERSION ||
          message.session !== capabilities.session) return;
      var entry = pending.get(message.requestId);
      if (!entry) return;
      pending.delete(message.requestId);
      clearTimeout(entry.timer);
      if (message.ok) entry.resolve(message.result);
      else entry.reject(new Error(message.message || "The action failed."));
    });
    try {
      window.Shiny.setInputValue(".shinyhub_agent_discover", { version: VERSION, nonce: Date.now() }, { priority: "event" });
    } catch (error) { /* on-flushed publication still supplies capabilities */ }
    return true;
  }

  window.shinyhubAgentTools = Object.freeze({ invoke: invoke });
  if (!install()) {
    var attempts = 0;
    var timer = setInterval(function () {
      attempts += 1;
      if (install() || attempts >= 100) clearInterval(timer);
    }, 100);
  }
})();
