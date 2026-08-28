(function () {
  "use strict";

  var VERSION = 1;
  var DISCOVER = "shinyhub:bookmark:discover";
  var CAPABILITIES = "shinyhub:bookmark:capabilities";
  var CREATE = "shinyhub:bookmark:create";
  var RESULT = "shinyhub:bookmark:result";
  var ERROR = "shinyhub:bookmark:error";
  var SYNC_STATUS = "shinyhub:bookmark:sync-status";
  var REQUEST_INPUT = ".shinyhub_bookmark_request";
  var DISCOVER_INPUT = ".shinyhub_bookmark_discover";
  var SYNC_ACK_INPUT = ".shinyhub_bookmark_sync_ack";
  var SYNC_DELAY_MS = 300;
  var SYNC_RETRY_DELAY_MS = 750;
  var SYNC_REQUEST_TIMEOUT_MS = 3000;
  var CREATE_REQUEST_TIMEOUT_MS = 7000;
  var SYNC_REQUEST_PREFIX = "url-sync-";
  var cachedCapabilities = null;
  var handlersInstalled = false;
  var installAttempts = 0;
  var pendingRequest = null;
  var queuedCreate = null;
  var syncNeeded = false;
  var syncTimer = null;
  var requestTimer = null;
  var syncSequence = 0;
  var desiredSyncRevision = 0;
  var syncRetries = 0;
  var recentSyncRequests = Object.create(null);
  var recentSyncOrder = [];
  var discoveryRetryTimer = null;
  var discoveryAttempts = 0;

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

  function clearSyncTimer() {
    if (syncTimer !== null) {
      window.clearTimeout(syncTimer);
      syncTimer = null;
    }
  }

  function clearRequestTimer() {
    if (requestTimer !== null) {
      window.clearTimeout(requestTimer);
      requestTimer = null;
    }
  }

  function isSyncRequest(requestId) {
    return typeof requestId === "string" && recentSyncRequests[requestId] === true;
  }

  function isSyncMessage(message) {
    return message && (message.purpose === "sync" || isSyncRequest(message.requestId));
  }

  function rememberSyncRequest(requestId) {
    recentSyncRequests[requestId] = true;
    recentSyncOrder.push(requestId);
    if (recentSyncOrder.length > 32) {
      delete recentSyncRequests[recentSyncOrder.shift()];
    }
  }

  function registeredFieldIDs() {
    if (!cachedCapabilities || !Array.isArray(cachedCapabilities.fields)) return [];
    var result = [];
    var seen = Object.create(null);
    for (var i = 0; i < cachedCapabilities.fields.length; i++) {
      var id = cachedCapabilities.fields[i] && cachedCapabilities.fields[i].id;
      if (typeof id !== "string" || !id || seen[id]) continue;
      seen[id] = true;
      result.push(id);
    }
    return result;
  }

  function replaceCurrentViewURL(rawURL) {
    if (typeof rawURL !== "string" || !rawURL) return false;
    try {
      var target = new URL(rawURL, window.location.href);
      if (
        target.origin !== window.location.origin ||
        target.pathname !== window.location.pathname
      ) {
        return false;
      }
      if (!target.hash && window.location.hash) target.hash = window.location.hash;
      var next = target.pathname + target.search + target.hash;
      var current = window.location.pathname + window.location.search + window.location.hash;
      if (next !== current) {
        window.history.replaceState(window.history.state, "", next);
      }
      return true;
    } catch (error) {
      return false;
    }
  }

  function emitSyncStatus(state, code, message) {
    emit(SYNC_STATUS, {
      version: VERSION,
      state: state,
      code: code || "",
      message: message || ""
    });
  }

  function acknowledgeURLSync(revision) {
    try {
      window.Shiny.setInputValue(
        SYNC_ACK_INPUT,
        { version: VERSION, syncRevision: revision },
        { priority: "event" }
      );
    } catch (error) {
      /* The URL is already durable; discovery will reconcile the missing ack. */
    }
  }

  function dispatchDiscovery() {
    try {
      window.Shiny.setInputValue(
        DISCOVER_INPUT,
        { version: VERSION, nonce: Date.now() },
        { priority: "event" }
      );
      discoveryAttempts = 0;
      return true;
    } catch (error) {
      discoveryAttempts += 1;
      if (discoveryRetryTimer === null && discoveryAttempts < 20) {
        discoveryRetryTimer = window.setTimeout(function () {
          discoveryRetryTimer = null;
          if (handlersInstalled) dispatchDiscovery();
        }, 100);
      }
      return false;
    }
  }

  function retryableSyncError(code) {
    return code === "busy" || code === "sync_timeout" ||
      code === "serialization_failed" || code === "request_timeout" ||
      code === "dispatch_failed";
  }

  function handleSyncFailure(code, message) {
    if (retryableSyncError(code) && syncRetries < 1) {
      syncRetries += 1;
      syncNeeded = true;
      clearSyncTimer();
      syncTimer = window.setTimeout(requestURLSync, SYNC_RETRY_DELAY_MS);
      return;
    }
    syncNeeded = false;
    emitSyncStatus(
      "error",
      code,
      message || "Latest filter changes could not be saved in this page's URL."
    );
  }

  function drainRequests(immediateSync) {
    if (pendingRequest) return;
    if (queuedCreate) {
      var nextCreate = queuedCreate;
      queuedCreate = null;
      sendRequest(nextCreate, "create");
      return;
    }
    if (immediateSync && syncNeeded) {
      clearSyncTimer();
      syncTimer = window.setTimeout(requestURLSync, 0);
    } else if (syncTimer === null) {
      scheduleURLSync();
    }
  }

  function finishRequest(requestId) {
    if (!pendingRequest || pendingRequest.requestId !== requestId) return null;
    var finished = pendingRequest;
    pendingRequest = null;
    clearRequestTimer();
    return finished;
  }

  function sendRequest(detail, kind, revision) {
    if (!install() || pendingRequest) return false;
    pendingRequest = { requestId: detail.requestId, kind: kind, revision: revision || 0 };
    try {
      window.Shiny.setInputValue(REQUEST_INPUT, detail, { priority: "event" });
    } catch (error) {
      pendingRequest = null;
      if (kind === "sync") {
        handleSyncFailure("dispatch_failed", "The app connection interrupted URL saving.");
      } else {
        emit(ERROR, {
          version: VERSION,
          requestId: detail.requestId,
          code: "dispatch_failed",
          message: "The app connection interrupted link creation. Try again."
        });
      }
      drainRequests();
      return false;
    }
    var timeout = kind === "sync" ? SYNC_REQUEST_TIMEOUT_MS : CREATE_REQUEST_TIMEOUT_MS;
    requestTimer = window.setTimeout(function () {
      if (!pendingRequest || pendingRequest.requestId !== detail.requestId) return;
      var timedOut = pendingRequest;
      pendingRequest = null;
      requestTimer = null;
      if (timedOut.kind === "sync") {
        handleSyncFailure("request_timeout", "The app took too long to save the latest URL.");
      } else {
        emit(ERROR, {
          version: VERSION,
          requestId: detail.requestId,
          code: "request_timeout",
          message: "The app took too long to create this link. Try again."
        });
      }
      drainRequests();
    }, timeout);
    return true;
  }

  function requestURLSync() {
    syncTimer = null;
    if (!syncNeeded || pendingRequest || queuedCreate) return;
    var include = registeredFieldIDs();
    if (!include.length) {
      syncNeeded = false;
      return;
    }
    syncNeeded = false;
    syncSequence += 1;
    var revision = desiredSyncRevision;
    var requestId = SYNC_REQUEST_PREFIX + Date.now() + "-" + syncSequence;
    rememberSyncRequest(requestId);
    sendRequest(
      {
        version: VERSION,
        requestId: requestId,
        include: include,
        purpose: "sync",
        syncRevision: revision
      },
      "sync",
      revision
    );
  }

  function scheduleURLSync() {
    if (!syncNeeded || pendingRequest || queuedCreate) return;
    clearSyncTimer();
    syncTimer = window.setTimeout(requestURLSync, SYNC_DELAY_MS);
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
      if (message.autoSync === true) {
        var revision = Number.isInteger(message.syncRevision) && message.syncRevision >= 0
          ? message.syncRevision
          : desiredSyncRevision + 1;
        var sameRevisionInFlight = pendingRequest &&
          pendingRequest.kind === "sync" && pendingRequest.revision === revision;
        if (revision < desiredSyncRevision) return;
        if (revision > desiredSyncRevision) syncRetries = 0;
        desiredSyncRevision = Math.max(desiredSyncRevision, revision);
        if (!sameRevisionInFlight) {
          syncNeeded = true;
          scheduleURLSync();
        }
      }
    });
    window.Shiny.addCustomMessageHandler("shinyhub-bookmark-result", function (message) {
      if (!validVersion(message)) return;
      var finished = finishRequest(message.requestId);
      if (finished && finished.kind === "sync") {
        if (syncNeeded || finished.revision !== desiredSyncRevision) {
          syncNeeded = true;
          drainRequests(true);
          return;
        }
        if (replaceCurrentViewURL(message.url)) {
          syncRetries = 0;
          acknowledgeURLSync(finished.revision);
          emitSyncStatus("saved", "", "");
        } else {
          handleSyncFailure(
            "invalid_url",
            "The app returned a URL outside the current view, so it was not applied."
          );
        }
        drainRequests();
        return;
      }
      if (finished) drainRequests();
      if (!isSyncMessage(message)) emit(RESULT, message);
    });
    window.Shiny.addCustomMessageHandler("shinyhub-bookmark-error", function (message) {
      if (!validVersion(message)) return;
      var finished = finishRequest(message.requestId);
      if (finished && finished.kind === "sync") {
        if (finished.revision !== desiredSyncRevision) {
          syncNeeded = true;
          drainRequests(true);
          return;
        }
        handleSyncFailure(
          typeof message.code === "string" ? message.code : "serialization_failed",
          typeof message.message === "string" ? message.message : ""
        );
        drainRequests();
        return;
      }
      if (finished) drainRequests();
      if (!isSyncMessage(message)) emit(ERROR, message);
    });
    // Capability messages can be part of Shiny's first flush, before a deferred
    // dependency has registered its handlers. Ask once immediately after the
    // bridge becomes live so that load order cannot hide the switcher action.
    dispatchDiscovery();
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
      dispatchDiscovery();
    }
    if (cachedCapabilities) emit(CAPABILITIES, cachedCapabilities);
  });

  window.addEventListener(CREATE, function (event) {
    if (!install() || !validVersion(event.detail)) return;
    var detail = {
      version: VERSION,
      requestId: event.detail.requestId,
      include: event.detail.include
    };
    clearSyncTimer();
    if (pendingRequest) {
      queuedCreate = detail;
      return;
    }
    sendRequest(detail, "create");
  });

  installWhenReady();
  emit(DISCOVER, { version: VERSION, source: "python" });
})();
