package config

import "testing"

func TestRenderConfigDefaults(t *testing.T) {
	c := &Config{} // zero server config: every accessor must return its default
	if h := c.RenderHeadroom(); h != 0.75 {
		t.Fatalf("RenderHeadroom default = %v, want 0.75", h)
	}
	if v := c.RenderCapacityCores(); v != 0 {
		t.Fatalf("RenderCapacityCores default = %v, want 0 (autodetect)", v)
	}
	if v := c.MaxCPUPercent(); v != 0 {
		t.Fatalf("MaxCPUPercent default = %v, want 0 (disabled)", v)
	}
	if v := c.PrincipalShareDivisor(); v != 20 {
		t.Fatalf("PrincipalShareDivisor default = %v, want 20", v)
	}
	if v := c.PrincipalLRUCapacity(); v != 4096 {
		t.Fatalf("PrincipalLRUCapacity default = %v, want 4096", v)
	}
}

func TestRenderConfigExplicitValues(t *testing.T) {
	hp := 50
	cores := 4.0
	maxCPU := 90.0
	div := 10
	cap := 512
	c := &Config{Server: ServerConfig{
		RenderHeadroomPercent: &hp,
		RenderCapacityCores:   &cores,
		MaxCPUPercent:         &maxCPU,
		PrincipalShareDivisor: &div,
		PrincipalLRUCapacity:  &cap,
	}}
	if h := c.RenderHeadroom(); h != 0.5 {
		t.Fatalf("RenderHeadroom(50%%) = %v, want 0.5", h)
	}
	if v := c.RenderCapacityCores(); v != 4 {
		t.Fatalf("RenderCapacityCores = %v, want 4", v)
	}
	if v := c.MaxCPUPercent(); v != 90 {
		t.Fatalf("MaxCPUPercent = %v, want 90", v)
	}
	if v := c.PrincipalShareDivisor(); v != 10 {
		t.Fatalf("PrincipalShareDivisor = %v, want 10", v)
	}
	if v := c.PrincipalLRUCapacity(); v != 512 {
		t.Fatalf("PrincipalLRUCapacity = %v, want 512", v)
	}
}
