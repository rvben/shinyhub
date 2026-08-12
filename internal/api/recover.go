package api

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RecoverPanic wraps next so a panic in a downstream handler is logged and
// converted to a 500 instead of crashing the connection (and, on paths without
// their own recovery, escaping to the stdlib server's per-connection default
// which bypasses structured logging). chi.Recoverer already guards /api/*; this
// covers the outer mux paths - notably the /app/* reverse proxy.
//
// http.ErrAbortHandler is re-panicked so the stdlib server aborts the connection
// as intended (used to silently drop a hijacked/streaming connection).
func RecoverPanic(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			logger.Error("panic serving request",
				"method", r.Method,
				"path", r.URL.Path,
				"panic", rec,
				"stack", string(debug.Stack()),
			)
			// Best effort: writing after the response started or the connection
			// was hijacked (WebSocket) is a no-op the server logs, not a panic.
			w.WriteHeader(http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// recoverAPI is the chi middleware counterpart used inside Observe. Keeping
// recovery inside observation means metrics see the client-visible 500; unlike
// chi's generic Recoverer, this path also emits a structured error carrying the
// request ID assigned by accessLog, so an access-log 500 always has a matching
// diagnostic row.
func recoverAPI(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logger.Error("panic serving API request",
					"request_id", RequestIDFromContext(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}
