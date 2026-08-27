(function () {
  "use strict";

  var VERSION = 1;
  var DISCOVER = "shinyhub:bookmark:discover";
  var CAPABILITIES = "shinyhub:bookmark:capabilities";
  var CREATE = "shinyhub:bookmark:create";
  var RESULT = "shinyhub:bookmark:result";
  var ERROR = "shinyhub:bookmark:error";
  var REQUEST_INPUT = ".shinyhub_bookmark_request";
  var DISCOVER_INPUT = ".shinyhub_bookmark_discover";
  var cachedCapabilities = null;
  var handlersInstalled = false;
  var installAttempts = 0;

  function emit(name, detail) {
    try {
      window.dispatchEvent(new CustomEvent(name, { detail: detail }));
    } catch (error) {
      /* The bridge is optional chrome and must never interrupt the app. */
    }
  }

  function validVersion(message) {
    return message && message.version === VERSION;
  }

  function install() {
    if (handlersInstalled) {
      return true;
    }
    if (!window.Shiny) {
      return false;
    }
    if (
      typeof window.Shiny.addCustomMessageHandler !== "function" ||
      typeof window.Shiny.setInputValue !== "function"
    ) {
      return false;
    }
    handlersInstalled = true;
    window.Shiny.addCustomMessageHandler("shinyhub-bookmark-capabilities", function (message) {
      if (!validVersion(message)) return;
      cachedCapabilities = message;
      emit(CAPABILITIES, message);
    });
    window.Shiny.addCustomMessageHandler("shinyhub-bookmark-result", function (message) {
      if (validVersion(message)) emit(RESULT, message);
    });
    window.Shiny.addCustomMessageHandler("shinyhub-bookmark-error", function (message) {
      if (validVersion(message)) emit(ERROR, message);
    });
    // Capability messages can be part of Shiny's first flush, before a deferred
    // dependency has registered its handlers. Ask once immediately after the
    // bridge becomes live so that load order cannot hide the switcher action.
    window.Shiny.setInputValue(
      DISCOVER_INPUT,
      { version: VERSION, nonce: Date.now() },
      { priority: "event" }
    );
    return true;
  }

  function installWhenReady() {
    if (install()) return;
    installAttempts += 1;
    if (installAttempts < 100) {
      window.setTimeout(installWhenReady, 100);
    }
  }

  window.addEventListener(DISCOVER, function () {
    if (install()) {
      window.Shiny.setInputValue(
        DISCOVER_INPUT,
        { version: VERSION, nonce: Date.now() },
        { priority: "event" }
      );
    }
    if (cachedCapabilities) emit(CAPABILITIES, cachedCapabilities);
  });

  window.addEventListener(CREATE, function (event) {
    if (!install() || !validVersion(event.detail)) return;
    window.Shiny.setInputValue(REQUEST_INPUT, event.detail, { priority: "event" });
  });

  installWhenReady();
  emit(DISCOVER, { version: VERSION, source: "python" });
})();
