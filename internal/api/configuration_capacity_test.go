package api

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

func TestConfigurationHostCPUCapacity(t *testing.T) {
	for _, tc := range []struct {
		name, mode, socket string
		clustered, remote  bool
		want               bool
	}{
		{name: "native", mode: "native", want: true},
		{name: "docker", mode: "docker", socket: "unix:///var/run/docker.sock", want: true},
		{name: "remote docker socket", mode: "docker", socket: "tcp://worker:2376"},
		{name: "managed", mode: "fargate"},
		{name: "cluster", mode: "native", clustered: true},
		{name: "worker registry", mode: "native", remote: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{Runtime: config.RuntimeConfig{Mode: tc.mode, Docker: config.DockerRuntimeConfig{Socket: tc.socket}}}, clustered: tc.clustered, hostCapacity: &HostCapacity{Cores: 8}}
			if tc.remote {
				s.nodeForTier = func(string) string { return "worker" }
			}
			got := s.configurationHostCPUCores(&db.App{})
			if tc.want {
				if got == nil || *got != 8 {
					t.Fatalf("capacity=%v", got)
				}
			} else if got != nil {
				t.Fatalf("controller capacity leaked: %v", *got)
			}
			s.hostCapacity = nil
			if s.configurationHostCPUCores(&db.App{}) != nil {
				t.Fatal("missing capacity must stay unknown")
			}
		})
	}
	s := &Server{cfg: &config.Config{Runtime: config.RuntimeConfig{Mode: "native", Tiers: []config.TierConfig{{Name: "remote", Runtime: "remote-docker"}}}}, hostCapacity: &HostCapacity{Cores: 8}}
	if s.configurationHostCPUCores(&db.App{}) != nil {
		t.Fatal("remote tier must not use control-plane capacity")
	}
}
