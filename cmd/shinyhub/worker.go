// cmd/shinyhub/worker.go
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker"
	"github.com/rvben/shinyhub/internal/worker/agent"
	"github.com/spf13/cobra"
)

func newWorkerCmd() *cobra.Command {
	var (
		serverURL     string
		token         string
		advertiseAddr string
		tier          string
		dataDir       string
		name          string
		dockerSocket  string
	)
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run ShinyHub as a remote worker that joins a control plane",
		RunE: func(cmd *cobra.Command, args []string) error {
			if serverURL == "" {
				return fmt.Errorf("--server is required")
			}
			if token == "" {
				return fmt.Errorf("--token is required")
			}
			ctx := cmd.Context()
			ag, err := agent.Bootstrap(ctx, agent.Config{
				ServerURL:     serverURL,
				Token:         token,
				AdvertiseAddr: advertiseAddr,
				Tier:          tier,
				DataDir:       dataDir,
				Name:          name,
				Version:       version,
			})
			if err != nil {
				return fmt.Errorf("worker bootstrap: %w", err)
			}
			slog.Info("worker joined control plane", "node_id", ag.NodeID(), "tier", tier)
			cfg, err := config.Load(serverConfigPath())
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			rt, err := process.NewDockerRuntime(dockerSocket,
				cfg.Runtime.Docker.Images.Python,
				cfg.Runtime.Docker.Images.R,
				cfg.Runtime.Docker.NetworkMode,
			)
			if err != nil {
				return fmt.Errorf("docker runtime: %w", err)
			}
			replicas := worker.NewReplicaServer(worker.ReplicaServerConfig{
				Runtime:   rt,
				DataDir:   dataDir,
				NodeID:    ag.NodeID(),
				Advertise: advertiseAddr,
			})
			agentSrv := worker.NewAgentServer(worker.AgentServerConfig{
				ListenAddr: advertiseAddr,
				ServerCert: ag.IssuedCert(),
				ClientCAs:  ag.CAPool(),
				NodeID:     ag.NodeID(),
				Replicas:   replicas,
			})
			ag.ServeFunc = agentSrv.Serve
			return ag.Run(ctx, 10*time.Second)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control-plane base URL (https://host:port)")
	cmd.Flags().StringVar(&token, "token", "", "join token")
	cmd.Flags().StringVar(&advertiseAddr, "advertise-addr", "", "address the control plane dials this worker on (host:port)")
	cmd.Flags().StringVar(&tier, "tier", "", "tier this worker serves")
	cmd.Flags().StringVar(&dataDir, "data-dir", "./worker-data", "worker-local data root")
	cmd.Flags().StringVar(&name, "name", "", "optional human-readable worker name")
	cmd.Flags().StringVar(&dockerSocket, "docker-socket", "/var/run/docker.sock", "Docker socket for the local runtime")
	return cmd
}

// readJoinTokens reads newline-separated join tokens from a file, ignoring blank
// lines. A missing file is an error: worker hosting was explicitly enabled.
func readJoinTokens(path string) ([]string, error) {
	if path == "" {
		return nil, fmt.Errorf("worker.join_token_file must be set when worker hosting is enabled")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read token file %s: %w", path, err)
	}
	var tokens []string
	for _, line := range strings.Split(string(b), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			tokens = append(tokens, s)
		}
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("no join tokens found in %s", path)
	}
	return tokens, nil
}

// workerListenAddr returns the configured worker-API listen address.
func workerListenAddr(cfg *config.Config) string {
	if cfg.Worker.ListenAddr != "" {
		return cfg.Worker.ListenAddr
	}
	return "0.0.0.0:8443"
}

// startWorkerHosting builds the CA, registry, and worker API and starts the
// dedicated mTLS listener in the background. Called from runServe when worker
// hosting is enabled. Returns the CA and registry so the control plane can
// build the mTLS dialer and resolve tier-to-node identity.
func startWorkerHosting(ctx context.Context, logger *slog.Logger, cfg *config.Config, store *db.Store) (*worker.CA, *worker.Registry, error) {
	tokens, err := readJoinTokens(cfg.Worker.JoinTokenFile)
	if err != nil {
		return nil, nil, fmt.Errorf("worker join tokens: %w", err)
	}
	ca, err := worker.OpenCA(cfg.Worker.CADir, tokens)
	if err != nil {
		return nil, nil, fmt.Errorf("worker CA: %w", err)
	}
	reg, err := worker.NewRegistry(store)
	if err != nil {
		return nil, nil, fmt.Errorf("worker registry: %w", err)
	}
	workerAPI := api.NewWorkerAPI(store, reg, ca, cfg.Storage.AppsDir)
	go serveWorkerMTLS(ctx, logger, cfg, ca, workerAPI)
	return ca, reg, nil
}

// serveWorkerMTLS serves the worker-facing API on a dedicated listener that
// requests (but does not require, so register can run pre-cert) client certs
// and verifies presented ones against the CA. Per-handler auth enforces cert
// identity on heartbeat and bundle fetch.
func serveWorkerMTLS(ctx context.Context, logger *slog.Logger, cfg *config.Config, ca *worker.CA, wapi *api.WorkerAPI) {
	r := chi.NewRouter()
	r.Post("/api/workers/register", wapi.HandleRegister)
	r.Post("/api/workers/heartbeat", wapi.HandleHeartbeat)
	r.Get("/internal/bundles/{digest}", wapi.HandleBundleFetch)

	srvCert, err := ca.ServerCertificate(cfg.Worker.AdvertiseHosts...)
	if err != nil {
		logger.Error("worker mTLS: server cert", "err", err)
		return
	}
	srv := &http.Server{
		Addr:    workerListenAddr(cfg),
		Handler: r,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{srvCert},
			ClientAuth:   tls.VerifyClientCertIfGiven,
			ClientCAs:    ca.Pool(),
		},
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Warn("worker mTLS shutdown", "err", err)
		}
	}()
	logger.Info("worker mTLS API listening", "addr", srv.Addr)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("worker mTLS server", "err", err)
	}
}
