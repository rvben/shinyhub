package config

import (
	"testing"
	"time"
)

func TestRenderParkDefaults(t *testing.T) {
	c := &Config{}
	if d := c.RenderParkTTL(); d != 2*time.Second {
		t.Fatalf("RenderParkTTL default = %v, want 2s", d)
	}
	if v := c.RenderParkMaxPerApp(); v != 64 {
		t.Fatalf("RenderParkMaxPerApp default = %v, want 64", v)
	}
	if v := c.RenderParkMaxTotal(); v != 512 {
		t.Fatalf("RenderParkMaxTotal default = %v, want 512", v)
	}
}

func TestRenderParkExplicitValues(t *testing.T) {
	ttl := "5s"
	perApp := 8
	total := 9
	c := &Config{Server: ServerConfig{
		RenderParkTTL:       &ttl,
		RenderParkMaxPerApp: &perApp,
		RenderParkMaxTotal:  &total,
	}}
	if d := c.RenderParkTTL(); d != 5*time.Second {
		t.Fatalf("RenderParkTTL = %v, want 5s", d)
	}
	if v := c.RenderParkMaxPerApp(); v != 8 {
		t.Fatalf("RenderParkMaxPerApp = %v, want 8", v)
	}
	if v := c.RenderParkMaxTotal(); v != 9 {
		t.Fatalf("RenderParkMaxTotal = %v, want 9", v)
	}
}

func TestRenderParkTTLUnparseableFallsBackToDefault(t *testing.T) {
	ttl := "nonsense"
	c := &Config{Server: ServerConfig{RenderParkTTL: &ttl}}
	if d := c.RenderParkTTL(); d != 2*time.Second {
		t.Fatalf("RenderParkTTL(unparseable) = %v, want fallback default 2s", d)
	}
}
