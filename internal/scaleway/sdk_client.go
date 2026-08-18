package scaleway

import (
	"context"
	"errors"

	container "github.com/scaleway/scaleway-sdk-go/api/container/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

type containerAPI interface {
	CreateContainer(*container.CreateContainerRequest, ...scw.RequestOption) (*container.Container, error)
	UpdateContainer(*container.UpdateContainerRequest, ...scw.RequestOption) (*container.Container, error)
	GetContainer(*container.GetContainerRequest, ...scw.RequestOption) (*container.Container, error)
	ListContainers(*container.ListContainersRequest, ...scw.RequestOption) (*container.ListContainersResponse, error)
	DeleteContainer(*container.DeleteContainerRequest, ...scw.RequestOption) (*container.Container, error)
}

// SDKClient adapts Scaleway's generated Serverless Containers v1 API to the
// narrow, context-aware Client boundary used by Runtime.
type SDKClient struct {
	api    containerAPI
	region scw.Region
}

func NewSDKClient(client *scw.Client, region string) *SDKClient {
	return &SDKClient{api: container.NewAPI(client), region: scw.Region(region)}
}

func newSDKClientWithAPI(api containerAPI) *SDKClient { return &SDKClient{api: api} }

func (c *SDKClient) CreateContainer(ctx context.Context, in CreateContainerInput) (Container, error) {
	req := &container.CreateContainerRequest{
		Region:                     scw.Region(in.Region),
		NamespaceID:                in.NamespaceID,
		Name:                       in.Name,
		EnvironmentVariables:       cloneMap(in.Environment),
		SecretEnvironmentVariables: cloneMap(in.SecretEnvironment),
		MinScale:                   ptr(uint32(in.MinScale)),
		MaxScale:                   ptr(uint32(in.MaxScale)),
		MemoryLimitBytes:           ptr(scw.Size(in.MemoryMB) * scw.MB),
		MvcpuLimit:                 ptr(uint32(in.MVCpu)),
		Timeout:                    scw.NewDurationFromTimeDuration(in.Timeout),
		Privacy:                    sdkPrivacy(in.Privacy),
		Image:                      in.Image,
		Protocol:                   container.ContainerProtocolHTTP1,
		Port:                       ptr(uint32(in.Port)),
		HTTPSConnectionsOnly:       ptr(true),
		Sandbox:                    container.ContainerSandboxV2,
		Tags:                       append([]string(nil), in.Tags...),
		PrivateNetworkID:           optionalString(in.PrivateNetworkID),
		Args:                       append([]string(nil), in.Args...),
	}
	out, err := c.api.CreateContainer(req, scw.WithContext(ctx))
	if err != nil {
		return Container{}, normalizeSDKError(err)
	}
	return fromSDKContainer(out), nil
}

func (c *SDKClient) UpdateContainer(ctx context.Context, in UpdateContainerInput) (Container, error) {
	tags := append([]string(nil), in.Tags...)
	args := append([]string(nil), in.Args...)
	env := cloneMap(in.Environment)
	secret := cloneMap(in.SecretEnvironment)
	req := &container.UpdateContainerRequest{
		Region:                     scw.Region(in.Region),
		ContainerID:                in.ID,
		EnvironmentVariables:       &env,
		SecretEnvironmentVariables: &secret,
		MinScale:                   ptr(uint32(in.MinScale)),
		MaxScale:                   ptr(uint32(in.MaxScale)),
		MemoryLimitBytes:           ptr(scw.Size(in.MemoryMB) * scw.MB),
		MvcpuLimit:                 ptr(uint32(in.MVCpu)),
		Timeout:                    scw.NewDurationFromTimeDuration(in.Timeout),
		Privacy:                    sdkPrivacy(in.Privacy),
		Image:                      ptr(in.Image),
		Protocol:                   container.ContainerProtocolHTTP1,
		Port:                       ptr(uint32(in.Port)),
		HTTPSConnectionOnly:        ptr(true),
		Sandbox:                    container.ContainerSandboxV2,
		Tags:                       &tags,
		PrivateNetworkID:           optionalString(in.PrivateNetworkID),
		Args:                       &args,
	}
	out, err := c.api.UpdateContainer(req, scw.WithContext(ctx))
	if err != nil {
		return Container{}, normalizeSDKError(err)
	}
	return fromSDKContainer(out), nil
}

func (c *SDKClient) GetContainer(ctx context.Context, id string) (Container, error) {
	out, err := c.api.GetContainer(&container.GetContainerRequest{
		Region: c.region, ContainerID: id,
	}, scw.WithContext(ctx))
	if err != nil {
		return Container{}, normalizeSDKError(err)
	}
	return fromSDKContainer(out), nil
}

func (c *SDKClient) ListContainers(ctx context.Context, in ListContainersInput) ([]Container, error) {
	namespaceID := in.NamespaceID
	projectID := in.ProjectID
	out, err := c.api.ListContainers(&container.ListContainersRequest{
		Region: scw.Region(in.Region), NamespaceID: &namespaceID, ProjectID: &projectID,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, normalizeSDKError(err)
	}
	result := make([]Container, 0, len(out.Containers))
	for _, item := range out.Containers {
		result = append(result, fromSDKContainer(item))
	}
	return result, nil
}

func (c *SDKClient) DeleteContainer(ctx context.Context, id string) error {
	_, err := c.api.DeleteContainer(&container.DeleteContainerRequest{
		Region: c.region, ContainerID: id,
	}, scw.WithContext(ctx))
	return normalizeSDKError(err)
}

func fromSDKContainer(in *container.Container) Container {
	if in == nil {
		return Container{}
	}
	errorMessage := ""
	if in.ErrorMessage != nil {
		errorMessage = *in.ErrorMessage
	}
	return Container{
		ID: in.ID, Name: in.Name, Status: in.Status.String(),
		ErrorMessage: errorMessage, PublicEndpoint: in.PublicEndpoint,
		Tags: append([]string(nil), in.Tags...),
	}
}

func sdkPrivacy(value string) container.ContainerPrivacy {
	if value == PrivacyPublic {
		return container.ContainerPrivacyPublic
	}
	return container.ContainerPrivacyPrivate
}

func normalizeSDKError(err error) error {
	if err == nil {
		return nil
	}
	var response *scw.ResponseError
	if errors.As(err, &response) && response.StatusCode == 404 {
		return ErrNotFound
	}
	var notFound *scw.ResourceNotFoundError
	if errors.As(err, &notFound) {
		return ErrNotFound
	}
	return err
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func ptr[T any](value T) *T { return &value }
