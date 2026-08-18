//go:build integration

package scaleway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"golang.org/x/net/websocket"
)

type integrationHarness struct {
	runtime  *Runtime
	sdk      *SDKClient
	params   process.StartParams
	endpoint process.ReplicaEndpoint
	token    string
}

func TestIntegrationManagedRuntimeHTTPWebSocketAndStableWake(t *testing.T) {
	h := newIntegrationHarness(t)
	h.start(t)
	h.probeHTTP(t)
	h.probeWebSocket(t)

	if err := h.runtime.Signal(h.endpoint.Handle, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := h.runtime.Wait(waitCtx, h.endpoint.Handle); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	firstHandle := h.endpoint.Handle.ContainerID
	h.start(t)
	if h.endpoint.Handle.ContainerID != firstHandle {
		t.Fatalf("wake created a new provider resource: first %q, second %q", firstHandle, h.endpoint.Handle.ContainerID)
	}
	h.probeHTTP(t)
}

func newIntegrationHarness(t *testing.T) *integrationHarness {
	t.Helper()
	accessKey := requiredIntegrationEnv(t, "SCW_ACCESS_KEY")
	secretKey := requiredIntegrationEnv(t, "SCW_SECRET_KEY")
	projectID := requiredIntegrationEnv(t, "SCW_DEFAULT_PROJECT_ID")
	namespaceID := requiredIntegrationEnv(t, "SHINYHUB_TEST_SCALEWAY_NAMESPACE_ID")
	image := requiredIntegrationEnv(t, "SHINYHUB_TEST_SCALEWAY_IMAGE")
	region := envOr("SCW_DEFAULT_REGION", "nl-ams")
	port, err := strconv.Atoi(envOr("SHINYHUB_TEST_SCALEWAY_PORT", "8080"))
	if err != nil {
		t.Fatalf("SHINYHUB_TEST_SCALEWAY_PORT: %v", err)
	}

	client, err := scw.NewClient(
		scw.WithAuth(accessKey, secretKey),
		scw.WithDefaultProjectID(projectID),
		scw.WithDefaultRegion(scw.Region(region)),
	)
	if err != nil {
		t.Fatalf("new Scaleway client: %v", err)
	}
	sdk := NewSDKClient(client, region)
	rt, err := New(sdk, Config{
		Region: region, ProjectID: projectID, NamespaceID: namespaceID,
		Image: image, NamePrefix: "shinyhub-integration",
		ControlPlaneURL: "https://integration.invalid", BundleTokenKey: []byte("integration-only-bundle-token-key"),
		OriginToken: secretKey, DefaultMemoryMB: 256, DefaultMVCpu: 250,
	}, nil, WithPollInterval(5*time.Second))
	if err != nil {
		t.Fatalf("New runtime: %v", err)
	}
	appID := time.Now().UnixNano() & 0x3fffffffffffffff
	h := &integrationHarness{
		runtime: rt, sdk: sdk, token: secretKey,
		params: process.StartParams{
			Slug: fmt.Sprintf("serverless-it-%d", appID), AppID: appID, Index: 2,
			Tier: "serverless", Port: port, MemoryLimitMB: 256, CPUQuotaPercent: 25,
			DeploymentID: 99, AppVersion: "integration",
		},
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := rt.CleanupApp(ctx, appID); err != nil {
			t.Errorf("cleanup provider resources: %v", err)
		}
	})
	return h
}

func (h *integrationHarness) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ep, err := h.runtime.Start(ctx, h.params, io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.endpoint = ep
}

func (h *integrationHarness) probeHTTP(t *testing.T) {
	t.Helper()
	client := &http.Client{Transport: h.runtime.ReplicaTransportForWorker(WorkerID), Timeout: 2 * time.Minute}
	deadline := time.Now().Add(10 * time.Minute)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(h.endpoint.URL + "/")
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK && strings.Contains(string(body), "shinyhub-serverless-probe") {
				return
			}
			lastErr = fmt.Errorf("status %s, body %q, read error %v", resp.Status, body, readErr)
		} else {
			lastErr = err
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("private HTTP endpoint did not become healthy: %v", lastErr)
}

func (h *integrationHarness) probeWebSocket(t *testing.T) {
	t.Helper()
	conn := h.dialWebSocket(t)
	defer conn.Close()
	if err := websocket.Message.Send(conn, "shinyhub-probe"); err != nil {
		t.Fatalf("websocket send: %v", err)
	}
	var reply string
	if err := websocket.Message.Receive(conn, &reply); err != nil {
		t.Fatalf("websocket receive: %v", err)
	}
	if reply != "shinyhub-probe" {
		t.Fatalf("websocket reply = %q", reply)
	}
}

func (h *integrationHarness) dialWebSocket(t *testing.T) *websocket.Conn {
	t.Helper()
	endpoint, err := url.Parse(h.endpoint.URL)
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	endpoint.Scheme = map[string]string{"http": "ws", "https": "wss"}[endpoint.Scheme]
	endpoint.Path = "/ws"
	cfg, err := websocket.NewConfig(endpoint.String(), h.endpoint.URL)
	if err != nil {
		t.Fatalf("websocket config: %v", err)
	}
	cfg.Header.Set("X-Auth-Token", h.token)
	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("websocket upgrade: %v", err)
	}
	return conn
}

func requiredIntegrationEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Skipf("real Scaleway integration requires %s", name)
	}
	return value
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
