package scaleway

import (
	"context"
	"testing"
	"time"

	container "github.com/scaleway/scaleway-sdk-go/api/container/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

type fakeSDKAPI struct {
	created *container.CreateContainerRequest
}

func (f *fakeSDKAPI) CreateContainer(req *container.CreateContainerRequest, _ ...scw.RequestOption) (*container.Container, error) {
	f.created = req
	return &container.Container{
		ID: "id", Name: req.Name, Status: container.ContainerStatusReady,
		PublicEndpoint: "https://private.example", Tags: req.Tags,
	}, nil
}
func (*fakeSDKAPI) UpdateContainer(*container.UpdateContainerRequest, ...scw.RequestOption) (*container.Container, error) {
	panic("unexpected UpdateContainer")
}
func (*fakeSDKAPI) GetContainer(*container.GetContainerRequest, ...scw.RequestOption) (*container.Container, error) {
	panic("unexpected GetContainer")
}
func (*fakeSDKAPI) ListContainers(*container.ListContainersRequest, ...scw.RequestOption) (*container.ListContainersResponse, error) {
	panic("unexpected ListContainers")
}
func (*fakeSDKAPI) DeleteContainer(*container.DeleteContainerRequest, ...scw.RequestOption) (*container.Container, error) {
	panic("unexpected DeleteContainer")
}

func TestSDKClientMapsCreateToContainersV1(t *testing.T) {
	api := &fakeSDKAPI{}
	client := newSDKClientWithAPI(api)
	in := CreateContainerInput{
		Region: "nl-ams", NamespaceID: "ns", Name: "demo-a42-r0", Image: "rg/image:tag",
		Environment:       map[string]string{"PUBLIC": "value"},
		SecretEnvironment: map[string]string{"TOKEN": "secret"},
		MinScale:          0, MaxScale: 1, MemoryMB: 512, MVCpu: 250,
		Timeout: time.Hour, Privacy: PrivacyPrivate, Port: 8080,
		Tags: []string{"shinyhub.managed=true"}, PrivateNetworkID: "pn-id",
		Args: []string{"uv", "run", "app.py"},
	}
	out, err := client.CreateContainer(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	if out.ID != "id" || out.PublicEndpoint == "" {
		t.Fatalf("container = %+v", out)
	}
	req := api.created
	if req == nil {
		t.Fatal("SDK CreateContainer was not called")
	}
	if req.Region != scw.RegionNlAms || req.Privacy != container.ContainerPrivacyPrivate || req.Protocol != container.ContainerProtocolHTTP1 || req.Sandbox != container.ContainerSandboxV2 {
		t.Fatalf("provider settings = region %q privacy %q protocol %q sandbox %q", req.Region, req.Privacy, req.Protocol, req.Sandbox)
	}
	if req.MinScale == nil || *req.MinScale != 0 || req.MaxScale == nil || *req.MaxScale != 1 {
		t.Fatalf("scale request = %v..%v", req.MinScale, req.MaxScale)
	}
	if req.MemoryLimitBytes == nil || *req.MemoryLimitBytes != 512*scw.MB || req.MvcpuLimit == nil || *req.MvcpuLimit != 250 {
		t.Fatalf("resource request = %v/%v", req.MemoryLimitBytes, req.MvcpuLimit)
	}
	if req.Timeout == nil || *req.Timeout.ToTimeDuration() != time.Hour {
		t.Fatalf("timeout = %#v", req.Timeout)
	}
	if req.SecretEnvironmentVariables["TOKEN"] != "secret" || req.PrivateNetworkID == nil || *req.PrivateNetworkID != "pn-id" {
		t.Fatalf("secrets/private network = %#v/%v", req.SecretEnvironmentVariables, req.PrivateNetworkID)
	}
}
