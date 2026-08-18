package scaleway

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/process/runtimetest"
)

type contractClient struct {
	mu         sync.Mutex
	next       int
	containers map[string]Container
}

func newContractClient() *contractClient {
	return &contractClient{containers: make(map[string]Container)}
}

func (f *contractClient) CreateContainer(_ context.Context, in CreateContainerInput) (Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	id := fmt.Sprintf("container-%d", f.next)
	c := Container{ID: id, Name: in.Name, Status: StatusReady, Tags: in.Tags,
		PublicEndpoint: "https://" + id + ".functions.fnc.nl-ams.scw.cloud"}
	f.containers[id] = c
	return c, nil
}

func (f *contractClient) UpdateContainer(_ context.Context, in UpdateContainerInput) (Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[in.ID]
	if !ok {
		return Container{}, ErrNotFound
	}
	c.Status = StatusReady
	c.Tags = in.Tags
	f.containers[in.ID] = c
	return c, nil
}

func (f *contractClient) GetContainer(_ context.Context, id string) (Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return Container{}, ErrNotFound
	}
	return c, nil
}

func (f *contractClient) ListContainers(context.Context, ListContainersInput) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Container, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, c)
	}
	return out, nil
}

func (f *contractClient) DeleteContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[id]; !ok {
		return ErrNotFound
	}
	delete(f.containers, id)
	return nil
}

func TestManagedRuntimeContract(t *testing.T) {
	runtimetest.ManagedRuntime(t, Provider, WorkerID, func(t *testing.T) (process.ManagedRuntime, process.StartParams) {
		t.Helper()
		rt, err := New(newContractClient(), testConfig(), nil, WithPollInterval(time.Millisecond))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return rt, testStartParams()
	})
}
