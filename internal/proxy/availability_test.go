package proxy_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestAvailabilityRouteExcludesDrainingAndWrongGeneration(t *testing.T) {
	p := proxy.New()
	if err := p.Register("demo", "http://127.0.0.1:9999"); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureServingGeneration("demo", 42, config.IsolationMultiplex); err != nil {
		t.Fatal(err)
	}
	if serving, _ := p.AvailabilityRoute("demo", 42); !serving {
		t.Fatal("published route unavailable")
	}
	if serving, eligible := p.AvailabilityRoute("demo", 43); serving || eligible {
		t.Fatal("wrong generation available")
	}
	restore := p.BeginSettingsDrain("demo")
	if serving, eligible := p.AvailabilityRoute("demo", 42); serving || eligible {
		t.Fatal("draining route available")
	}
	restore()
	p.DrainReplica("demo", 0)
	if serving, _ := p.AvailabilityRoute("demo", 42); serving {
		t.Fatal("draining replica available")
	}
	p.SetPoolMode("elastic", config.IsolationPerSession, 1, 2)
	if serving, eligible := p.AvailabilityRoute("elastic", 42); serving || !eligible {
		t.Fatal("idle elastic pool must be eligible only")
	}
	if p.ElasticWorkerCount("elastic") != 0 {
		t.Fatal("monitor allocated a worker")
	}
}

func TestAvailabilityRouteElasticCandidateDoesNotHidePublishedPool(t *testing.T) {
	p := proxy.New()
	p.SetPoolSize("demo", 1)
	p.SetPoolMode("demo", config.IsolationGrouped, 2, 4)
	if err := p.EnsureServingGeneration("demo", 42, config.IsolationGrouped); err != nil {
		t.Fatal(err)
	}
	if _, err := p.StageGenerationWithPolicy("demo", 43, 1, proxy.GenerationPolicy{Mode: config.IsolationGrouped, GroupedSize: 2, MaxWorkers: 4}, 0); err != nil {
		t.Fatal(err)
	}
	if serving, eligible := p.AvailabilityRoute("demo", 42); serving || !eligible {
		t.Fatal("candidate hid eligible published generation")
	}
	if serving, eligible := p.AvailabilityRoute("demo", 43); serving || eligible {
		t.Fatal("candidate advertised before publication")
	}
	if p.ElasticWorkerCount("demo") != 0 {
		t.Fatal("monitor allocated a worker")
	}
}
