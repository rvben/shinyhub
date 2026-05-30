package httproute_test

import (
	"context"
	"testing"

	"github.com/rvben/shinyhub/internal/httproute"
)

func TestWithPatternRoundTrip(t *testing.T) {
	ctx := context.Background()
	pattern := "/api/apps/{slug}"
	ctx = httproute.WithPattern(ctx, pattern)
	if got := httproute.PatternFromContext(ctx); got != pattern {
		t.Fatalf("PatternFromContext = %q, want %q", got, pattern)
	}
}

func TestPatternFromContextEmpty(t *testing.T) {
	if got := httproute.PatternFromContext(context.Background()); got != "" {
		t.Fatalf("PatternFromContext on bare context = %q, want empty string", got)
	}
}

func TestWithPatternDoesNotMutateParent(t *testing.T) {
	parent := context.Background()
	_ = httproute.WithPattern(parent, "/api/foo")
	if got := httproute.PatternFromContext(parent); got != "" {
		t.Fatalf("parent context was mutated: got %q, want empty", got)
	}
}

func TestWithPatternOverwrite(t *testing.T) {
	ctx := context.Background()
	ctx = httproute.WithPattern(ctx, "/api/first")
	ctx = httproute.WithPattern(ctx, "/api/second")
	if got := httproute.PatternFromContext(ctx); got != "/api/second" {
		t.Fatalf("PatternFromContext after overwrite = %q, want %q", got, "/api/second")
	}
}
