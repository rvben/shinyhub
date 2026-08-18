package scaleway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

type fakeClient struct {
	createFn func(context.Context, CreateContainerInput) (Container, error)
	updateFn func(context.Context, UpdateContainerInput) (Container, error)
	getFn    func(context.Context, string) (Container, error)
	listFn   func(context.Context, ListContainersInput) ([]Container, error)
	deleteFn func(context.Context, string) error
}

func (f *fakeClient) CreateContainer(ctx context.Context, in CreateContainerInput) (Container, error) {
	return f.createFn(ctx, in)
}
func (f *fakeClient) UpdateContainer(ctx context.Context, in UpdateContainerInput) (Container, error) {
	if f.updateFn == nil {
		panic("unexpected UpdateContainer")
	}
	return f.updateFn(ctx, in)
}
func (f *fakeClient) GetContainer(ctx context.Context, id string) (Container, error) {
	if f.getFn == nil {
		panic("unexpected GetContainer")
	}
	return f.getFn(ctx, id)
}
func (f *fakeClient) ListContainers(ctx context.Context, in ListContainersInput) ([]Container, error) {
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx, in)
}
func (f *fakeClient) DeleteContainer(ctx context.Context, id string) error {
	if f.deleteFn == nil {
		panic("unexpected DeleteContainer")
	}
	return f.deleteFn(ctx, id)
}

func testConfig() Config {
	return Config{
		Region:          "nl-ams",
		ProjectID:       "project-id",
		NamespaceID:     "namespace-id",
		Image:           "rg.nl-ams.scw.cloud/shinyhub/runner:latest",
		NamePrefix:      "demo",
		ControlPlaneURL: "https://demo.shinyhub.dev",
		BundleTokenKey:  []byte("01234567890123456789012345678901"),
		BundleTokenTTL:  10 * time.Minute,
		OriginToken:     "scw-secret-key",
		DefaultMemoryMB: 512,
		DefaultMVCpu:    250,
	}
}

func testStartParams() process.StartParams {
	return process.StartParams{
		Slug:            "operations-dashboard",
		AppID:           42,
		Index:           2,
		Tier:            "serverless",
		Command:         []string{"uv", "run", "python", "-m", "shiny", "run", "--port", "8080", "app.py"},
		Port:            8080,
		Env:             []string{"PUBLIC=value"},
		SecretEnv:       []string{"API_KEY=secret"},
		MemoryLimitMB:   768,
		CPUQuotaPercent: 50,
		DeploymentID:    99,
		AppVersion:      "v3",
		ContentDigest:   "sha256:abcdef",
	}
}

func TestStartCreatesPrivateScaleToZeroContainer(t *testing.T) {
	var created CreateContainerInput
	client := &fakeClient{createFn: func(_ context.Context, in CreateContainerInput) (Container, error) {
		created = in
		return Container{
			ID: "container-id", Name: in.Name, Status: StatusReady,
			PublicEndpoint: "https://ops.functions.fnc.nl-ams.scw.cloud",
			Tags:           in.Tags,
		}, nil
	}}
	rt, err := New(client, testConfig(), nil, WithPollInterval(time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ep, err := rt.Start(context.Background(), testStartParams(), io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ep.Provider != Provider || ep.WorkerID != WorkerID || ep.URL == "" || ep.Handle.ContainerID == "" {
		t.Fatalf("endpoint = %+v", ep)
	}
	if created.MinScale != 0 || created.MaxScale != 1 {
		t.Fatalf("scale = %d..%d, want 0..1", created.MinScale, created.MaxScale)
	}
	if created.Privacy != PrivacyPrivate {
		t.Fatalf("privacy = %q, want private", created.Privacy)
	}
	if created.Timeout != time.Hour {
		t.Fatalf("timeout = %s, want 1h", created.Timeout)
	}
	if created.MemoryMB != 768 || created.MVCpu != 500 {
		t.Fatalf("resources = %d MB/%d mvCPU, want 768/500", created.MemoryMB, created.MVCpu)
	}
	if created.Environment["SHINYHUB_SLUG"] != "operations-dashboard" || created.Environment["PUBLIC"] != "value" {
		t.Fatalf("environment = %#v", created.Environment)
	}
	if created.SecretEnvironment["API_KEY"] != "secret" || created.SecretEnvironment["SHINYHUB_BUNDLE_TOKEN"] == "" {
		t.Fatalf("secret environment = %#v", created.SecretEnvironment)
	}
	if got := created.Args; len(got) != len(testStartParams().Command) {
		t.Fatalf("args = %#v", got)
	}
}

func TestStartReusesStableContainerForReplica(t *testing.T) {
	existing := Container{
		ID: "stable-id", Name: "demo-a42-r2", Status: StatusReady,
		PublicEndpoint: "https://stable.functions.fnc.nl-ams.scw.cloud",
		Tags:           tags(testStartParams()),
	}
	var updated UpdateContainerInput
	client := &fakeClient{
		createFn: func(context.Context, CreateContainerInput) (Container, error) {
			t.Fatal("Start must update the retained replica resource, not create a duplicate")
			return Container{}, nil
		},
		listFn: func(context.Context, ListContainersInput) ([]Container, error) {
			return []Container{existing}, nil
		},
		updateFn: func(_ context.Context, in UpdateContainerInput) (Container, error) {
			updated = in
			result := existing
			result.Tags = in.Tags
			return result, nil
		},
	}
	rt, err := New(client, testConfig(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ep, err := rt.Start(context.Background(), testStartParams(), io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if ep.Handle.ContainerID != WorkerID+"/stable-id" || ep.URL != existing.PublicEndpoint {
		t.Fatalf("endpoint = %+v", ep)
	}
	if updated.ID != "stable-id" || updated.Image != testConfig().Image || updated.MaxScale != 1 {
		t.Fatalf("update = %+v", updated)
	}
}

func TestStartFailsClosedWhenTwoResourcesClaimOneReplica(t *testing.T) {
	params := testStartParams()
	client := &fakeClient{
		createFn: func(context.Context, CreateContainerInput) (Container, error) {
			t.Fatal("ambiguous ownership must not create another resource")
			return Container{}, nil
		},
		listFn: func(context.Context, ListContainersInput) ([]Container, error) {
			return []Container{
				{ID: "first", Tags: tags(params)},
				{ID: "second", Tags: tags(params)},
			}, nil
		},
	}
	rt, err := New(client, testConfig(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = rt.Start(context.Background(), params, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "multiple managed containers") {
		t.Fatalf("Start error = %v", err)
	}
}

func TestStartClampsResourcesToProviderLimits(t *testing.T) {
	var created CreateContainerInput
	client := &fakeClient{createFn: func(_ context.Context, in CreateContainerInput) (Container, error) {
		created = in
		return Container{ID: "container-id", Status: StatusReady, PublicEndpoint: "https://example.test"}, nil
	}}
	rt, err := New(client, testConfig(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	params := testStartParams()
	params.MemoryLimitMB = 99_999
	params.CPUQuotaPercent = 999

	if _, err := rt.Start(context.Background(), params, io.Discard); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if created.MemoryMB != 12_228 || created.MVCpu != 6_000 {
		t.Fatalf("resources = %d MB/%d mvCPU, want provider maxima 12228/6000", created.MemoryMB, created.MVCpu)
	}
}

func TestReplicaTransportAuthenticatesPrivateOriginWithoutMutatingRequest(t *testing.T) {
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-Auth-Token"); got != testConfig().OriginToken {
			t.Errorf("X-Auth-Token = %q", got)
		}
		if got := req.Header.Get("X-Request-ID"); got != "request-id" {
			t.Errorf("X-Request-ID = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})

	rt, err := New(&fakeClient{}, testConfig(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://private.example.test", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Request-ID", "request-id")
	transport := rt.ReplicaTransportForWorker(WorkerID).(*authTransport)
	transport.base = base
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()
	if got := req.Header.Get("X-Auth-Token"); got != "" {
		t.Fatalf("source request was mutated: X-Auth-Token = %q", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestResourceNamePreservesReplicaIdentityWhenPrefixIsLong(t *testing.T) {
	params := testStartParams()
	longPrefix := strings.Repeat("company-platform-", 8)
	first := resourceName(longPrefix, params)
	params.AppID++
	second := resourceName(longPrefix, params)

	if len(first) > 34 || !strings.HasSuffix(first, "-a42-r2") {
		t.Fatalf("resourceName = %q", first)
	}
	if first == second || !strings.HasSuffix(second, "-a43-r2") {
		t.Fatalf("resource names do not preserve identity: %q, %q", first, second)
	}
}
