// cmd/shinyhub/worker.go
package main

import (
	"context"
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
		caFile        string
		pythonImage   string
		rImage        string
		networkMode   string
	)
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Run ShinyHub as a remote worker that joins a control plane",
		RunE: func(cmd *cobra.Command, args []string) error {
			if serverURL == "" {
				return fmt.Errorf("--server is required")
			}
			// --token is required only for a fresh join; a worker with a valid
			// persisted identity re-adopts it without one (Bootstrap enforces the
			// token on the register path), so a worker can restart after its join
			// token has been rotated away.
			if caFile == "" {
				caFile = os.Getenv("SHINYHUB_WORKER_CA")
			}
			var caPEM []byte
			if caFile != "" {
				b, err := os.ReadFile(caFile)
				if err != nil {
					return fmt.Errorf("read ca file: %w", err)
				}
				caPEM = b
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
				CAPEM:         caPEM,
			})
			if err != nil {
				return fmt.Errorf("worker bootstrap: %w", err)
			}
			slog.Info("worker joined control plane", "node_id", ag.NodeID(), "tier", tier)
			rt, err := process.NewDockerRuntime(dockerSocket, pythonImage, rImage, networkMode)
			if err != nil {
				return fmt.Errorf("docker runtime: %w", err)
			}
			replicas := worker.NewReplicaServer(worker.ReplicaServerConfig{
				Runtime:   rt,
				DataDir:   dataDir,
				NodeID:    ag.NodeID(),
				Advertise: advertiseAddr,
				Bundles:   ag.Bundles(),
			})
			if err := replicas.RebuildFromContainers(); err != nil {
				slog.Warn("agent: rebuild data-plane table from containers", "err", err)
			}
			agentSrv := worker.NewAgentServer(worker.AgentServerConfig{
				ListenAddr: advertiseAddr,
				CertSource: ag.Certs(),
				CASource:   ag.CACerts(),
				NodeID:     ag.NodeID(),
				Replicas:   replicas,
			})
			ag.Listen = agentSrv.Listen
			ag.Serve = agentSrv.ServeListener
			// A fenced heartbeat means the control plane reaped this node's prior
			// incarnation and reassigned its work elsewhere; stop every replica
			// this process still runs before adopting the new incarnation.
			ag.OnFenced = replicas.StopAll
			return ag.Run(ctx, 10*time.Second)
		},
	}
	cmd.Flags().StringVar(&serverURL, "server", "", "control-plane base URL (https://host:port)")
	cmd.Flags().StringVar(&token, "token", "", "join token")
	cmd.Flags().StringVar(&advertiseAddr, "advertise-addr", "", "address the control plane dials this worker on (host:port)")
	cmd.Flags().StringVar(&tier, "tier", "", "tier this worker serves")
	cmd.Flags().StringVar(&dataDir, "data-dir", "./worker-data", "worker-local data root")
	cmd.Flags().StringVar(&name, "name", "", "optional human-readable worker name")
	cmd.Flags().StringVar(&dockerSocket, "docker-socket", config.DefaultDockerSocket, "Docker socket for the local runtime")
	cmd.Flags().StringVar(&caFile, "ca-file", "", "path to the control plane's CA certificate (PEM) to trust at join; overrides SHINYHUB_WORKER_CA. Required for the internal self-signed CA; omit only when the worker API is fronted by a publicly trusted certificate")
	cmd.Flags().StringVar(&pythonImage, "python-image", config.DefaultPythonImage, "Docker image used to run Python/Shiny apps")
	cmd.Flags().StringVar(&rImage, "r-image", config.DefaultRImage, "Docker image used to run R/Shiny apps")
	cmd.Flags().StringVar(&networkMode, "network-mode", config.DefaultNetworkMode, "Docker network mode for app containers (bridge or host)")
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
// hosting is enabled. Returns the CA, registry, and worker API so the control
// plane can build the mTLS dialer, resolve tier-to-node identity, refresh the
// registry on lease acquire, and wire the ownership-and-readiness gate.
func startWorkerHosting(ctx context.Context, logger *slog.Logger, cfg *config.Config, store *db.Store) (*worker.CA, *worker.Registry, *api.WorkerAPI, error) {
	tokens, err := readJoinTokens(cfg.Worker.JoinTokenFile)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("worker join tokens: %w", err)
	}
	ca, err := worker.LoadOrInitCA(store, cfg.Worker.CADir, cfg.Auth.Secret, tokens)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("worker CA: %w", err)
	}
	reg, err := worker.NewRegistry(store)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("worker registry: %w", err)
	}
	workerAPI := api.NewWorkerAPI(store, reg, ca, cfg.Storage.AppsDir)
	// Reject worker mutations until this instance is the elected, ready owner.
	// runServe upgrades this to the elector-and-ready predicate once the elector
	// exists; starting in reject mode means a booting standby never accepts a
	// register/heartbeat while another instance holds the lease.
	workerAPI.SetOwnership(func() bool { return false })
	go serveWorkerMTLS(ctx, logger, cfg, ca, workerAPI)
	return ca, reg, workerAPI, nil
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

	tlsConf, err := ca.ListenerTLSConfig(cfg.Worker.AdvertiseHosts...)
	if err != nil {
		logger.Error("worker mTLS: listener tls config", "err", err)
		return
	}
	srv := &http.Server{
		Addr: workerListenAddr(cfg),
		// bodyLimitHandler caps request bodies (register decodes its body before
		// the join-token check, so this bound applies pre-authentication); the
		// timeouts bound slow-header/slow-body clients so a peer that only
		// completes the TLS handshake cannot pin connections. WriteTimeout is left
		// unset so large bundle-fetch responses stream without being severed.
		Handler:           bodyLimitHandler(r),
		TLSConfig:         tlsConf,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		MaxHeaderBytes:    1 << 20,
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
