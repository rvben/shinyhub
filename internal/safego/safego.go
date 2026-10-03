// Package safego runs background goroutines and timer callbacks under a
// shared panic recovery policy: an unhandled panic in a background task
// crashes the whole server, since nothing is waiting to observe it the way
// an HTTP handler's caller does. Recover logs and swallows the panic so one
// failing background task does not take the process down with it, except
// for a Fatal panic, which is re-panicked so the small set of regions where
// swallowing would leave an untracked live process still crash as before.
package safego

import (
	"log/slog"
	"runtime/debug"
	"time"
)

// Fatal wraps a panic value that must never be swallowed by Recover. It is
// raised in the few places where recovering would leave state worse than
// crashing: a launched process that no entry points at, for example, can
// only be reclaimed by the next startup's recovery scan. Every recover this
// package's callers install re-panics a Fatal immediately (see RepanicFatal)
// so it still crashes the process even when raised several call frames below
// a safego root.
type Fatal struct {
	Value any
	Stack []byte
}

// RepanicFatal re-panics r if it wraps a Fatal panic, and is a no-op for any
// other panic value (including nil, meaning "no panic in flight"). Call it
// as the first statement inside any recover this package's callers add, so a
// Fatal raised deep inside a background call chain is not absorbed by an
// intermediate recover before it reaches the goroutine root.
func RepanicFatal(r any) {
	if f, ok := r.(Fatal); ok {
		panic(f)
	}
}

// Recover returns a function to defer at the root of a background goroutine
// or timer callback named name. It re-panics a Fatal panic (RepanicFatal),
// and otherwise logs and swallows so the caller survives.
func Recover(name string) func() {
	return func() {
		r := recover()
		if r == nil {
			return
		}
		RepanicFatal(r)
		slog.Error("panic in background task", "task", name, "panic", r, "stack", debug.Stack())
	}
}

// Go launches fn in a new goroutine with panic recovery via Recover.
func Go(name string, fn func()) {
	go func() {
		defer Recover(name)()
		fn()
	}()
}

// AfterFunc behaves like time.AfterFunc, but recovers a panic in fn the same
// way Recover does, so a panicking timer callback cannot crash the server.
func AfterFunc(d time.Duration, name string, fn func()) *time.Timer {
	return time.AfterFunc(d, func() {
		defer Recover(name)()
		fn()
	})
}
