package tracing

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestDeploymentResources(t *testing.T) {
	cfg := config.TracingConfig{Enabled: true, OTLPEndpoint: "http://collector:4318"}
	base := EnvFor(cfg, "app", 2)
	for _, tc := range []struct{ version, digest, want string }{
		{"v1 + test", "sha256:digest", "service.version=v1%20%2B%20test"},
		{"", "sha256:digest", "service.version=sha256%3Adigest"},
	} {
		got := WithDeployment(base, 17, tc.version, tc.digest)
		attrs := envValue(t, got, "OTEL_RESOURCE_ATTRIBUTES")
		for _, want := range []string{"shinyhub.app.slug=app", "shinyhub.replica=2", "shinyhub.deployment.id=17", tc.want} {
			if !strings.Contains(attrs, want) {
				t.Errorf("missing %s in %s", want, attrs)
			}
		}
		if strings.Contains(attrs, "service.instance.id") {
			t.Fatal("must retain SDK instance identity")
		}
	}
	if strings.Contains(envValue(t, base, "OTEL_RESOURCE_ATTRIBUTES"), "deployment.id") {
		t.Fatal("mutated defaults")
	}
	if WithDeployment(nil, 17, "v1", "") != nil {
		t.Fatal("disabled tracing must inject nothing")
	}
	attrs := envValue(t, EnvFor(cfg, "app", -1), "OTEL_RESOURCE_ATTRIBUTES")
	if strings.Contains(attrs, "replica") {
		t.Fatal("hook must not claim a serving replica")
	}
}

func TestResourceOverridesKeepIdentityAndSecretPartition(t *testing.T) {
	base := []string{"OTEL_RESOURCE_ATTRIBUTES=shinyhub.replica=2,service.version=v1,team=platform"}
	for _, tc := range []struct {
		name        string
		app, secret []string
		want        string
		isSecret    bool
	}{
		{"custom tags", []string{"OTEL_RESOURCE_ATTRIBUTES=team=app,owner=one%2Ctwo"}, nil, "shinyhub.replica=2,service.version=v1,team=platform,team=app,owner=one%2Ctwo", false},
		{"empty override", []string{"OTEL_RESOURCE_ATTRIBUTES="}, nil, "shinyhub.replica=2,service.version=v1,team=platform", false},
		{"secret resource", []string{"OTEL_RESOURCE_ATTRIBUTES=team=app"}, []string{"OTEL_RESOURCE_ATTRIBUTES=owner=secret"}, "shinyhub.replica=2,service.version=v1,team=platform,team=app,owner=secret", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, secret := MergeResourceAttributes(append(append([]string{}, base...), tc.app...), tc.secret)
			got := envValue(t, append(append([]string{}, env...), secret...), "OTEL_RESOURCE_ATTRIBUTES")
			if got != tc.want {
				t.Fatalf("merged resource %s want %s", got, tc.want)
			}
			if tc.isSecret {
				for _, kv := range env {
					if strings.Contains(kv, "owner=secret") {
						t.Fatal("secret moved into plaintext env")
					}
				}
			}
		})
	}
}
