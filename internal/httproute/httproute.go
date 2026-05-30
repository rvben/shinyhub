// Package httproute propagates a matched HTTP route pattern through a request
// context as an immutable string. It is a leaf package with no dependencies
// beyond the standard library so any layer of the middleware stack can import
// it without introducing import cycles.
//
// The pattern is set once by the observation layer (before the inner handler
// runs) using an independent chi route-context lookup, then read after the
// inner handler returns. Stashing it as a plain string avoids sharing a
// mutable chi.RouteContext across a timeout boundary, which would race when
// http.TimeoutHandler returns concurrently with the inner chi mux still
// mutating the context's RoutePatterns slice.
package httproute

import "context"

// routePatternKey is the unexported context key for the matched route pattern.
type routePatternKey struct{}

// WithPattern returns a derived context carrying pattern as the matched route.
// Callers should pass the pattern string obtained from an independent chi
// route lookup performed on a private, non-shared route context.
func WithPattern(ctx context.Context, pattern string) context.Context {
	return context.WithValue(ctx, routePatternKey{}, pattern)
}

// PatternFromContext returns the route pattern stored by WithPattern, or the
// empty string when no pattern has been set.
func PatternFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(routePatternKey{}).(string); ok {
		return v
	}
	return ""
}
