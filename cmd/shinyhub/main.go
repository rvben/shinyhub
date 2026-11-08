package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3files"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/activation"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/autoscale"
	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/cloudlogs"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/data"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fargate"
	"github.com/rvben/shinyhub/internal/favicon"
	"github.com/rvben/shinyhub/internal/history"
	"github.com/rvben/shinyhub/internal/identity"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/leader"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/localtoken"
	"github.com/rvben/shinyhub/internal/logging"
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/rungroup"
	"github.com/rvben/shinyhub/internal/safego"
	"github.com/rvben/shinyhub/internal/sandbox"
	scalewayruntime "github.com/rvben/shinyhub/internal/scaleway"
	"github.com/rvben/shinyhub/internal/schedulespec"
	"github.com/rvben/shinyhub/internal/secrets"
	"github.com/rvben/shinyhub/internal/servertrace"
	"github.com/rvben/shinyhub/internal/tracing"
	"github.com/rvben/shinyhub/internal/ui"
	"github.com/rvben/shinyhub/internal/upgrade"
	"github.com/rvben/shinyhub/internal/usage"
	"github.com/rvben/shinyhub/internal/worker"
	"github.com/rvben/shinyhub/internal/workloadmetrics"
	"github.com/scaleway/scaleway-sdk-go/scw"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/hkdf"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
// It defaults to "dev" for local builds. Propagated to internal/cli via
// cli.SetVersion in init().
var version = "dev"

var rootCmd = &cobra.Command{
	Use:     "shinyhub",
	Short:   "ShinyHub — self-hosted platform for deploying and managing Shiny apps",
	Version: version,
	Long: "ShinyHub is a self-hosted platform for deploying and managing Shiny apps.\n\n" +
		"Environment variables:\n" +
		"  SHINYHUB_HOST           Server URL; overrides the saved host.\n" +
		"  SHINYHUB_TOKEN          API key or deploy token; overrides the saved token.\n" +
		"                          Set HOST and TOKEN together to skip `shinyhub connect` (CI).\n" +
		"  SHINYHUB_CREDENTIALS    Client credentials path (default ~/.config/shinyhub/config.json).\n" +
		"  SHINYHUB_CONFIG         Legacy client alias; server configuration path for server commands.\n" +
		"  SHINYHUB_DEPLOY_TOKEN   (server) Pre-shared deploy token; clients pass it as SHINYHUB_TOKEN.\n\n" +
		"Run `shinyhub completion install` for shell completions or `shinyhub schema` for the machine-readable contract.",
	SilenceErrors: true,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the ShinyHub server",
	RunE: func(cmd *cobra.Command, args []string) error {
		explicitConfig := cmd.Flags().Changed("config") || os.Getenv("SHINYHUB_CONFIG") != ""
		setup, err := maybeRunInteractiveSetup(cmd)
		if err != nil {
			return err
		}
		if explicitConfig {
			setup = nil
		}
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		environment, err := config.ServerEnvironment(serverConfigPath(), os.Environ())
		if err != nil {
			return err
		}
		config.ApplyServerEnvironment(environment)
		logger := logging.New()
		slog.SetDefault(logger)
		if serveSupervise {
			cfg, err := config.LoadForMaintenance(serverConfigPath())
			if err != nil {
				return err
			}
			hup := make(chan os.Signal, 1)
			signal.Notify(hup, syscall.SIGHUP)
			defer signal.Stop(hup)
			err = upgrade.Supervise(ctx, []string{os.Args[0], "serve", "--config", serverConfigPath(), "--no-browser"}, os.Environ(), cfg.Server.PIDFile, hup, logger)
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return &cli.ExitCodeError{Code: exit.ExitCode(), Err: err}
			}
			return err
		}
		return runServe(ctx, logger, serveOptions{
			FirstRun:  setup,
			NoBrowser: serveNoBrowser,
			ErrOut:    cmd.ErrOrStderr(),
		})
	},
}

var (
	backupOut      string
	serveNoBrowser bool
	serveSupervise bool
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Write a consistent snapshot of all durable state to an archive",
	Long: "Creates a .tar.gz containing a transactionally consistent database\n" +
		"snapshot (SQLite VACUUM INTO, or pg_dump for Postgres) plus the apps\n" +
		"and app-data dirs. Safe to run on a live server. Postgres backups\n" +
		"require pg_dump on PATH.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadForMaintenance(serverConfigPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if err := backup.Create(cfg, version, backupOut); err != nil {
			return err
		}
		return cli.RenderAction(cmd, "written",
			map[string]any{"path": backupOut},
			fmt.Sprintf("backup written to %s", backupOut))
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore <archive>",
	Short: "Restore durable state from a backup archive (server must be stopped)",
	Long: "Restores the database, apps, and app-data from a backup archive.\n" +
		"Stop the server first. Existing state is preserved, never deleted, so\n" +
		"you can roll back: SQLite files and the app trees are moved aside with\n" +
		"a '.pre-restore-<timestamp>' suffix; for Postgres the current database\n" +
		"is dumped to 'pre-restore-<timestamp>.dump' beside the archive before\n" +
		"pg_restore loads the backup. Postgres restores require pg_dump and\n" +
		"pg_restore on PATH.\n\n" +
		"Refuses to run while a server is detected: one publishes\n" +
		"'<database>-running.json' beside its database file while it runs, and\n" +
		"a live server.pid_file or a listener on server.host:server.port count\n" +
		"too. Pass --force for a server you have confirmed stopped by other\n" +
		"means.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadForMaintenance(serverConfigPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		force, _ := cmd.Flags().GetBool("force")
		moved, err := backup.RestoreForce(cfg, args[0], force)
		for _, p := range moved {
			fmt.Fprintf(cmd.ErrOrStderr(), "previous state preserved at %s\n", p)
		}
		var running *backup.ServerRunningError
		if errors.As(err, &running) {
			// A server that is up is something the operator can fix, not a
			// fault to retry, and the remedy they can reach from here is the
			// flag rather than the Go API behind it.
			return cli.ValidationError(running.Error(),
				"stop the server, or pass --force once you have confirmed it is stopped")
		}
		if err != nil {
			return err
		}
		return cli.RenderAction(cmd, "restored",
			map[string]any{"archive": args[0]},
			"restore complete; start the server to apply any pending migrations")
	},
}

var rotateSecretCmd = &cobra.Command{
	Use:   "rotate-secret",
	Short: "Re-encrypt all at-rest secrets under a new auth.secret",
	Long: "Re-encrypts every stored app-env secret and the worker CA private key\n" +
		"from the current auth.secret to a new one, in a single transaction. Run\n" +
		"with the server STOPPED. The current (old) secret is read from the config\n" +
		"/ SHINYHUB_AUTH_SECRET; the new secret from SHINYHUB_NEW_AUTH_SECRET.\n\n" +
		"On success, set SHINYHUB_AUTH_SECRET to the new value and start the\n" +
		"server. If the command fails, nothing is changed (atomic), so it is safe\n" +
		"to retry. Without this, changing auth.secret leaves every stored secret\n" +
		"undecryptable.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(serverConfigPath()) // requires the CURRENT (old) auth.secret
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		oldSecret := cfg.Auth.Secret
		newSecret := os.Getenv("SHINYHUB_NEW_AUTH_SECRET")
		if err := validateRotationSecret(oldSecret, newSecret); err != nil {
			return err
		}

		store, err := db.Open(cfg.Database.DSN)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer store.Close()

		oldEnv, newEnv := secrets.DeriveKey(oldSecret), secrets.DeriveKey(newSecret)
		oldCA, newCA := worker.CAKeyEncKey(oldSecret), worker.CAKeyEncKey(newSecret)
		reEnv := func(old []byte) ([]byte, error) {
			p, derr := secrets.Decrypt(oldEnv, old)
			if derr != nil {
				return nil, fmt.Errorf("decrypt env secret with the current auth.secret: %w", derr)
			}
			return secrets.Encrypt(newEnv, p)
		}
		reCA := func(old []byte) ([]byte, error) {
			p, derr := secrets.Decrypt(oldCA, old)
			if derr != nil {
				return nil, fmt.Errorf("decrypt worker CA key with the current auth.secret: %w", derr)
			}
			return secrets.Encrypt(newCA, p)
		}
		oldUsage := usage.UsagePseudonymEncryptionKey(oldSecret)
		newUsage := usage.UsagePseudonymEncryptionKey(newSecret)
		reUsage := func(old []byte) ([]byte, error) {
			p, derr := secrets.Decrypt(oldUsage, old)
			if derr != nil {
				return nil, fmt.Errorf("decrypt usage pseudonym key with the current auth.secret: %w", derr)
			}
			return secrets.Encrypt(newUsage, p)
		}

		n, caRotated, usageRotated, err := store.RotateSecretsTx(reEnv, reCA, reUsage)
		if err != nil {
			return fmt.Errorf("rotation failed (no changes committed - safe to retry): %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"Rotated %d app-env secret(s)%s%s.\nNext: set SHINYHUB_AUTH_SECRET to the new value and start the server.\n",
			n, caRotatedSuffix(caRotated), usageRotatedSuffix(usageRotated))
		return cli.RenderAction(cmd, "rotated",
			map[string]any{"env_secrets": n, "worker_ca_rotated": caRotated, "usage_pseudonym_key_rotated": usageRotated},
			fmt.Sprintf("rotated %d env secret(s); worker_ca_rotated=%v; usage_pseudonym_key_rotated=%v", n, caRotated, usageRotated))
	},
}

func caRotatedSuffix(rotated bool) string {
	if rotated {
		return " and the worker CA key"
	}
	return ""
}

func usageRotatedSuffix(rotated bool) string {
	if rotated {
		return " and the usage pseudonym key"
	}
	return ""
}

// validateRotationSecret enforces the same floor as auth.secret and refuses a
// no-op rotation.
func validateRotationSecret(oldSecret, newSecret string) error {
	switch {
	case newSecret == "":
		return fmt.Errorf("set SHINYHUB_NEW_AUTH_SECRET to the new secret (generate one with: openssl rand -hex 32)")
	case newSecret == "change-me-to-a-random-string":
		return fmt.Errorf("SHINYHUB_NEW_AUTH_SECRET is the placeholder value; generate a strong one with: openssl rand -hex 32")
	case len(newSecret) < 32:
		return fmt.Errorf("SHINYHUB_NEW_AUTH_SECRET must be at least 32 characters (got %d)", len(newSecret))
	case newSecret == oldSecret:
		return fmt.Errorf("SHINYHUB_NEW_AUTH_SECRET is identical to the current auth.secret; nothing to rotate")
	}
	return nil
}

var migrateTargetDSN string

var resolveLegacyWritersAck bool

var resolveLegacyWritersCmd = &cobra.Command{
	Use:   "resolve-legacy-schedule-writers",
	Short: "Resolve the fail-closed fence left by schedule writers from an older server",
	Long: "Lists schedule processes captured during the provenance upgrade. Run this only\n" +
		"with every old ShinyHub server stopped and after verifying that every listed\n" +
		"job process and descendant is gone. With --acknowledge-processes-stopped, the\n" +
		"command atomically interrupts the legacy rows, records conservative data-write\n" +
		"uncertainty, and clears the startup fence. A successful fenced producer rerun is\n" +
		"then required before consumers can start.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadForMaintenance(serverConfigPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		store, err := db.Open(cfg.Database.DSN)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer store.Close()

		snap, err := backup.PreMigrationSnapshot(cfg, store, time.Now())
		if err != nil {
			return fmt.Errorf("pre-migration snapshot: %w", err)
		}
		if snap.PruneErr != "" {
			slog.Warn("prune old pre-migration snapshots failed", "err", snap.PruneErr)
		}
		if err := store.Migrate(); err != nil {
			return fmt.Errorf("migrate database: %w", err)
		}
		// This acknowledgement mutates durable safety evidence. Apply the same
		// downgrade guard as serve before even listing/resolving markers: an older
		// recovery binary must never reinterpret a schema it does not understand.
		if err := store.VerifySchemaCompatibility(); err != nil {
			return fmt.Errorf("schema compatibility: %w", err)
		}
		legacy, err := store.ListLegacyUnfencedScheduleRuns()
		if err != nil {
			return err
		}
		if len(legacy) == 0 {
			return cli.RenderAction(cmd, "clear", map[string]any{"legacy_schedule_writers": 0},
				"no legacy schedule writers require resolution")
		}
		for _, run := range legacy {
			fmt.Fprintf(cmd.ErrOrStderr(), "app=%s schedule=%s run_id=%d status=%s\n",
				run.AppSlug, run.ScheduleName, run.RunID, run.RunStatus)
		}
		if !resolveLegacyWritersAck {
			return fmt.Errorf("%d legacy schedule writer(s) remain fenced; stop every old server, verify all listed process trees are gone, then rerun with --acknowledge-processes-stopped", len(legacy))
		}
		resolved, err := store.ResolveLegacyUnfencedScheduleRuns()
		if err != nil {
			return err
		}
		return cli.RenderAction(cmd, "resolved",
			map[string]any{"legacy_schedule_writers": resolved, "producer_repair_required": true},
			fmt.Sprintf("resolved %d legacy schedule writer(s); producer repair is required before consumers can start", resolved))
	},
}

var migrateBackendCmd = &cobra.Command{
	Use:   "migrate-backend",
	Short: "Copy all data from the current SQLite database to a fresh Postgres database",
	Long: "One-time migration from single-node SQLite to a Postgres backend (the\n" +
		"prerequisite for HA). Run with the server STOPPED. The SOURCE is the\n" +
		"SQLite database in your config; the TARGET Postgres DSN is given via --to\n" +
		"or SHINYHUB_TARGET_DSN. The target must be empty: it is migrated to the\n" +
		"current schema, then every row is copied - IDs and references preserved -\n" +
		"in a single transaction. If anything fails, nothing is committed. On\n" +
		"success, point the server's database.dsn at the Postgres DSN and start it.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadForMaintenance(serverConfigPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		if db.IsPostgresDSN(cfg.Database.DSN) {
			return fmt.Errorf("source is already Postgres (config database.dsn); migrate-backend goes SQLite -> Postgres")
		}
		target := migrateTargetDSN
		if target == "" {
			target = os.Getenv("SHINYHUB_TARGET_DSN")
		}
		if target == "" {
			return fmt.Errorf("set the target Postgres DSN with --to <dsn> or SHINYHUB_TARGET_DSN")
		}
		if !db.IsPostgresDSN(target) {
			return fmt.Errorf("target must be a Postgres DSN (postgres://...)")
		}

		src, err := db.Open(cfg.Database.DSN)
		if err != nil {
			return fmt.Errorf("open source (SQLite): %w", err)
		}
		defer src.Close()

		dst, err := db.Open(target)
		if err != nil {
			return fmt.Errorf("open target (Postgres): %w", err)
		}
		defer dst.Close()
		if err := dst.Migrate(); err != nil {
			return fmt.Errorf("migrate target schema: %w", err)
		}

		counts, err := dst.ImportFrom(src)
		if err != nil {
			return fmt.Errorf("migration failed (nothing committed - safe to retry): %w", err)
		}
		total := 0
		for _, n := range counts {
			total += n
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"Copied %d rows across %d tables.\nNext: point the server's database.dsn at the Postgres DSN and start it.\n",
			total, len(counts))
		return cli.RenderAction(cmd, "migrated",
			map[string]any{"tables": len(counts), "rows": total},
			fmt.Sprintf("migrated %d rows across %d tables", total, len(counts)))
	},
}

// configPath holds the value of the --config flag (empty when unset). Bound on
// serve, backup, and restore so a config file can be selected without an env var.
var configPath string

// serverConfigPath resolves the server config file. Precedence: the --config
// flag, then the SHINYHUB_CONFIG env var, then ./shinyhub.yaml.
func serverConfigPath() string {
	if configPath != "" {
		return configPath
	}
	if v := os.Getenv("SHINYHUB_CONFIG"); v != "" {
		return v
	}
	return "shinyhub.yaml"
}

func init() {
	cli.SetVersion(version)
	reloadCmd.Flags().DurationVar(&reloadWait, "wait", 0, "Wait for temporary transitions before handoff (0..24h); readiness separately waits server.upgrade_timeout + 1m")
	serveCmd.Flags().BoolVar(&serveSupervise, "supervise", false, "Keep a stable systemd supervisor across reloads (Linux 5.3+; requires server.pid_file)")
	serveCmd.Flags().BoolVar(&serveNoBrowser, "no-browser", false, "Do not open a browser after interactive first-run setup")
	backupCmd.Flags().StringVar(&backupOut, "out", "", "Destination archive path (.tar.gz)")
	_ = backupCmd.MarkFlagRequired("out")
	migrateBackendCmd.Flags().StringVar(&migrateTargetDSN, "to", "", "Target Postgres DSN (overrides SHINYHUB_TARGET_DSN)")
	resolveLegacyWritersCmd.Flags().BoolVar(&resolveLegacyWritersAck, "acknowledge-processes-stopped", false,
		"Confirm every old server and listed job process tree is stopped, then record uncertainty and clear the fence")
	restoreCmd.Flags().Bool("force", false, "Restore even if a running server is detected (only after confirming the server is stopped)")
	const configUsage = "Path to the server config file (overrides SHINYHUB_CONFIG; default ./shinyhub.yaml)"
	for _, c := range []*cobra.Command{serveCmd, statusCmd, reloadCmd, validateConfigCmd, backupCmd, restoreCmd, rotateSecretCmd, migrateBackendCmd, resolveLegacyWritersCmd} {
		c.Flags().StringVar(&configPath, "config", "", configUsage)
	}
}

var buildRootOnce sync.Once

// buildRoot wires the complete command tree: server-side commands plus the
// client CLI. The sync.Once guard makes it safe for main() and any number of
// tests to call; registration happens exactly once per process.
func buildRoot() *cobra.Command {
	buildRootOnce.Do(func() {
		rootCmd.AddCommand(newOutputPumpCmd(), newLocalTokenCmd(), newRelayProtocolCmd(), newInitCmd(), serveCmd, statusCmd, reloadCmd, validateConfigCmd, backupCmd, restoreCmd, rotateSecretCmd, migrateBackendCmd, resolveLegacyWritersCmd, newHealthcheckCmd(), newWorkerCmd(), newSandboxCmd())
		cli.AddCommandsTo(rootCmd)
	})
	return rootCmd
}

// newSandboxCmd is the hidden re-exec shim the native runtime launches apps
// through when isolation is enabled: it applies the sandbox spec from the
// environment, then execs the real command that follows "--". It is not a
// user-facing command.
func newSandboxCmd() *cobra.Command {
	return &cobra.Command{
		Use:                sandbox.ShimCommand,
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sandbox.RunShim(args)
		},
	}
}

func main() {
	if err := buildRoot().Execute(); err != nil {
		os.Exit(cli.Report(err))
	}
}

// listenFunc constructs a listener; injected so the metrics listener can be
// routed through the upgrader (for zero-downtime handoff) or a fake in tests.
type listenFunc func(network, addr string) (net.Listener, error)

// preflightAddrFree reports early that addr can still be bound, so a port
// conflict is named before the expensive part of startup rather than after it.
// It is deliberately not the authority: the socket opened here is closed again
// immediately and the serving listener is still acquired later through the
// upgrader, which keeps accept behaviour during startup exactly as it was (a
// client connecting before the server serves is refused, not parked in a
// backlog). Something else can take the address in between; that case still
// fails at the real bind, with the same message it always did.
//
// An upgrade child is skipped: it inherits its listeners from a parent that is
// still bound, so probing would report a conflict with the very process this
// one is replacing and abort every zero-downtime handoff.
func preflightAddrFree(upg upgrade.Upgrader, label, addr string) error {
	if upg.HasParent() {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot bind %s %s: %w (is another ShinyHub already running here?)", label, addr, err)
	}
	return ln.Close()
}

// startMetricsListener binds addr via listen and serves the Prometheus scrape
// endpoint at /metrics on its own listener, separate from the main application
// listener so server internals are never exposed on the public port. The
// returned server is already serving in a background goroutine; the caller is
// responsible for Shutdown. The listener is returned so callers can log the
// resolved address (useful when addr requests an ephemeral :0 port).
// addrIsLoopback reports whether a is a TCP address on a loopback IP. Used to
// gate pprof so the profiling endpoints are never served on a non-loopback (and
// thus potentially publicly reachable) metrics interface. A non-TCP or
// wildcard (0.0.0.0 / ::) bind is treated as not loopback.
func addrIsLoopback(a net.Addr) bool {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return false
	}
	return ta.IP.IsLoopback()
}

func startMetricsListener(listen listenFunc, addr string, reg *metrics.Registry) (*http.Server, net.Listener, error) {
	ln, err := listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("metrics listen %s: %w", addr, err)
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", reg.Handler())
	// pprof exposes process internals (cmdline, heap/goroutine dumps, CPU/trace
	// profiling) with no authentication, so mount it ONLY when the metrics
	// listener is bound to a loopback address. An operator who binds metrics to a
	// non-loopback interface for remote Prometheus scraping must not also expose
	// profiling there. The server sets no WriteTimeout, so the default 30s CPU
	// profile completes.
	if addrIsLoopback(ln.Addr()) {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	} else {
		slog.Warn("pprof disabled: metrics listener is not bound to loopback", "addr", ln.Addr().String())
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	handoffLn := newHandoffListener(ln, srv)
	go func() {
		defer handoffLn.ServeDone()
		if err := srv.Serve(handoffLn); err != nil && !errors.Is(err, http.ErrServerClosed) && !handoffLn.Closing() {
			slog.Error("metrics server", "err", err)
		}
	}()
	return srv, handoffLn, nil
}

// probeWritable confirms dir is writable by creating and removing a temp file.
// os.MkdirAll succeeds on an existing-but-unwritable directory (e.g. wrong owner
// left by a prior install), so without this probe the first deploy would fail
// later with a cryptic permission error instead of a clear startup failure.
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".shinyhub-writeprobe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

type appLogMaintenanceMetrics interface {
	RecordAppLogRunsPruned(count int64)
	RecordAppLogFilesPruned(count int)
}

// runMaintenance periodically prunes database history and reconciles immutable
// local log files. It runs on the owner instance only, promptly and then on the
// configured interval.
func runMaintenance(ctx context.Context, store *db.Store, manager *process.Manager, telemetry appLogMaintenanceMetrics, cfg config.MaintenanceConfig, usageCfg config.UsageConfig) {
	auditRetention := time.Duration(cfg.AuditRetentionDays) * 24 * time.Hour
	usageRawRetention := time.Duration(usageCfg.RawRetentionDays) * 24 * time.Hour
	usageAggregateRetention := time.Duration(usageCfg.AggregateRetentionDays) * 24 * time.Hour
	keepRuns := cfg.ScheduleRunRetentionCount
	keepAppLogs := cfg.AppLogRunRetentionCount
	keepFleetRuns := cfg.FleetRunRetentionCount
	developmentSessionRetention := time.Duration(cfg.DevelopmentSessionRetentionDays) * 24 * time.Hour
	// The database-backed login limiter only writes rows on a Postgres backend,
	// so its ledger only needs sweeping there. Retention must comfortably exceed
	// the longest limiter window (login uses 1m) so an in-window row is never
	// dropped; raise it if a longer-window bucket is ever added.
	pruneRateLimits := store.IsPostgres()
	const rateLimitRetention = time.Hour
	if auditRetention <= 0 && usageRawRetention <= 0 && usageAggregateRetention <= 0 && keepRuns <= 0 &&
		keepAppLogs <= 0 && keepFleetRuns <= 0 && developmentSessionRetention <= 0 && !pruneRateLimits &&
		!usageCfg.Enabled {
		return
	}

	prune := func() {
		if auditRetention > 0 {
			if n, err := store.PruneAuditEvents(auditRetention); err != nil {
				slog.Warn("prune_audit_events_failed", "err", err)
			} else if n > 0 {
				slog.Info("pruned_audit_events", "removed", n, "retention_days", cfg.AuditRetentionDays)
			}
		}
		// Finalizing stale (crashed, heartbeat-silent) sessions must run whenever
		// usage tracking is on, regardless of retention: an unfinalized session
		// never enters the usage_closed_daily fast path (its trigger only fires
		// once ended_at is set), so leaving it open forever would force every
		// future usage report to keep rescanning it even with retention disabled.
		if usageCfg.Enabled {
			if n, err := store.FinalizeStaleUsageSessions(); err != nil {
				slog.Warn("finalize_stale_usage_sessions_failed", "err", err)
			} else if n > 0 {
				slog.Info("finalized_stale_usage_sessions", "closed", n)
			}
		}
		if usageRawRetention > 0 {
			if n, err := store.PruneUsageSessions(usageRawRetention); err != nil {
				slog.Warn("prune_usage_sessions_failed", "err", err)
			} else if n > 0 {
				slog.Info("pruned_usage_sessions", "removed", n, "retention_days", usageCfg.RawRetentionDays)
			}
		}
		if usageAggregateRetention > 0 {
			if n, err := store.PruneUsageDaily(usageAggregateRetention); err != nil {
				slog.Warn("prune_usage_daily_failed", "err", err)
			} else if n > 0 {
				slog.Info("pruned_usage_daily", "removed", n, "retention_days", usageCfg.AggregateRetentionDays)
			}
		}
		if keepRuns > 0 {
			if n, err := store.PruneScheduleActivations(keepRuns); err != nil {
				slog.Warn("prune_schedule_activations_failed", "err", err)
			} else if n > 0 {
				slog.Info("pruned_schedule_activations", "removed", n, "keep_terminal_per_schedule", keepRuns)
			}
			ids, err := store.ListAllScheduleIDs()
			if err != nil {
				slog.Warn("prune_schedule_runs_list_failed", "err", err)
				return
			}
			var total int64
			for _, id := range ids {
				n, err := store.PruneScheduleRuns(id, keepRuns)
				if err != nil {
					slog.Warn("prune_schedule_runs_failed", "schedule_id", id, "err", err)
					continue
				}
				total += n
			}
			if total > 0 {
				slog.Info("pruned_schedule_runs", "removed", total, "keep_per_schedule", keepRuns)
			}
		}
		if keepAppLogs > 0 {
			n, err := store.PruneAppLogRuns(keepAppLogs)
			if err != nil {
				slog.Warn("prune_app_log_runs_failed", "err", err)
			} else {
				if n > 0 {
					slog.Info("pruned_app_log_runs", "removed", n, "keep_per_replica", keepAppLogs)
					if telemetry != nil {
						telemetry.RecordAppLogRunsPruned(n)
					}
				}
				reconcileLocalAppLogFiles(store, manager, telemetry)
			}
		}
		if pruneRateLimits {
			if n, err := store.PruneRateLimitCounters(rateLimitRetention); err != nil {
				slog.Warn("prune_rate_limit_counters_failed", "err", err)
			} else if n > 0 {
				slog.Info("pruned_rate_limit_counters", "removed", n)
			}
		}
		if keepFleetRuns > 0 {
			if n, err := store.PruneFleetRuns(keepFleetRuns); err != nil {
				slog.Warn("prune_fleet_runs_failed", "err", err)
			} else if n > 0 {
				slog.Info("pruned_fleet_runs", "removed", n, "keep_per_fleet", keepFleetRuns)
			}
		}
		if developmentSessionRetention > 0 {
			if n, err := store.PruneDevelopmentSessions(developmentSessionRetention); err != nil {
				slog.Warn("prune_development_sessions_failed", "err", err)
			} else if n > 0 {
				slog.Info("pruned_development_sessions", "removed", n, "retention_days", cfg.DevelopmentSessionRetentionDays)
			}
		}
	}

	prune()
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

func reconcileLocalAppLogFiles(store *db.Store, manager *process.Manager, telemetry appLogMaintenanceMetrics) {
	if manager == nil {
		return
	}
	retained, err := store.ListAppLogRunIDs()
	if err != nil {
		slog.Warn("list_retained_app_log_runs_failed", "err", err)
		return
	}
	removed, err := manager.PruneLogRunFiles(retained)
	if err != nil {
		slog.Warn("prune_app_log_files_failed", "removed", removed, "err", err)
	} else if removed > 0 {
		slog.Info("pruned_app_log_files", "removed", removed)
		if telemetry != nil {
			telemetry.RecordAppLogFilesPruned(removed)
		}
	}
}

// runSQLiteMaintenance keeps a file-backed SQLite database compact: promptly
// and then on every interval it refreshes the planner's statistics, then
// moves the write-ahead log into the database file and truncates the log.
// Statistics come first so the rows they write are part of what gets
// checkpointed and the pass ends with an empty log. The automatic checkpoint
// never shrinks the log file, so without this pass an idle server keeps the
// WAL of its busiest hour. Other backends return at once.
func runSQLiteMaintenance(ctx context.Context, store *db.Store, interval time.Duration) {
	if !store.IsSQLiteFile() {
		return
	}
	if interval <= 0 {
		interval = time.Hour
	}
	maintain := func() {
		if err := store.Optimize(); err != nil {
			slog.Warn("sqlite_optimize_failed", "err", err)
		}
		if res, err := store.CheckpointWAL(); err != nil {
			slog.Warn("sqlite_wal_checkpoint_failed", "err", err)
		} else if res.Blocked {
			slog.Warn("sqlite_wal_checkpoint_blocked", "pending_frames", res.PendingFrames)
		} else {
			slog.Debug("sqlite_wal_truncated")
		}
	}
	maintain()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			maintain()
		}
	}
}

// runLocalLogMaintenance reconciles every control-plane node's private disk.
// Database deletion remains owner-only, but a former owner may retain files on
// a standby; each node therefore removes database-orphaned files independently.
func runLocalLogMaintenance(ctx context.Context, store *db.Store, manager *process.Manager, telemetry appLogMaintenanceMetrics, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	reconcileLocalAppLogFiles(store, manager, telemetry)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileLocalAppLogFiles(store, manager, telemetry)
		}
	}
}

// isClustered reports whether cfg is using the Postgres backend, which means
// multiple control-plane instances may share the same database. The check
// reuses the same scheme-prefix dispatch that db.Open uses to pick a backend,
// so the two can never disagree.
func isClustered(cfg *config.Config) bool {
	return db.IsPostgresDSN(cfg.Database.DSN)
}

// checkClusteredRuntimeTiers returns an error when a clustered deployment
// (Postgres DSN) includes a local-only tier (native or docker). Local runtimes
// bind loopback ports on a single host and cannot be reached by other CP
// instances, so they must be refused at boot with a clear message directing the
// operator to use an off-host tier (remote_docker or a managed cloud runtime).
//
// Single-node deployments (SQLite) are unaffected: native and docker tiers
// continue to work unchanged.
func checkClusteredRuntimeTiers(cfg *config.Config) error {
	if !isClustered(cfg) {
		return nil
	}
	for _, tier := range cfg.Runtime.Tiers {
		if tier.Runtime == "native" || tier.Runtime == "docker" {
			return fmt.Errorf(
				"tier %q uses runtime %q, which is not supported in a clustered deployment "+
					"(Postgres DSN detected); use an off-host runtime (remote_docker, fargate, or scaleway_serverless) instead",
				tier.Name, tier.Runtime,
			)
		}
	}
	return nil
}

// buildRuntime constructs a process.Runtime for a single tier from its TierConfig.
// Docker tiers share the daemon settings from cfg.Runtime.Docker; a burst tier
// may therefore point at the same daemon under a distinct tier name. Managed
// provider tiers share their provider-level configuration.
// config.Load validates tier modes, so the default case is unreachable in production.
func buildRuntime(ctx context.Context, tier config.TierConfig, cfg *config.Config, bundleTokenKey []byte) (process.Runtime, error) {
	switch tier.Runtime {
	case "docker":
		dockerRT, err := process.NewDockerRuntime(
			cfg.Runtime.Docker.Socket,
			cfg.Runtime.Docker.Images.Python,
			cfg.Runtime.Docker.Images.R,
			cfg.Runtime.Docker.NetworkMode,
		)
		if err != nil {
			return nil, fmt.Errorf("docker runtime: %w", err)
		}
		// Enable warm-wake (freeze + cgroup reclaim) when configured. When
		// disabled, Suspend reports not-supported and the watcher hibernates via
		// Stop exactly as before.
		dockerRT.SetSnapshot(cfg.Runtime.Snapshot.Enabled, cfg.Runtime.Snapshot.ReclaimMinFraction)
		return dockerRT, nil
	case "native":
		if cfg.Runtime.Native.BrokerSocket != "" {
			rt, err := process.NewSystemdRuntime(ctx, cfg.Runtime.Native.BrokerSocket)
			if err != nil {
				return nil, fmt.Errorf("native user isolation: %w", err)
			}
			rt.SetSnapshot(cfg.Runtime.Snapshot.Enabled, cfg.Runtime.Snapshot.ReclaimMinFraction)
			if err := rt.ValidateStorage(cfg.Storage.AppsDir, cfg.Storage.AppDataDir, cfg.Storage.AppCacheDir); err != nil {
				return nil, fmt.Errorf("native user isolation: %w", err)
			}
			if !db.IsPostgresDSN(cfg.Database.DSN) {
				if err := rt.ValidateControlDatabase(cfg.Database.DSN); err != nil {
					return nil, fmt.Errorf("native user isolation: %w", err)
				}
			}
			for _, path := range []string{serverConfigPath(), cfg.Auth.SecretFile, cfg.Server.EnvFile} {
				if err := rt.ValidateControlPath(path); err != nil {
					return nil, fmt.Errorf("native user isolation: %w", err)
				}
			}
			if err := rt.PrepareStorage(ctx); err != nil {
				return nil, fmt.Errorf("native user isolation: prepare storage: %w", err)
			}
			process.SetHostCommandRunner(rt.RunBuild)
			slog.Info("native backend selected", "tier", tier.Name, "backend", "isolated_systemd", "boundary", "per_app_uid")
			return rt, nil
		}
		slog.Warn("native apps run as the controller user; deploy only trusted app code, or configure runtime.native.broker_socket for separate app users", "tier", tier.Name, "backend", "trusted_shared_uid")
		nativeRT := process.NewNativeRuntime()
		// Enable warm-wake (SIGSTOP freeze + per-app cgroup reclaim) when
		// configured. If the delegated cgroup subtree cannot be prepared at
		// runtime (no systemd Delegate=memory, no cgroup v2, or no swap), the
		// runtime degrades gracefully: Suspend reports not-supported and the
		// watcher hibernates via Stop exactly as before.
		nativeRT.SetSnapshot(cfg.Runtime.Snapshot.Enabled, cfg.Runtime.Snapshot.ReclaimMinFraction)
		nativeRT.SetOutputPump(os.Args[0])
		nativeRT.SetIsolation(sandbox.Level(cfg.Runtime.Native.Isolation))
		return nativeRT, nil
	case "fargate":
		return buildFargateRuntime(ctx, cfg, tier, bundleTokenKey)
	case "scaleway_serverless":
		return buildScalewayRuntime(cfg, bundleTokenKey)
	case "remote_docker":
		// Handled upstream: remote tiers are registered via NewRemoteRuntime before
		// RegisterRuntime; buildRuntime is not called for remote_docker tiers.
		return nil, fmt.Errorf("buildRuntime called for remote_docker tier; wire via NewRemoteRuntime instead")
	default:
		return nil, fmt.Errorf("unsupported runtime mode: %s", tier.Runtime)
	}
}

// deriveBundleTokenKey derives the 32-byte key used to mint and verify managed
// runtime bundle tokens. Key material comes from the same auth secret as all other
// HKDF derivations, but with a distinct info string so the bundle key is
// independent of the secrets-encryption key. Panics on failure (HKDF read of
// 32 bytes is infallible).
func deriveBundleTokenKey(authSecret string) []byte {
	r := hkdf.New(sha256.New, []byte(authSecret), nil, []byte("shinyhub-fargate-bundle-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		panic(err)
	}
	return key
}

// deriveStickyCookieKey derives the HMAC key that signs the proxy's
// sticky-routing cookie, independent of the other HKDF-derived keys via its own
// info string. Panics on failure (HKDF read of 32 bytes is infallible).
func deriveStickyCookieKey(authSecret string) []byte {
	r := hkdf.New(sha256.New, []byte(authSecret), nil, []byte("shinyhub-sticky-cookie-v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(r, key); err != nil {
		panic(err)
	}
	return key
}

// buildFargateRuntime constructs the ECS runtime for one fargate tier from the
// shared cfg.Runtime.Fargate settings and the tier's per-entry launch type.
// The launch type determines the workerID ("fargate" or "ecs-ec2") and whether
// PlatformVersion is set on RunTask. config.Load has already validated the
// required Fargate fields.
func buildFargateRuntime(ctx context.Context, cfg *config.Config, tier config.TierConfig, bundleTokenKey []byte) (process.Runtime, error) {
	fc := cfg.Runtime.Fargate
	var optFns []func(*awsconfig.LoadOptions) error
	if fc.Region != "" {
		optFns = append(optFns, awsconfig.WithRegion(fc.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("load aws config for fargate: %w", err)
	}

	// Resolve the SDK launch type from the tier's string value.
	lt := ecstypes.LaunchTypeFargate
	if tier.LaunchType == "EC2" {
		lt = ecstypes.LaunchTypeEc2
	}

	var opts []fargate.Option
	if fc.RouteViaPublicIP {
		// Out-of-VPC control plane: resolve task public IPs via EC2.
		opts = append(opts, fargate.WithEC2Client(fargate.NewEC2Client(ec2.NewFromConfig(awsCfg))))
	}
	if fc.SecretsNamePrefix != "" {
		// Route apps' secret env vars through AWS Secrets Manager (referenced by
		// ARN from a per-app task-def secrets block) instead of plaintext task
		// overrides, so they never appear in ecs:DescribeTasks.
		opts = append(opts, fargate.WithSecretsStore(
			fargate.NewSecretsManagerStore(secretsmanager.NewFromConfig(awsCfg), fc.SecretsKMSKeyID)))
	}
	if fc.S3Files.Configured() && fc.S3Files.AccessPointArn == "" {
		// Managed durable-data backend without an access point: the runtime
		// pre-creates each app's directory in the linked bucket (a non-existent
		// per-app rootDirectory fails to mount). Needs GetFileSystem + PutObject.
		opts = append(opts,
			fargate.WithS3FilesDescriber(fargate.NewS3FilesDescriber(s3files.NewFromConfig(awsCfg))),
			fargate.WithObjectPutter(fargate.NewObjectPutter(s3.NewFromConfig(awsCfg))))
	}
	return fargate.New(fargate.NewECSClient(ecs.NewFromConfig(awsCfg)), fargate.Config{
		Cluster:          fc.Cluster,
		TaskDefinition:   fc.TaskDefinition,
		ContainerName:    fc.ContainerName,
		Subnets:          fc.Subnets,
		SecurityGroups:   fc.SecurityGroups,
		AssignPublicIP:   fc.AssignPublicIP,
		PlatformVersion:  fc.PlatformVersion,
		RouteViaPublicIP: fc.RouteViaPublicIP,
		TaskCPUUnits:     int32(fc.TaskCPUUnits), // operator-configured task ceiling
		TaskMemoryMB:     int32(fc.TaskMemoryMB), // operator-configured task ceiling
		ControlPlaneURL:  fc.ControlPlaneURL,
		BundleTokenTTL:   fc.BundleTokenTTL,
		BundleTokenKey:   bundleTokenKey,
		LaunchType:       lt,
		SecretNamePrefix: fc.SecretsNamePrefix,
		// DurableData is the operator's manual assertion; S3Files is the managed
		// backend. TierHasDurableData is true when either is set.
		DurableData: fc.DurableData,
		S3Files: fargate.S3FilesMount{
			FileSystemArn:         fc.S3Files.FileSystemArn,
			RootDirectory:         fc.S3Files.RootDirectory,
			AccessPointArn:        fc.S3Files.AccessPointArn,
			TransitEncryptionPort: int32(fc.S3Files.TransitEncryptionPort),
			MountPath:             fc.S3Files.MountPath,
		},
	}, slog.Default(), opts...), nil
}

// buildScalewayRuntime constructs a scale-to-zero runtime backed by Scaleway
// Serverless Containers v1. Each replica keeps one private provider resource;
// Scaleway suspends its instance after idle time and wakes it on the next proxy
// request. The secret key authenticates both SDK calls and private-origin HTTP.
func buildScalewayRuntime(cfg *config.Config, bundleTokenKey []byte) (process.Runtime, error) {
	accessKey := strings.TrimSpace(os.Getenv("SCW_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("SCW_SECRET_KEY"))
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("scaleway serverless runtime requires SCW_ACCESS_KEY and SCW_SECRET_KEY")
	}

	sc := cfg.Runtime.Scaleway
	client, err := scw.NewClient(
		scw.WithAuth(accessKey, secretKey),
		scw.WithDefaultProjectID(sc.ProjectID),
		scw.WithDefaultRegion(scw.Region(sc.Region)),
	)
	if err != nil {
		return nil, fmt.Errorf("create scaleway client: %w", err)
	}
	return scalewayruntime.New(scalewayruntime.NewSDKClient(client, sc.Region), scalewayruntime.Config{
		Region:           sc.Region,
		ProjectID:        sc.ProjectID,
		NamespaceID:      sc.NamespaceID,
		Image:            sc.Image,
		NamePrefix:       sc.NamePrefix,
		ControlPlaneURL:  sc.ControlPlaneURL,
		BundleTokenKey:   bundleTokenKey,
		BundleTokenTTL:   sc.BundleTokenTTL,
		OriginToken:      secretKey,
		PrivateNetworkID: sc.PrivateNetworkID,
		DefaultMemoryMB:  sc.DefaultMemoryMB,
		DefaultMVCpu:     sc.DefaultMVCpu,
		DurableData:      sc.DurableData,
	}, slog.Default())
}

// appResourceCleaners fans app deletion out to every configured managed cloud
// backend. All cleanups are attempted so a transient provider failure cannot
// prevent another provider from releasing its resources.
type appResourceCleaners []process.AppResourceCleaner

func (cleaners appResourceCleaners) CleanupApp(ctx context.Context, appID int64) error {
	var errs []error
	for _, cleaner := range cleaners {
		if err := cleaner.CleanupApp(ctx, appID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// hostSampler samples PID-backed replicas (native) via gopsutil and reports
// PID-less replicas (docker/remote/fargate handles, whose RunHandle carries a
// ContainerID rather than a PID) as unmeasured without error: the zero Stats it
// returns carries a nil CPU rate, so they render as "not available" rather than
// as an idle app. Returning no error for the PID-less case is deliberate: the
// status endpoint treats a sampler error as a dead replica, so a running replica
// on a container/fargate tier must not be probed by PID (PID 0) and misreported
// as stopped.
type hostSampler struct{ gops process.GopsutilSampler }

func (h *hostSampler) Sample(handle process.RunHandle) (process.Stats, error) {
	if handle.PID == 0 {
		return process.Stats{}, nil
	}
	return h.gops.Sample(handle)
}

func runServe(ctx context.Context, logger *slog.Logger, serveOpts serveOptions) error {
	environment, envErr := config.ServerEnvironment(serverConfigPath(), os.Environ())
	if envErr != nil {
		return fmt.Errorf("load server environment: %w", envErr)
	}
	// Apply before creating any proxy, build command, or successor.
	config.ApplyServerEnvironment(environment)
	cfg, err := config.Load(serverConfigPath())
	if err != nil {
		return &cli.ExitCodeError{Code: 1, Kind: cli.KindValidation, Err: fmt.Errorf("load config: %w", err)}
	}

	// Record the configured build interpreter policy (build.python_preference,
	// build.python, build.python_install_mirror) so it is applied as the
	// outermost env layer of every native build (uv sync, the uv init/add
	// project-synthesis step), serve-time `uv run`, and host-side hooks -
	// authoritative over any per-app UV_PYTHON_*. Held in process memory rather
	// than exported to os.Environ, so a zero-downtime re-exec recomputes it from
	// the freshly loaded config instead of inheriting a stale value. Done before
	// the manager starts so no build can run without it.
	process.SetBuildInterpreterPolicy(cfg.Build.UVBuildEnv())
	if cfg.Build.IsActive() {
		slog.Info("serve: build interpreter policy",
			"python_preference", cfg.Build.PythonPreference,
			"python", cfg.Build.Python,
			"install_mirror_configured", cfg.Build.PythonInstallMirror != "")
	}

	if err := os.MkdirAll(cfg.Storage.AppsDir, 0o750); err != nil {
		return fmt.Errorf("create apps dir: %w", err)
	}
	if err := probeWritable(cfg.Storage.AppsDir); err != nil {
		return fmt.Errorf("apps dir %s is not writable: %w", cfg.Storage.AppsDir, err)
	}

	if err := os.MkdirAll(cfg.Storage.AppDataDir, 0o750); err != nil {
		return fmt.Errorf("create app-data dir: %w", err)
	}
	if err := probeWritable(cfg.Storage.AppDataDir); err != nil {
		return fmt.Errorf("app-data dir %s is not writable: %w", cfg.Storage.AppDataDir, err)
	}

	// Normalize the configured app-data dir to an absolute path once, here at
	// the call site, and pass the absolute value to every consumer (the
	// SharedMounts resolver, process.Manager.SetAppDataRoot, jobs.NewManager).
	// This guarantees that long-running apps and scheduled jobs agree on the
	// same on-disk location regardless of where the server's CWD ends up,
	// closing the bug where a relative storage.app_data_dir caused schedules
	// to write into <bundle>/data/<rel-path>/<slug>/ rather than the
	// persistent data dir.
	absAppDataDir, err := filepath.Abs(cfg.Storage.AppDataDir)
	if err != nil {
		return fmt.Errorf("resolve app data dir: %w", err)
	}

	sweepOrphanTempfiles(absAppDataDir)

	// Zero-downtime upgrades require stable listener addresses: tableflip matches
	// inherited sockets by network+addr, so an ephemeral port 0 would make the
	// successor bind a fresh random port and silently break the handoff. Parse the
	// numeric port rather than string-matching ":0" (which misses ":00" etc.).
	if cfg.Server.Port == 0 {
		return fmt.Errorf("server.port must be a fixed non-zero port for zero-downtime upgrades")
	}
	if cfg.Metrics.Enabled {
		_, mport, perr := net.SplitHostPort(cfg.Metrics.Addr)
		if perr != nil {
			return fmt.Errorf("metrics.addr %q must be host:port: %w", cfg.Metrics.Addr, perr)
		}
		if n, aerr := strconv.Atoi(mport); aerr != nil || n == 0 {
			return fmt.Errorf("metrics.addr must use a fixed non-zero port for zero-downtime upgrades, got %q", cfg.Metrics.Addr)
		}
	}

	// Zero-downtime upgrades: tableflip lets a SIGHUP re-exec the new binary and
	// inherit the listeners. All upg.Listen calls must precede upg.Ready().
	upg, err := upgrade.New(cfg.Server.UpgradeTimeout, cfg.Server.PIDFile)
	if err != nil {
		return fmt.Errorf("init upgrader: %w", err)
	}
	defer upg.(*upgrade.RepeatableUpgrader).Close()
	upg.(*upgrade.RepeatableUpgrader).SetEnvironment(func() ([]string, error) {
		next, err := config.LoadForMaintenance(serverConfigPath())
		if err != nil {
			return nil, err
		}
		if next.Server.PIDFile != cfg.Server.PIDFile {
			return nil, fmt.Errorf("server.pid_file cannot change during a handoff")
		}
		return config.ServerEnvironment(serverConfigPath(), os.Environ())
	})
	// Preflight the handoff's re-exec target now: a SIGHUP re-execs argv[0], and
	// an unresolvable argv[0] fails every upgrade attempt (the process keeps
	// serving, so the failure is safe but silent until upgrade time).
	if _, rerr := exec.LookPath(os.Args[0]); rerr != nil {
		logger.Warn("zero-downtime upgrade preflight: cannot resolve re-exec target; SIGHUP upgrades will fail and this process will keep serving",
			"argv0", os.Args[0], "err", rerr)
	}

	// SIGHUP -> upgrade; ctx cancel (SIGINT/SIGTERM) -> Stop (closes Exit).
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	defer signal.Stop(sighup)
	upgradeSignals := upgrade.WireSignals(ctx, upg, sighup, logger)

	// Fail fast on the commonest startup mistake, a second instance on a port
	// something else already holds. The serving bind is the last thing this
	// function does, so without this check the conflict is only discovered after
	// migrations, the pre-migration snapshot, admin bootstrap and the ownership
	// election have all run: a process that can never serve does real work on the
	// way to finding that out.
	preflight := []struct{ label, addr string }{
		{"server.port", fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)},
	}
	if cfg.Metrics.Enabled {
		preflight = append(preflight, struct{ label, addr string }{"metrics.addr", cfg.Metrics.Addr})
	}
	for _, p := range preflight {
		if err := preflightAddrFree(upg, p.label, p.addr); err != nil {
			return err
		}
	}

	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	// The database holds password and API-key hashes and the audit log; keep the
	// file owner-only. Best-effort and only for a real on-disk file (plain-path
	// DSN), not an in-memory or parameterised DSN.
	if !strings.Contains(cfg.Database.DSN, ":memory:") {
		if _, statErr := os.Stat(cfg.Database.DSN); statErr == nil {
			_ = os.Chmod(cfg.Database.DSN, 0o600)
		}
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Warn("store close", "err", err)
		}
	}()
	// Long-lived background services. The shutdown sequence stops each one at
	// the point it must be gone; this deferred Stop, which runs before the
	// store closes, joins whatever an early return left running.
	services := rungroup.New(slog.Default())
	defer services.Stop()
	// On the shared-user native runtime a deployed app runs as the same OS user as this
	// process, and a process environment is readable by any process of that
	// user. auth.secret signs every session token and derives the key
	// encrypting every app's secret env vars, so holding it in the environment
	// puts it within reach of the code it is meant to be secret from.
	if cfg.Auth.SecretSource == "env" && cfg.Runtime.Mode != "docker" && cfg.Runtime.Native.BrokerSocket == "" {
		logger.Warn("auth.secret comes from SHINYHUB_AUTH_SECRET, which any process running as this user can read (/proc/<pid>/environ, ps eww); native-runtime apps run as this user",
			"remedy", "write the secret to a mode-0600 file and set auth.secret_file (SHINYHUB_AUTH_SECRET_FILE), then unset SHINYHUB_AUTH_SECRET")
	}

	// Publish liveness beside the database, so `shinyhub restore` can tell this
	// server is up from the database path alone. Its other two signals both
	// depend on settings a maintenance invocation commonly omits, and omitting
	// them disables the guard without saying so.
	clearMarker, err := backup.PublishRuntimeMarker(cfg)
	if err != nil {
		return fmt.Errorf("publish runtime marker: %w", err)
	}
	defer clearMarker()
	// Copy the database aside before changing its schema, so a bad upgrade can
	// be rolled back by putting the old binary and this file back. A snapshot
	// that cannot be written aborts startup: migrating a database the operator
	// cannot recover is worse than not starting.
	snap, err := backup.PreMigrationSnapshot(cfg, store, time.Now())
	if err != nil {
		return fmt.Errorf("pre-migration snapshot: %w (set database.pre_migration_snapshot: false to skip it)", err)
	}
	if snap.PruneErr != "" {
		slog.Warn("prune old pre-migration snapshots failed", "err", snap.PruneErr,
			"remedy", "delete stale *.pre-migration-v*.sqlite files beside the database by hand")
	}
	switch {
	case snap.Path != "":
		slog.Info("pre-migration snapshot written", "path", snap.Path,
			"pending_migrations", len(snap.Pending),
			"retention", cfg.Database.PreMigrationSnapshotRetention)
	case len(snap.Pending) > 0 && store.IsPostgres():
		slog.Warn("applying migrations with no snapshot; take a pg_dump before upgrading a Postgres deployment",
			"pending_migrations", len(snap.Pending))
	case len(snap.Pending) > 0:
		slog.Info("applying migrations without a pre-migration snapshot",
			"pending_migrations", len(snap.Pending), "reason", snap.Skipped)
	}
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("db migrate: %w", err)
	}
	// Refuse to serve a database that a newer build already migrated past this
	// binary: the schema would carry columns/tables this code cannot read,
	// risking silent corruption. Fail fast with an actionable message instead.
	if err := store.VerifySchemaCompatibility(); err != nil {
		return fmt.Errorf("schema compatibility: %w", err)
	}
	if legacyRuns, err := store.CountLegacyUnfencedScheduleRuns(); err != nil {
		return fmt.Errorf("legacy schedule writer fence: %w", err)
	} else if legacyRuns > 0 {
		return fmt.Errorf("legacy schedule writer fence: %d schedule run(s) were running during the 0.12.x upgrade and may have surviving unfenced descendants; stop every old server, verify those process trees are gone, then run `shinyhub resolve-legacy-schedule-writers --acknowledge-processes-stopped` before restarting", legacyRuns)
	}
	if err := validateStoredProducerTopology(store, cfg.Runtime); err != nil {
		return fmt.Errorf("producer topology: %w", err)
	}

	var (
		workerCA  *worker.CA
		workerReg *worker.Registry
		workerAPI *api.WorkerAPI
	)
	if cfg.Worker.Enabled {
		ca, reg, wapi, err := startWorkerHosting(ctx, logger, cfg, store)
		if err != nil {
			return err
		}
		workerCA = ca
		workerReg = reg
		workerAPI = wapi
	}

	var dialer worker.AgentDialer
	if workerCA != nil {
		d, err := worker.NewMTLSDialer(workerCA.ControlClientCertificate, workerCA.Pool())
		if err != nil {
			return fmt.Errorf("control client cert: %w", err)
		}
		dialer = d
	}

	secretsKey := secrets.DeriveKey(cfg.Auth.Secret)
	bundleTokenKey := deriveBundleTokenKey(cfg.Auth.Secret)

	// readyCh is closed once HTTP listener is live. /readyz returns 503 until then.
	readyCh := make(chan struct{})

	if err := bootstrapAdminFromEnv(store, cfg.Auth.LocalLoginEnabled(), slog.Default()); err != nil {
		return err
	}

	// The built-in deployment identity always exists as an explicit service
	// account. The compatibility environment token is persisted only as a hash,
	// so it follows the same lifecycle and attribution path as managed service
	// credentials.
	sysUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, "developer")
	if err != nil {
		return fmt.Errorf("ensure deployment service account: %w", err)
	}
	if cfg.Auth.DeployToken != "" {
		if err := auth.ValidateDeployTokenFormat(cfg.Auth.DeployToken); err != nil {
			return fmt.Errorf("SHINYHUB_DEPLOY_TOKEN: %w", err)
		}
	}
	// Only the elected control-plane owner reconciles configuration-managed
	// credentials. In HA this prevents a restarting standby with stale rollout
	// configuration from rotating or deleting the cluster-wide credential.
	syncDeployCredential := func() error {
		if cfg.Auth.DeployToken == "" {
			return store.DeleteDeployCredential()
		}
		if _, err := store.SyncDeployCredential(sysUser.ID, auth.HashAPIKey(cfg.Auth.DeployToken),
			cfg.Auth.DeployTokenRole, cfg.Auth.DeployTokenApps); err != nil {
			return err
		}
		slog.Info("deploy token registered",
			"username", sysUser.Username,
			"role", cfg.Auth.DeployTokenRole,
			"apps", cfg.Auth.DeployTokenApps)
		if warning := auth.DeployTokenRoleWarning(cfg.Auth.DeployTokenRole); warning != "" {
			slog.Warn(warning)
		}
		return nil
	}

	// A local-login server without a password-backed administrator can render a
	// perfectly normal sign-in page while leaving the installation impossible to
	// administer. Refuse that deceptive half-start and point directly at the safe
	// recovery command. SSO-backed installations may provision an admin from IdP
	// group mappings, so they are allowed to begin empty.
	if err := ensureUsableFirstLogin(cfg, store, serverConfigPath()); err != nil {
		return err
	}

	// Refuse local-only runtimes in a clustered (Postgres) deployment before
	// constructing any tier. Native and docker processes bind loopback on one
	// host; other CP instances cannot reach them, so they are incompatible with
	// shared state across nodes.
	if err := checkClusteredRuntimeTiers(cfg); err != nil {
		return err
	}

	mgr, err := newProcessManager(ctx, processManagerDeps{
		cfg:            cfg,
		store:          store,
		workerReg:      workerReg,
		dialer:         dialer,
		bundleTokenKey: bundleTokenKey,
		secretsKey:     secretsKey,
		absAppDataDir:  absAppDataDir,
	})
	if err != nil {
		return err
	}

	// Tracing: shared ring buffer surfaced by the /traces handler. It is a
	// no-op when cfg.Tracing.Enabled is false.
	traceBuffer := tracing.NewBuffer(cfg.Tracing.RingBufferSize, time.Duration(cfg.Tracing.SlowRequestMS)*time.Millisecond)

	prx := proxy.New()
	prx.SetTracing(cfg.Tracing, traceBuffer)
	// Trust forwarding headers only from the configured upstream proxies; a
	// direct client's X-Forwarded-* / Forwarded values are stripped before the
	// request reaches an app backend.
	prx.SetTrustedProxies(cfg.TrustedProxyNets)
	// Sign the sticky-routing cookie so a client cannot forge it to pin a
	// replica and bypass the per-replica session cap.
	prx.SetStickySecret(deriveStickyCookieKey(cfg.Auth.Secret))
	// Explain a mid-session disconnect to the visitor. The app process cannot:
	// it does not know whether it was hibernated, redeployed, or killed.
	prx.SetStatusOverlay(cfg.Server.StatusOverlayEnabled())
	prx.SetSupportSessions(cfg.Auth.SupportSessions)
	// Give the visitor a way out of the app they opened. Without it an app page
	// is a dead end: it fills the tab and links nowhere else in the fleet.
	prx.SetAppNav(cfg.Server.AppNavEnabled(), appNavHomeURL(cfg))
	prx.SetBrowserSessions(cfg.Auth.BrowserSessionMaxAge(), cfg.Server.BaseURL)
	relayDir := filepath.Join(absAppDataDir, ".reload-relays")
	relayLeases := &upgrade.RelayLeases{Dir: relayDir}
	_, liveRelays, unknownRelays := relayLeases.Snapshot()
	relaySupported := !isClustered(cfg) && cfg.Runtime.Mode == "native"
	if (liveRelays > 0 || unknownRelays || cfg.Server.ReloadSessions == "preserve") && !relaySupported {
		return fmt.Errorf("session-preserving reloads require single-host native runtime")
	}
	relayEnabled := relaySupported && (cfg.Server.ReloadSessions == "preserve" || liveRelays > 0 || unknownRelays || upg.HasParent())
	var stopRelayWatch func()
	if relayEnabled {
		stopRelayWatch = relayLeases.Watch(ctx)
		defer stopRelayWatch()
		prx.SetRelayLeases(relayLeases)
	}

	prx.SetAnnouncements(true)
	// Give every app a stable tab identity without overriding a favicon the app
	// authored itself. The proxy only injects the fallback into HTML pages that
	// do not already declare rel=icon.
	prx.SetAppFavicon(true)
	prx.SetEnvironment(cfg.Branding.Environment)
	prx.SetDashboardURL(appNavHomeURL(cfg))
	// Compress WebSocket messages for backends that do not, which is every R
	// Shiny app: httpuv never negotiates permessage-deflate.
	prx.SetWebSocketCompression(cfg.Server.WebSocketCompressionEnabled())
	// Usage analytics run off the WebSocket lifecycle rather than raw request
	// counts. The recorder's bounded queue keeps database work off the serving
	// path and is stopped only after upgraded connections have drained, so normal
	// shutdown records their end times.
	usagePolicy, err := usage.LoadOrInitPolicy(store, cfg.Usage.IdentityMode, cfg.Auth.Secret)
	if err != nil {
		return fmt.Errorf("initialize usage privacy policy: %w", err)
	}
	var stopUsage func()
	var usageRecorder *usage.Recorder
	if cfg.Usage.Enabled {
		usageRecorder = usage.NewRecorderWithPolicy(store, cfg.Server.InstanceID, usagePolicy)
		usageService := services.Go("usage recorder", serviceStoreStopBudget, usageRecorder.Run)
		prx.SetUsageRecorder(usageRecorder)
		var stopOnce sync.Once
		stopUsage = func() {
			stopOnce.Do(func() {
				prx.SetUsageRecorder(nil)
				usageService.Stop()
			})
		}
		defer stopUsage()
		slog.Info("usage analytics enabled",
			"identity_mode", cfg.Usage.IdentityMode,
			"raw_retention_days", cfg.Usage.RawRetentionDays,
			"aggregate_retention_days", cfg.Usage.AggregateRetentionDays)
	}
	// Identity forwarding: the proxy injects the authenticated user's
	// identity headers + per-app signed token. The provider owns the groups
	// TTL cache and minting; the proxy holds no secret and no store.
	identityProvider := identity.NewProvider(cfg.Auth.Secret, store)
	prx.SetIdentityProvider(identityProvider.PayloadFor)
	// On single-node deployments there is no pool syncer, so pre-seed the
	// first-sync gate as satisfied. This keeps /readyz behaviour unchanged
	// from before the gate was added (a clustered deployment leaves it false
	// until the pool syncer marks it after its first pass).
	if !isClustered(cfg) {
		prx.MarkSynced()
	}
	// In clustered mode answer the per-app readiness probe from the DB replica
	// status so all instances agree, rather than from the locally-observed WS
	// handshake which only the instance that handled the connection sees.
	if isClustered(cfg) {
		prx.SetAppReadyFunc(func(slug string) bool {
			ok, err := store.AppHasRunningReplica(slug)
			if err != nil {
				slog.Warn("app readiness probe: AppHasRunningReplica failed", "slug", slug, "err", err)
			}
			return ok
		})
	}

	// In clustered deployments every instance runs a session reporter that
	// periodically upserts its local per-(app, replica) active-connection
	// counts into replica_sessions so other instances can aggregate fleet
	// load. Single-node deployments skip this entirely: no rows, no goroutine.
	var reporterService *rungroup.Service
	if isClustered(cfg) {
		flushCh := make(chan string, 16)
		prx.EnableImmediateFlush(flushCh)
		reporter := proxy.NewSessionReporter(prx, store, cfg.Server.InstanceID, flushCh)
		reporterService = services.Go("session reporter", serviceStoreStopBudget, reporter.Run)
		slog.Info("session reporter started",
			"interval", proxy.ReporterInterval,
			"stale_cutoff", proxy.ReplicaSessionStaleCutoff,
			"instance", cfg.Server.InstanceID)
	}

	srv := api.New(cfg, store, mgr, prx)
	defer srv.Close()
	srv.SetUsagePolicy(usagePolicy)
	srv.SetVersion(version)
	var retiringParentDone <-chan struct{}
	var releaseRetiringParentFence func()
	if upg.HasParent() {
		done := make(chan struct{})
		retiringParentDone = done
		fence := make(chan struct{})
		prx.SetRetiringParent(fence)
		var releaseOnce sync.Once
		releaseRetiringParentFence = func() { releaseOnce.Do(func() { close(fence) }) }
		services.Go("retiring parent wait", serviceMemoryStopBudget, func(wctx context.Context) {
			if err := upg.WaitForParent(wctx); err != nil {
				// Fail closed: an unconfirmed parent's sessions must not be
				// mistaken for zero. No store access in this service.
				if wctx.Err() == nil {
					logger.Error("cannot confirm retiring proxy exit; lifecycle changes remain fenced", "err", err)
				}
				return
			}
			relayLeases.Refresh()
			close(done)
		})
	}

	// In clustered deployments every instance runs a pool syncer that
	// reconciles the proxy's backend pools against the DB replica table.
	// This lets standbys serve off-host apps without relying on the local
	// placement registry. Single-node deployments skip this entirely: the
	// deploy fast-path and RecoverProcesses remain the sole pool-population
	// paths, byte-for-byte unchanged.
	var syncerService *rungroup.Service
	if isClustered(cfg) {
		transportBuilder := worker.NewReplicaTransportBuilder(dialer, store)
		syncer := proxy.NewPoolSyncer(prx, store, transportBuilder, slog.Default(), cfg.Auth.IdentityHeadersEnabled())
		syncer.SetReplicaValidator(lifecycle.ReplicaRouteValidator(store, mgr))
		syncer.SetReconcileGuard(srv.TryAcquireFleetReconciliation)
		syncerService = services.Go("pool syncer", serviceStoreStopBudget, syncer.Run)
		// Wire the on-miss sync hook so a first-request for a freshly-started
		// app is served immediately without waiting for the next background tick.
		// It binds to the syncer's context so stopping the syncer ends it too.
		syncerCtx := syncerService.Context()
		prx.SetOnMissSync(func(slug string) {
			syncer.SyncSlug(syncerCtx, slug)
		})
		slog.Info("pool syncer started", "interval", proxy.PoolSyncInterval)
	}

	providerLogs := process.ExternalLogReaders{}
	if cfg.Runtime.Native.BrokerSocket != "" {
		providerLogs["systemd_journal"] = process.NewJournalLogReader(cfg.Runtime.Native.BrokerSocket)
	}
	if fc := cfg.Runtime.Fargate; fc.Cluster != "" {
		var opts []func(*awsconfig.LoadOptions) error
		if fc.Region != "" {
			opts = append(opts, awsconfig.WithRegion(fc.Region))
		}
		if awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...); err != nil {
			slog.Warn("CloudWatch application logs unavailable; using AWS console handoff", "err", err)
		} else {
			providerLogs["aws_ecs"] = cloudlogs.NewSDKReader(cloudwatchlogs.NewFromConfig(awsCfg), awsCfg.Region)
		}
	}
	if len(providerLogs) > 0 {
		srv.SetExternalLogReader(providerLogs)
	}
	if isClustered(cfg) {
		srv.SetCluster(cfg.Server.InstanceID)
	}
	srv.SetSecretsKey(secretsKey)
	srv.SetTraceBuffer(traceBuffer)

	// Wire retained provider-resource cleanup into both app deletion and startup
	// tombstone reconciliation. Provider configuration is shared across tiers,
	// so one cleaner per runtime mode covers all tiers without duplicate API calls.
	var managedResourceCleaners appResourceCleaners
	seenCleanerMode := make(map[string]bool)
	for _, tierName := range cfg.Runtime.TierOrder() {
		mode, _ := cfg.Runtime.RuntimeForTier(tierName)
		if seenCleanerMode[mode] {
			continue
		}
		cleaner, ok := mgr.RuntimeForTier(tierName).(process.AppResourceCleaner)
		if !ok {
			continue
		}
		managedResourceCleaners = append(managedResourceCleaners, cleaner)
		seenCleanerMode[mode] = true
	}
	if len(managedResourceCleaners) > 0 {
		srv.SetSecretsCleaner(managedResourceCleaners)
	}
	if workerReg != nil {
		srv.SetNodeForTier(func(tier string) string {
			if w, ok := workerReg.WorkerForTier(tier); ok {
				return w.NodeID
			}
			return ""
		})
		srv.SetWorkerRegistry(workerReg)
	}

	// Server self-telemetry: when enabled, instrument the API router and serve
	// the Prometheus scrape endpoint on its own loopback-by-default listener.
	var metricsSrv *http.Server
	var metricsLn net.Listener
	var metricsReg *metrics.Registry
	if cfg.Metrics.Enabled {
		reg := metrics.New(version)
		reg.RegisterDBStats(store.Stats)
		metricsReg = reg
		if usageRecorder != nil {
			usageRecorder.SetMetrics(reg)
		}
		srv.SetMetrics(reg)
		prx.SetRejectRecorder(reg)
		// Fleet gauges read live counts from the store at scrape time, so
		// "apps/replicas running right now" is answerable from Prometheus alone.
		reg.RegisterFleetGauges(
			func() float64 {
				n, err := store.CountRunningApps()
				if err != nil {
					return 0
				}
				return float64(n)
			},
			func() float64 {
				n, err := store.CountRunningReplicas()
				if err != nil {
					return 0
				}
				return float64(n)
			},
			func() float64 {
				n, err := store.CountCrashedApps()
				if err != nil {
					return 0
				}
				return float64(n)
			},
		)
		reg.RegisterRetiredGauges(func() (int, int64, bool) { st := relayLeases.Status(); return st.Count, st.Sessions, st.Unknown })
		// Schedule last-success gauge: DB-backed (restart-safe), one sample per
		// schedule that has ever succeeded.
		if err := reg.Register(metrics.NewScheduleFreshnessCollector(func() ([]metrics.ScheduleSample, error) {
			rows, qerr := store.ScheduleFreshness()
			if qerr != nil {
				return nil, qerr
			}
			out := make([]metrics.ScheduleSample, len(rows))
			for i, fr := range rows {
				s := metrics.ScheduleSample{
					Slug: fr.Slug, Name: fr.Name, Enabled: fr.Enabled,
					ConsecutiveFailures: fr.ConsecutiveFailures,
					ActivationStatus:    fr.ActivationStatus,
					CapacityDeferrals:   fr.ActivationCapacityDeferrals,
				}
				if fr.LastFailureAt != nil {
					s.LastFailureUnix = fr.LastFailureAt.Unix()
				}
				if fr.LastInterruptedAt != nil {
					s.LastInterruptedUnix = fr.LastInterruptedAt.Unix()
				}
				if fr.LastSuccessAt != nil {
					s.LastSuccessUnix = fr.LastSuccessAt.Unix()
					s.OK = true
				}
				if fr.ActivationCreatedAt != nil {
					s.ActivationCreatedUnix = fr.ActivationCreatedAt.Unix()
				}
				if fr.ActivationCapacityDeferredAt != nil {
					s.CapacityDeferredUnix = fr.ActivationCapacityDeferredAt.Unix()
				}
				if fr.ActivationGeneration != nil {
					s.ActivationGeneration = *fr.ActivationGeneration
				}
				out[i] = s
			}
			return out, nil
		})); err != nil {
			slog.Warn("register schedule freshness collector", "err", err)
		}
		// Per-app session gauges: sampled live from the proxy at scrape time so a
		// Grafana panel can show sessions-vs-ceiling without polling the REST API.
		// The admission ceiling is per-replica cap times the live replica count;
		// uncapped pools emit only the sessions gauge (no meaningful ceiling).
		if err := reg.Register(metrics.NewSessionGaugesCollector(func() []metrics.SessionSample {
			stats := prx.PoolSessionSnapshot()
			out := make([]metrics.SessionSample, 0, len(stats))
			for slug, st := range stats {
				out = append(out, metrics.SessionSample{
					Slug: slug, Sessions: st.Sessions, Cap: st.Cap, Replicas: st.Replicas,
				})
			}
			return out
		})); err != nil {
			slog.Warn("register session gauges collector", "err", err)
		}
		// Surface dropped audit events (e.g. disk full) as a counter so the
		// silent loss of the compliance trail is alertable.
		store.SetAuditErrorHook(reg.RecordAuditWriteError)
		// Shared-log writers capture this recorder when they are created. Keep
		// persistence health observable without importing Prometheus from db.
		store.SetAppLogMetrics(reg)
		metricsSrv, metricsLn, err = startMetricsListener(upg.Listen, cfg.Metrics.Addr, reg)
		if err != nil {
			return err
		}
		slog.Info("metrics listening", "addr", metricsLn.Addr().String())
		// Tear down the metrics server on any early error return below. Idempotent
		// with the ordered shutdown path (Close after Shutdown is a no-op), so it
		// only matters when runServe returns before reaching that path.
		defer func() { _ = metricsSrv.Close() }()
	}

	// Server tracing: when the existing tracing config is enabled, emit OTel
	// spans for control-plane API request handling to the same OTLP endpoint the
	// managed apps export to. The exporter connects lazily, so Setup never blocks
	// on the collector being reachable.
	var tracer *servertrace.Tracer
	if cfg.Tracing.Enabled {
		tracer, err = servertrace.Setup(ctx, cfg.Tracing, version, cfg.Server.InstanceID)
		if err != nil {
			return fmt.Errorf("server tracing setup: %w", err)
		}
		srv.SetTracer(tracer)
		if tracer != nil {
			// /app/* proxy spans export through the same provider, so the
			// traceparent an app receives names a span the backend has.
			prx.SetSpanTracer(tracer.Tracer())
		}
		slog.Info("server tracing enabled", "endpoint", cfg.Tracing.OTLPEndpoint, "protocol", cfg.Tracing.OTLPProtocol)
		// Tear down the tracer on any early error return below (idempotent with
		// the ordered shutdown path).
		defer func() { _ = tracer.Shutdown(context.Background()) }()
	}

	var stopWorkloadMetrics func()
	if cfg.Metrics.ProcessInterval > 0 {
		// The ownership-lease ID defaults to hostname-PID and changes on restart.
		// Counts need the stable server slot, while launch CPU streams stay unique.
		listener := net.JoinHostPort(cfg.Server.Host, strconv.Itoa(cfg.Server.Port))
		collector, setupErr := workloadmetrics.New(cfg.Tracing, cfg.Metrics.ProcessInterval, listener)
		if setupErr != nil {
			return fmt.Errorf("workload metrics setup: %w", setupErr)
		}
		for _, tier := range cfg.Runtime.Tiers {
			if native, ok := mgr.RuntimeForTier(tier.Name).(interface {
				SetWorkloadObserver(process.WorkloadObserver)
			}); ok {
				native.SetWorkloadObserver(collector.Observe)
			}
		}
		metricsService := services.Go("workload metrics", serviceMemoryStopBudget, collector.Run)
		var stopOnce sync.Once
		stopWorkloadMetrics = func() {
			stopOnce.Do(func() {
				// The final export runs only once sampling has stopped, so no
				// observation lands after the flush.
				if !metricsService.Stop() {
					return
				}
				flushCtx, cancelFlush := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancelFlush()
				if err := collector.Shutdown(flushCtx); err != nil {
					slog.Warn("workload metrics shutdown failed")
				}
			})
		}
		defer stopWorkloadMetrics()
		slog.Info("native workload metrics enabled", "interval", cfg.Metrics.ProcessInterval)
	}

	// Running with neither metrics nor tracing means no external visibility into
	// request rates, error rates, or a crashed-app fleet - an operational blind
	// spot. Warn loudly so it is a deliberate choice, not an oversight.
	if !cfg.Metrics.Enabled && !cfg.Tracing.Enabled {
		slog.Warn("observability disabled: no Prometheus metrics or OTel tracing are enabled; enable metrics.enabled to get request/error/crashed-app signals")
	}

	// Emit a structured access log for every proxied app request. Using the
	// Server's trusted-proxy-aware ClientIP keeps the "client" field honest
	// when shinyhub itself sits behind an edge proxy; this is independent of
	// anything the backend app (uvicorn/httpuv) chooses to print in its own
	// log and gives operators a reliable per-slug audit trail.
	prx.SetClientIPResolver(srv.ClientIP)
	// Distinguish unknown-slug requests from hibernated-app requests so the
	// proxy returns 404 for typos / deleted apps instead of looping the
	// loading page indefinitely. The lookup hits SQLite (cached page) and
	// only runs on miss, so the cost is negligible.
	//
	// We distinguish "row missing" (db.ErrNotFound → return false, nil → 404)
	// from "lookup itself failed" (DB unavailable, ctx cancelled → return
	// false, err → fall through to loading page). Conflating them would 404
	// a real app whenever SQLite hiccupped, masking transient infra issues
	// as a permanent "deleted app" UX.
	prx.SetSlugExists(func(slug string) (bool, error) {
		_, err := store.GetAppBySlug(slug)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, db.ErrNotFound) {
			return false, nil
		}
		slog.Warn("proxy_slug_lookup_failed", "slug", slug, "error", err.Error())
		return false, err
	})
	prx.SetAccessLogger(func(e proxy.AccessLogEntry) {
		attrs := []any{
			"slug", e.Slug,
			"method", e.Method,
			"path", e.Path,
			"status", e.Status,
			"bytes", e.Bytes,
			"duration_ms", e.Duration.Milliseconds(),
			"client_ip", e.ClientIP,
			"peer", e.Peer,
		}
		if e.ReplicaIndex >= 0 {
			attrs = append(attrs, "replica", e.ReplicaIndex, "sticky", e.Sticky)
		}
		if e.Reject != "" {
			attrs = append(attrs, "reject", string(e.Reject))
		}
		if e.Fallback {
			attrs = append(attrs, "fallback", true, "fallback_reason", e.FallbackReason)
		}
		slog.Info("proxy_access", attrs...)
	})
	wsBursts := proxy.NewWSAbnormalBurstDetector()
	prx.SetWSSessionEndRecorder(func(e proxy.WSSessionEnd) {
		attrs := []any{
			"slug", e.Slug, "connection_id", e.ConnectionID, "replica", e.ReplicaIndex,
			"deployment_id", e.DeploymentID, "duration_ms", e.Duration.Milliseconds(),
			"closed_by", e.ClosedBy, "end_signal", e.EndSignal,
			"transport_end_side", e.TransportEndSide, "bytes_to_client", e.BytesToClient,
			"bytes_to_upstream", e.BytesToUpstream, "abnormal", e.Abnormal,
		}
		if e.CloseCode != nil {
			attrs = append(attrs, "close_code", *e.CloseCode)
		}
		if e.CloseReason != "" {
			attrs = append(attrs, "close_reason", e.CloseReason)
		}
		if metricsReg != nil {
			metricsReg.RecordWSSessionEnd(e.Slug, e.ClosedBy, e.TransportEndSide, e.Abnormal)
		}
		if e.Abnormal {
			slog.Warn("ws_session_end", attrs...)
		} else {
			slog.Info("ws_session_end", attrs...)
		}
		if burst, ok := wsBursts.Record(e); ok {
			if metricsReg != nil {
				metricsReg.RecordWSAbnormalBurst(burst.Slug)
			}
			burstAttrs := []any{
				"slug", burst.Slug, "replica", burst.Replica,
				"deployment_id", burst.DeploymentID, "count", burst.Count,
				"window_ms", burst.Window.Milliseconds(), "span_ms", burst.Span.Milliseconds(),
			}
			if burst.CloseCode != nil {
				burstAttrs = append(burstAttrs, "close_code", *burst.CloseCode)
			}
			if burst.CloseReason != "" {
				burstAttrs = append(burstAttrs, "close_reason", burst.CloseReason)
			}
			slog.Warn("ws_abnormal_burst", burstAttrs...)
		}
	})

	// Choose the metrics sampler. A docker default tier samples container stats
	// through the Runtime API. Otherwise use the host sampler, which reads host
	// PIDs for native replicas and reports PID-less replicas (fargate/remote
	// container handles) as zero usage without error, so a running replica on
	// such a tier is never misreported as stopped by a failed PID probe.
	// Compare the default tier's resolved runtime rather than cfg.Runtime.Mode
	// (the legacy field): a config that declares tiers:[{runtime:docker}]
	// without setting runtime.mode must still pick the RuntimeSampler, not the
	// host sampler.
	defaultTierCfg := defaultTierConfig(cfg)
	rt := mgr.RuntimeForTier(defaultTierCfg.Name)
	var replicaSampler process.Sampler
	if defaultTierCfg.Runtime == "docker" {
		replicaSampler = &process.RuntimeSampler{Runtime: rt}
	} else {
		replicaSampler = &hostSampler{}
	}
	srv.SetSampler(replicaSampler)

	// Start the in-memory metrics-history collector unless disabled
	// (history_window == 0). It runs on every instance (recording the replicas it
	// runs locally) with its own sampler instance so its CPU deltas are
	// independent of the API's on-demand sampler. Cancelled + joined at shutdown
	// so it stops cleanly on SIGTERM and on a tableflip re-exec.
	var historyService *rungroup.Service
	if cfg.Metrics.HistoryWindow > 0 {
		histStore := history.NewStore(cfg.Metrics.HistoryWindow, cfg.Metrics.HistoryInterval)
		srv.SetHistory(histStore)
		var histSampler process.Sampler = &process.GopsutilSampler{}
		if defaultTierCfg.Runtime == "docker" {
			histSampler = &process.RuntimeSampler{Runtime: rt}
		}
		collector := history.NewCollector(mgr, prx, histSampler, histStore, cfg.Metrics.HistoryInterval)
		historyService = services.Go("metrics history collector", serviceMemoryStopBudget, collector.Run)
		slog.Info("metrics history collector started",
			"window", cfg.Metrics.HistoryWindow, "interval", cfg.Metrics.HistoryInterval)
	}

	if cfg.OAuth.OIDC.IssuerURL != "" {
		oidcCtx, oidcCancel := context.WithTimeout(context.Background(), 15*time.Second)
		p, err := oauth.NewOIDCProvider(oidcCtx,
			cfg.OAuth.OIDC.IssuerURL,
			cfg.OAuth.OIDC.ClientID,
			cfg.OAuth.OIDC.ClientSecret,
			cfg.OAuth.OIDC.CallbackURL,
			cfg.OAuth.OIDC.DisplayName,
			cfg.OAuth.OIDC.GroupsClaim,
			cfg.OAuth.OIDC.GroupsScope,
		)
		oidcCancel()
		if err != nil {
			return fmt.Errorf("oidc init: %w", err)
		}
		srv.SetOIDCProvider(p)
		slog.Info("oidc configured", "display_name", cfg.OAuth.OIDC.DisplayName, "issuer", cfg.OAuth.OIDC.IssuerURL)
	}
	if cfg.Auth.ForwardAuth.Enabled {
		slog.Info("forward auth configured",
			"user_header", cfg.Auth.ForwardAuth.UserHeader,
			"groups_header", cfg.Auth.ForwardAuth.GroupsHeader,
			"group_role_mappings", len(cfg.Auth.GroupRoleMappings),
			"default_role", cfg.Auth.ForwardAuth.DefaultRole,
		)
	}
	if !cfg.Auth.LocalLoginEnabled() {
		// SSO-only. The startup lockout guard already ensured at least one SSO
		// path is CONFIGURED, but not that it WORKS - surface exactly what was
		// counted so a misconfigured edge (e.g. forward-auth without the edge in
		// trusted_proxies) is diagnosable rather than a silent total lockout.
		slog.Warn("local password login is disabled (auth.local_login: false); users must sign in via SSO",
			"sso_paths", cfg.ActiveSSOLoginPaths(),
			"note", "verify SSO end-to-end; forward-auth needs trusted_proxies to include your edge proxy and the proxy to send the user header",
		)
	}

	deployFn := func(ctx context.Context, slug, bundleDir string, index int) (*deploy.Result, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, fmt.Errorf("get app for deploy: %w", err)
		}
		mode, policyErr := store.ServingWorkerIsolation(app, cfg.Runtime.DefaultWorkerIsolation)
		if policyErr != nil || mode != "multiplex" {
			return nil, fmt.Errorf("replica deploy requires serving multiplex policy: %v", policyErr)
		}
		deployDefaultMem, deployDefaultCPU := cfg.Runtime.DefaultResourcesForApp(app)
		p := deploy.Params{
			Slug:                  slug,
			AppID:                 app.ID,
			BundleDir:             bundleDir,
			Manager:               mgr,
			Proxy:                 prx,
			Replicas:              app.Replicas,
			Placement:             app.PlacementMap(),
			TierOrder:             cfg.Runtime.TierOrder(),
			DefaultTier:           cfg.Runtime.DefaultTierName(),
			MemoryLimitMB:         deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, deployDefaultMem),
			CPUQuotaPercent:       deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, deployDefaultCPU),
			MaxSessionsPerReplica: deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, cfg.Runtime.DefaultMaxSessionsPerReplica),
			IdentityHeaders:       deploy.ResolveIdentityHeaders(app.IdentityHeaders, cfg.Auth.IdentityHeadersEnabled()),
			StartupSampler:        replicaSampler,
			WorkerIsolation:       mode,
			// Pin a shared-mount consumer's restarted replica to the worker set
			// hosting its source data, matching the full-deploy placement so a
			// recovered replica lands beside the data it mounts.
			ColocateWorkers: srv.ColocationPins(app),
		}
		current, derr := store.GetServingDeployment(app.ID)
		if derr != nil {
			return nil, fmt.Errorf("list deployment for guarded consumer start: %w", derr)
		}
		p.ContentDigest = current.ContentDigest
		p.DeploymentID = current.ID
		p.RecordRuntimePolicy = func() error { return store.RecordDeploymentWorkerIsolation(app.ID, current.ID, mode) }
		p.AppVersion = current.Version
		p.Preparation = deploy.ActivationPreparation(current.Prepared)
		p.GuardUntilAcknowledged = true
		p.ReplicaStarted = func(result deploy.Result) error {
			rows, listErr := store.ListReplicas(app.ID)
			if listErr != nil {
				return fmt.Errorf("check prior replica identity: %w", listErr)
			}
			for _, prior := range rows {
				if prior.Index != result.Index || prior.PID == nil || *prior.PID <= 0 || *prior.PID == result.PID {
					continue
				}
				groupProbeErr := syscall.Kill(-*prior.PID, 0)
				pidProbeErr := syscall.Kill(*prior.PID, 0)
				if !errors.Is(groupProbeErr, syscall.ESRCH) || !errors.Is(pidProbeErr, syscall.ESRCH) {
					return fmt.Errorf("replica %d still has unconfirmed prior native process group %d", result.Index, *prior.PID)
				}
				if clearErr := store.ClearReplicaRuntimeIdentity(app.ID, result.Index); clearErr != nil && !errors.Is(clearErr, db.ErrNotFound) {
					return fmt.Errorf("clear confirmed-absent prior replica identity: %w", clearErr)
				}
				break
			}
			pid, port, deploymentID := result.PID, result.Port, current.ID
			return store.UpsertReplica(db.UpsertReplicaParams{
				AppID: app.ID, Index: result.Index, PID: &pid, Port: &port, Status: "starting",
				Provider: result.Provider, Tier: result.Tier, EndpointURL: result.EndpointURL,
				WorkerID: result.WorkerID, AppVersion: current.Version,
				DesiredState: "running", DeploymentID: &deploymentID, ConsumerBooted: true,
			})
		}
		return deploy.RunReplica(traceReplica(ctx, tracer, p), index)
	}

	// resumeFn restores a suspended replica via the Snapshotter path, mirroring
	// deployFn's Params shape (placement/tier so the route transport resolves).
	resumeFn := func(ctx context.Context, slug, bundleDir string, index int) (*deploy.Result, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, fmt.Errorf("get app for resume: %w", err)
		}
		p := deploy.Params{
			Slug:        slug,
			AppID:       app.ID,
			BundleDir:   bundleDir,
			Manager:     mgr,
			Proxy:       prx,
			Replicas:    app.Replicas,
			Placement:   app.PlacementMap(),
			TierOrder:   cfg.Runtime.TierOrder(),
			DefaultTier: cfg.Runtime.DefaultTierName(),
		}
		mode, policyErr := store.ServingWorkerIsolation(app, cfg.Runtime.DefaultWorkerIsolation)
		if policyErr != nil || mode != "multiplex" {
			return nil, fmt.Errorf("replica resume requires serving multiplex policy: %v", policyErr)
		}
		current, derr := store.GetServingDeployment(app.ID)
		if derr != nil {
			return nil, derr
		}
		p.DeploymentID = current.ID
		p.AppVersion = current.Version
		p.WorkerIsolation = mode
		return deploy.ResumeReplica(traceReplica(ctx, tracer, p), index)
	}

	lcCfg := lifecycle.Config{
		WatchInterval:                cfg.Lifecycle.WatchInterval,
		RestartMaxAttempts:           cfg.Lifecycle.RestartMaxAttempts,
		HibernateTimeout:             cfg.Lifecycle.HibernateTimeout,
		DefaultMaxSessionsPerReplica: cfg.Runtime.DefaultMaxSessionsPerReplica,
		IdentityHeadersGlobal:        cfg.Auth.IdentityHeadersEnabled(),
		Clustered:                    isClustered(cfg),
		InstanceID:                   cfg.Server.InstanceID,
		MaxSuspended:                 cfg.Runtime.Snapshot.MaxSuspended,
		DefaultWorkerIsolation:       cfg.Runtime.DefaultWorkerIsolation,
	}
	watcher := lifecycle.New(lcCfg, mgr, prx, store, deployFn)
	watcher.SetResume(resumeFn)
	watcher.SetOperationInFlight(srv.DeployInFlight)
	watcher.SetTryAppOperation(srv.TryAcquireAppOperation)

	// Wire pre-warming ops: when an app has min_warm_replicas > 0 the watcher
	// calls WarmShrink instead of fully hibernating. The drain grace matches
	// the autoscaler's ScaleDown grace so replica drain behaviour is consistent
	// across the two code paths. srv is constructed above, before the watcher.
	const warmDrainGrace = 30 * time.Second
	watcher.SetWarmOps(
		func(slug string, floor int) (bool, error) {
			return srv.WarmShrink(slug, floor, warmDrainGrace)
		},
		srv.WarmExpand,
	)

	// Wire operator-requested sleep. The teardown lives on the watcher because it
	// is the same path idle hibernation uses; the API server is constructed
	// before the watcher, so the executor is injected here.
	srv.SetSleepOp(watcher.SleepNow)

	// Wire the wake trigger on every instance at startup, independent of
	// ownership. A standby issues the BeginWake CAS (hibernated->waking) on a
	// proxy miss so the DB reflects the pending wake immediately; the active's
	// runOnce reconciler drives it to running. The active drives inline.
	// The proxy's ErrorHandler also calls this trigger on a pre-response
	// upstream error so a dead/hibernated replica is woken rather than 502'd,
	// in single-node and clustered deployments alike.
	prx.SetWakeTrigger(watcher.WakeTrigger)

	// Wire demand-driven worker spawn and terminate callbacks for elastic
	// isolation pools (per_session / grouped). The spawner boots one native
	// worker per slot and registers it with the proxy; the terminate callback
	// is called when all assigned clients disconnect (after the grace window).
	elasticSpawner := &lifecycle.ElasticSpawner{
		StartupSampler:      replicaSampler,
		CheckLaunchCapacity: srv.CheckElasticLaunchCapacity,
		Store:               store,
		Manager:             mgr,
		Proxy:               prx,
		RuntimeCfg:          cfg.Runtime,
		AcquireAppOperation: srv.AcquireAppOperation,
		EnqueuePendingStop:  watcher.QueuePendingStop,
	}
	prx.SetSpawnFunc(func(slug string, slotID int) {
		safego.Go("elastic spawn", func() { elasticSpawner.Spawn(slug, slotID) })
	})
	prx.SetResumeFunc(func(slug string, slotID int) {
		safego.Go("elastic resume", func() { elasticSpawner.Resume(slug, slotID) })
	})
	prx.SetWarmSpareConsumedFunc(elasticSpawner.WarmSpareConsumed)
	prx.SetTerminateFunc(elasticSpawner.Terminate)
	prx.SetTerminateGenerationFunc(elasticSpawner.TerminateGeneration)
	prx.SetCancelElasticLifetimeFunc(elasticSpawner.CancelLifetime)
	prx.SetElasticLifetimeUpdateFunc(elasticSpawner.UpdateSessionLifetime)
	// The watcher's hibernation sweep stops elastic worker slots through the
	// same confirmed-exit path as a demand-driven terminate, so it needs the
	// spawner too. Wired here because elasticSpawner does not exist yet when
	// the watcher is constructed above.
	watcher.SetElasticTerminator(elasticSpawner)

	if err := configureProxyAdmission(cfg, store, srv, prx, services); err != nil {
		return err
	}

	// Record hibernate/wake transitions and crash-restart counts when metrics
	// are enabled. Nil-safe inside the watcher when metrics are disabled.
	if metricsReg != nil {
		watcher.SetMetrics(metricsReg)
	}
	// Wire Fargate operation metrics for every tier that uses a Fargate runtime.
	// metricsReg satisfies fargate.FargateMetrics via the methods added to Registry.
	if metricsReg != nil {
		for _, tierName := range cfg.Runtime.TierOrder() {
			tierRT := mgr.RuntimeForTier(tierName)
			if frt, ok := tierRT.(*fargate.Runtime); ok {
				frt.SetMetrics(metricsReg)
			}
		}
	}
	// Emit spans for background wake/restart/hibernate operations into the same
	// provider the API server uses, so cold-start latency and restart storms are
	// visible in the trace backend.
	if tracer != nil {
		watcher.SetTracer(tracer.Tracer())
	}

	// Wire lost-replica healing. Managed cloud API reachability is validated at
	// startup, so those tiers do not depend on a worker registry. For worker
	// tiers, delegate to the registry. Mixed cloud + worker deployments are
	// handled by the combined predicate.
	//
	// Note: persistent provider failures still consume the normal crash-restart
	// budget, so always-true for managed tiers cannot cause runaway
	// re-placement.
	hasManagedCloudTier := false
	for _, name := range cfg.Runtime.TierOrder() {
		mode, _ := cfg.Runtime.RuntimeForTier(name)
		if mode == "fargate" || mode == "scaleway_serverless" {
			hasManagedCloudTier = true
			break
		}
	}
	if workerReg != nil || hasManagedCloudTier {
		watcher.EnableLostReplicaHealing(func(tier string) bool {
			mode, _ := cfg.Runtime.RuntimeForTier(tier)
			if mode == "fargate" || mode == "scaleway_serverless" {
				return true // provider API is the placement authority
			}
			if workerReg != nil {
				_, ok := workerReg.WorkerForTier(tier)
				return ok
			}
			return false
		})
	}

	// reconcileCleaner is handed to the owner runtime for owner-scoped cleanup so only
	// the lease holder mutates retained cloud resources.
	var reconcileCleaner lifecycle.AppSecretsCleaner
	if len(managedResourceCleaners) > 0 {
		reconcileCleaner = managedResourceCleaners
	}

	// Worker-down monitor: object created here, loop started as owner work.
	// Only relevant when worker hosting is enabled.
	var monitor *lifecycle.WorkerDownMonitor
	if workerReg != nil {
		monitor = lifecycle.NewWorkerDownMonitor(store, workerTimeout, workerRetention, workerReg.Reap, func(slug string, index int, expectURL string) {
			prx.DeregisterReplicaIfTarget(slug, index, expectURL)
		}, mgr.EvictReplicaIfWorker, workerReg.Forget)
	}

	jobsMgr, err := jobs.NewManager(mgr, cfg.Runtime.TierOrder(), cfg.Runtime.DefaultTierName(), store, secretsKey, cfg.Storage.AppsDir, absAppDataDir)
	if err != nil {
		return fmt.Errorf("init jobs manager: %w", err)
	}
	mgr.SetConsumerLifetimeResolver(jobsMgr.AcquireConsumerLifetime)
	watcher.SetConsumerBootGate(jobsMgr.AcquireCompatibleConsumerBootGate)
	elasticSpawner.AcquireConsumerBootGate = jobsMgr.AcquireCompatibleConsumerBootGate
	// Cap scheduled jobs by the same per-replica memory/CPU ceiling as the app, so
	// a heavy refresh-data run cannot exceed the app's resource budget on a shared
	// native host.
	jobsMgr.SetResourceResolver(func(app *db.App) (memoryMB, cpuPct int) {
		defaultMem, defaultCPU := cfg.Runtime.DefaultResourcesForApp(app)
		return deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, defaultMem),
			deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, defaultCPU)
	})
	// Trace scheduled runs with the server's tracer so each run's
	// schedule.run span and the job's own spans share one trace.
	if tracer != nil {
		jobsMgr.SetTracing(cfg.Tracing, tracer.Tracer())
	}
	sched := scheduler.New(jobsMgr, store, cfg.Scheduler.Location)
	if metricsReg != nil {
		sched.SetMetrics(metricsReg)
	}
	srv.SetJobs(jobsMgr, sched)
	activationCoordinator := activation.New(store, srv, 2*time.Second)

	// Push scheduled-run outcomes into Prometheus when metrics are enabled.
	if metricsReg != nil {
		jobsMgr.SetScheduleMetrics(metricsReg)
	}
	// Preserve a genuinely nil interface when metrics are disabled. Passing the
	// nil *Registry directly would produce a non-nil interface and panic on the
	// first retention event that tried to record cleanup.
	var appLogTelemetry appLogMaintenanceMetrics
	if metricsReg != nil {
		appLogTelemetry = metricsReg
	}

	// Replica autoscaling is opt-in per app and gated by a global switch. When
	// enabled, the controller evaluates opted-in apps on its own interval and
	// drives the same incremental scale primitives the API exposes; it never
	// scales worker hosts. The loop is started inside ownerRuntime.run.
	var controller *autoscale.Controller
	if cfg.Runtime.Autoscale.Enabled {
		runtimeMax := cfg.Runtime.MaxReplicas
		if runtimeMax <= 0 {
			runtimeMax = 32
		}
		// Look back twice the scan interval so a saturated pool is still observed
		// between ticks, but never beyond the cooldown: a stale saturation event
		// must not bias successive actions once a fresh decision is allowed.
		rejectWindow := 2 * cfg.Runtime.Autoscale.ScanInterval
		if rejectWindow > cfg.Runtime.Autoscale.Cooldown {
			rejectWindow = cfg.Runtime.Autoscale.Cooldown
		}
		// In clustered mode the autoscaler reads the fleet-wide session count
		// (sum across all instances from replica_sessions) so a scale decision
		// is based on total load, not just this instance's local count. In
		// single-node mode the proxy is used directly for the exact local count,
		// which is byte-for-byte unchanged from before.
		var autoscaleSignal autoscale.Signal = prx
		if isClustered(cfg) {
			autoscaleSignal = proxy.NewFleetSignal(prx, store, slog.Default())
		}
		controller = autoscale.New(autoscale.Config{
			ScanInterval:  cfg.Runtime.Autoscale.ScanInterval,
			Cooldown:      cfg.Runtime.Autoscale.Cooldown,
			DrainGrace:    30 * time.Second,
			RejectWindow:  rejectWindow,
			DefaultTarget: cfg.Runtime.Autoscale.DefaultTarget,
			DefaultCap:    cfg.Runtime.DefaultMaxSessionsPerReplica,
			RuntimeMax:    runtimeMax,
		}, store, autoscaleSignal, srv, store, store, slog.Default())
		if metricsReg != nil {
			controller.SetMetrics(metricsReg)
		}
	}

	owner := &ownerRuntime{
		cfg:                        cfg,
		store:                      store,
		srv:                        srv,
		mgr:                        mgr,
		prx:                        prx,
		jobsMgr:                    jobsMgr,
		sched:                      sched,
		watcher:                    watcher,
		workerReg:                  workerReg,
		monitor:                    monitor,
		controller:                 controller,
		activationCoordinator:      activationCoordinator,
		reconcileCleaner:           reconcileCleaner,
		appLogTelemetry:            appLogTelemetry,
		syncDeployCredential:       syncDeployCredential,
		retiringParentDone:         retiringParentDone,
		releaseRetiringParentFence: releaseRetiringParentFence,
	}
	ownerReady := &owner.ready
	handoffIncomplete := &owner.handoffIncomplete

	scope := leader.NewOwnerScope(owner.run)
	elector := leader.New(store, leader.Config{
		InstanceID: cfg.Server.InstanceID,
		TTL:        cfg.Server.LeaseTTL,
		RenewEvery: cfg.Server.LeaseRenewEvery,
		OnAcquire:  scope.Acquire,
		OnLose:     scope.Lose,
		// Sticky for the process: a job left running by any failed drain keeps
		// running until exit, so the final shutdown must not hand it a successor.
		RetainLeaseOnShutdown: handoffIncomplete.Load,
		Logger:                slog.Default(),
	})
	// Wire the ownership predicate into the watcher's wake trigger so a standby
	// that wins BeginWake defers to the active's reconciler rather than trying
	// to deploy. The raw IsOwner (not ownerAndReady) is intentional: the wake
	// trigger only needs to know "am I the active process-owner right now"; the
	// ownerAndReady gate is for API mutations that need a fresh worker index.
	watcher.SetIsOwner(elector.IsOwner)

	// Gate mutations on owner AND ready: the elector reports IsOwner true before
	// ownerRuntime.run refreshes the registry, so admitting owner-only mutations (main-API
	// deploy/placement, worker register/heartbeat) must wait for the fresh index.
	ownerAndReady := ownerAndReadyPredicate(elector.IsOwner, ownerReady)
	srv.SetOwnership(func() bool { return ownerAndReady() && !srv.AppOperationsPaused() })
	srv.SetOwnerLease(func() *db.OwnerLease {
		return &db.OwnerLease{Instance: cfg.Server.InstanceID, Epoch: elector.Epoch()}
	})
	elasticSpawner.CanMutate = ownerAndReady
	if workerAPI != nil {
		workerAPI.SetOwnership(ownerAndReady)
	}
	electorCtx, cancelElector := context.WithCancel(context.Background())
	electorDone := make(chan struct{})
	var localLogMaintenanceWG sync.WaitGroup
	// rungroup:exempt: joined through electorDone by stopOwnership, under the ownership watchdog that exits rather than close the store under it
	go func() {
		defer close(electorDone)
		elector.Run(electorCtx)
	}()
	// Cancellation alone is not a join: the Elector may be finishing an
	// in-flight acquire and can synchronously start owner work before it observes
	// cancellation. Always wait for Run to return before dependencies are closed.
	stopOwnership := func() {
		cancelElector()
		<-electorDone
		localLogMaintenanceWG.Wait()
		scope.Stop()
	}
	// stopOwnership can hang (ownerRuntime.run's shutdown branch blocking on
	// loops.Wait(), a slow drain, or the fence retry), and letting that hang
	// propagate here would race runServe's deferred store.Close() against a
	// goroutine still using the store. awaitOwnershipStop is idempotent to call
	// more than once (scope.Stop() is idempotent) - the explicit call sites
	// below on error paths use the same watchdog wrapper.
	defer awaitOwnershipStop(shutdownWatchdogBudget, stopOwnership, slog.Default())
	if cfg.Maintenance.AppLogRunRetentionCount > 0 {
		localLogMaintenanceWG.Add(1)
		// rungroup:exempt: joined by stopOwnership with the elector, under the same exit watchdog
		go func() {
			defer localLogMaintenanceWG.Done()
			runLocalLogMaintenance(electorCtx, store, mgr, appLogTelemetry, cfg.Maintenance.Interval)
		}()
	}

	// Re-resolve JWT-claimed users against the live DB on every /app/* hit
	// so role demotions and account deletions take effect immediately.
	// Without this an admin's still-valid JWT keeps the admin-bypass path
	// open through the access middleware until the token expires — the same
	// staleness bug the API middleware already guards against via its own
	// userLookup wiring (see internal/api/router.go). Routes through the
	// canonical store.LookupContextUser so the resolved user carries Email and
	// DisplayName - the proxy forwards these to apps as X-Shinyhub-Email /
	// X-Shinyhub-Name, and a hand-rolled ContextUser here would silently blank
	// them for every native /app session.
	appUserLookup := store.LookupContextUser
	// The middleware above decides once per request, and a WebSocket upgrade is
	// one request: the session it opens then outlives every later decision. This
	// re-runs the same check over this instance's live upgraded connections and
	// closes the ones whose identity has been revoked. Deliberately started
	// outside the owner scope - an HA standby holds its own connections and no
	// other instance can close them for it.
	every := cfg.SessionRecheckInterval()
	recheckAppSessions := every > 0
	if !recheckAppSessions {
		every = 30 * time.Second // business permission snapshots must always be revocable
	}
	reauthorize := func(c proxy.ConnPrincipal) (bool, string, error) {
		principal := access.Principal{
			Slug:                   c.Slug,
			EntitlementAppID:       c.EntitlementAppID,
			EntitlementFingerprint: c.EntitlementFingerprint,
			UserID:                 c.UserID,
			Role:                   c.Role,
			SessionEpoch:           c.SessionEpoch,
			JTI:                    c.JTI,
			ActorID:                c.ActorID,
			ActorRole:              c.ActorRole,
			ActorSessionEpoch:      c.ActorSessionEpoch,
			SupportAppID:           c.SupportAppID,
			RoutedAppID:            c.RoutedAppID,
			SupportExpiresAt:       c.SupportExpiresAt,
		}
		if !recheckAppSessions {
			return access.RecheckEntitlements(store, principal)
		}
		return access.Recheck(store, appUserLookup, store.IsTokenRevoked, principal)
	}
	// A session-preserving reload replaces this service with relay
	// authorization once the store is closed; whichever runs last is stopped
	// before shutdown completes.
	recheckService := services.Go("session recheck", serviceStoreStopBudget, func(rctx context.Context) {
		prx.StartSessionRecheck(rctx, every, reauthorize)
	})
	slog.Info("session recheck started", "interval", every, "entitlements_only", !recheckAppSessions)
	if !recheckAppSessions {
		slog.Warn("general session recheck disabled; app entitlements still checked every 30s")
	}
	httpHandler, err := buildHTTPHandler(httpHandlerDeps{
		cfg:            cfg,
		store:          store,
		srv:            srv,
		prx:            prx,
		readyCh:        readyCh,
		ownerAndReady:  ownerAndReady,
		bundleTokenKey: bundleTokenKey,
		appUserLookup:  appUserLookup,
	})
	if err != nil {
		return err
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	// ReadTimeout/WriteTimeout bound slow-body and slow-read clients on ordinary
	// requests so they cannot pin a connection (and its goroutine) indefinitely.
	// Streaming and long-running routes (the reverse proxy, SSE log tails, large
	// uploads, lifecycle swaps) clear these per-connection deadlines via
	// deadlineExemptions so they are not severed mid-flight.
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           httpHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Route stdlib server errors (TLS handshake failures, malformed requests)
		// through slog instead of the default unstructured log package.
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
	}

	ln, err := upg.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	// Startup pool adoption (single-node only): populate the proxy pool from
	// routable DB replica rows before the listener starts serving, so a ZDT
	// successor process can route requests to already-running app processes
	// immediately. Without this the pool is empty until RecoverProcesses runs
	// inside ownerRuntime.run (~10s after boot), causing 10-18s of loading-page
	// degradation on every handoff.
	//
	// Clustered deployments already have a periodic PoolSyncer that runs from
	// the first tick (and calls RunOnce via its Run loop). Single-node has no
	// periodic syncer, so a one-shot call here covers the ZDT gap without
	// changing any other single-node behaviour.
	//
	// prx.MarkSynced() was already called above (single-node pre-seeds the
	// first-sync gate so /readyz is not blocked). RunOnce calls MarkSynced
	// again, which is idempotent.
	if !isClustered(cfg) {
		// Pass the real dialer (nil when worker support is unconfigured) so startup
		// adoption can build mTLS transports for remote_docker replicas it adopts;
		// TransportForReplica reports an error for a nil dialer rather than crashing.
		startupSyncer := proxy.NewPoolSyncer(prx, store,
			worker.NewReplicaTransportBuilder(dialer, store),
			slog.Default(), cfg.Auth.IdentityHeadersEnabled())
		startupSyncer.SetReplicaValidator(lifecycle.ReplicaRouteValidator(store, mgr))
		adoptionCtx, cancelAdoption := context.WithTimeout(ctx, 3*time.Second)
		startupSyncer.RunOnce(adoptionCtx)
		cancelAdoption()
		slog.Info("startup pool adoption complete (single-node)")
	}

	httpListener := newHandoffListener(ln, httpSrv)
	serveErr := make(chan error, 1)
	// rungroup:exempt: joined by httpSrv.Shutdown and the handoff listener's ServeDone
	go func() {
		slog.Info("listening", "version", version, "addr", addr)
		close(readyCh)
		defer httpListener.ServeDone()
		if err := httpSrv.Serve(httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) && !httpListener.Closing() {
			serveErr <- err
		}
		close(serveErr)
	}()

	// Signal readiness: closes inherited-but-unused fds, writes the PID file, and
	// (when this is an upgrade child) tells the old process it may begin draining.
	// On error, close the servers we just started so we do not leak listeners and
	// goroutines, then return.

	var relayRPC *relayOwner
	if relayEnabled {
		relayRPC, err = startRelayOwner(relayDir, cfg.Auth.Secret, store, reauthorize)
		if err != nil {
			return fmt.Errorf("start relay owner: %w", err)
		}
		defer relayRPC.Close()
		upg.(*upgrade.RepeatableUpgrader).SetBeforeReady(relayRPC.Activate)
		upg.(*upgrade.RepeatableUpgrader).SetUpgradeFailure(func(error) {
			if err := relayRPC.Activate(); err != nil {
				slog.Error("restore relay owner after failed upgrade", "err", err)
			}
		})
		upg.(*upgrade.RepeatableUpgrader).EnableRelayHandoff()
	}
	checkRelayUpgrade := func(probe bool) error {
		_, live, unknown := relayLeases.Snapshot()
		if unknown {
			return errors.New("cannot verify retained relay leases; upgrade refused")
		}
		if cfg.Server.ReloadSessions == "preserve" {
			var blockers []string
			jobBlockers, err := jobsMgr.ReloadBlockers()
			if err != nil {
				return err
			}
			blockers = append(blockers, jobBlockers...)
			temporary := true
			if live >= cfg.Server.ReloadRelayLimit {
				blockers = append(blockers, "reload relay limit reached; wait for retained sessions to finish")
			}
			if serveOpts.FirstRun != nil {
				blockers = append(blockers, "first-run setup must finish")
				temporary = false
			}
			poolBlockers, poolTemporary := prx.RelayUpgradeBlockers()
			blockers = append(blockers, poolBlockers...)
			temporary = temporary && poolTemporary
			if len(poolBlockers) == 0 {
				if _, stable := prx.RelaySnapshot(); !stable {
					blockers = append(blockers, prx.RelayConnectionBlockers()...)
				}
			}
			apps, err := store.ListAppsLean(0, 0)
			if err != nil {
				return err
			}
			for _, app := range apps {
				inFlight, err := store.ScheduleActivationInFlight(app.ID)
				if err != nil {
					return err
				}
				if inFlight {
					blockers = append(blockers, fmt.Sprintf("%s: serving activation in progress", app.Slug))
				}
			}
			if len(blockers) > 0 {
				reason := "session-preserving reload blocked: " + strings.Join(blockers, "; ")
				if temporary {
					return &upgrade.TransitionError{Reason: reason}
				}
				return errors.New(reason)
			}
		}
		if !probe || (live == 0 && cfg.Server.ReloadSessions != "preserve") {
			return nil
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return upgrade.CheckRelaySuccessor(probeCtx)
	}
	upg.(*upgrade.RepeatableUpgrader).SetUpgradeGuard(func() (func(), error) {
		resumeJobs := func() {}
		if cfg.Server.ReloadSessions == "preserve" {
			resumeJobs = jobsMgr.PauseAdmissions()
		}
		// Observe known blockers without fencing the fleet on every wait tick.
		// Recheck after pausing to close the admission race before probing.
		if err := checkRelayUpgrade(false); err != nil {
			resumeJobs()
			return nil, err
		}
		if cfg.Server.ReloadSessions != "preserve" {
			_, live, unknown := relayLeases.Snapshot()
			if live == 0 && !unknown {
				return nil, checkRelayUpgrade(true)
			}
		}
		release, ok := srv.TryPauseAppOperations()
		if !ok {
			resumeJobs()
			return nil, &upgrade.TransitionError{Reason: "session-preserving reload waits for admitted app operations"}
		}
		if err := checkRelayUpgrade(true); err != nil {
			release()
			resumeJobs()
			return nil, err
		}
		return func() { release(); resumeJobs() }, nil
	})
	if cfg.Server.ReloadSessions == "preserve" {
		upgradeSignals.SetReloadWaitHook(func() func(bool) {
			resume := activationCoordinator.Pause()
			resumeJobs := jobsMgr.PauseAdmissions()
			return func(succeeded bool) {
				if !succeeded {
					resume()
					resumeJobs()
				}
			}
		})
	}
	var reloadStartupReady atomic.Bool
	reloadReady := reloadReadyPredicate(isClustered(cfg), prx, reloadStartupReady.Load, elector.IsOwner, ownerAndReady, srv.AppOperationsPaused)
	var servingChecks []upgrade.ReloadServingCheck
	if cfg.Server.ReloadSessions == "preserve" && !isClustered(cfg) {
		servingChecks = append(servingChecks, reloadServingCheck(store, mgr, prx))
	}
	stopReloadControl, err := upgrade.StartReloadControl(ctx, cfg.Server.PIDFile, cfg.Server.UpgradeTimeout, upg, upgradeSignals, reloadReady, logger, servingChecks...)
	if err != nil {
		// An optional acknowledgement endpoint must not turn a compatible cold
		// start into an outage. Signals still work; the command fails clearly.
		logger.Error("acknowledged reload unavailable; use SIGHUP until the control socket configuration is corrected", "err", err)
		stopReloadControl = func() {}
	}
	defer stopReloadControl()
	stopLocalTokens, err := localtoken.Start(ctx, cfg.Server.PIDFile, func(ctx context.Context, scope string, ttl time.Duration) (string, error) {
		token, id, expires, err := auth.IssueLocalToken(cfg.Auth.Secret, scope, ttl)
		if err != nil {
			return "", err
		}
		if err := store.RecordLocalToken(ctx, id, expires); err != nil {
			return "", err
		}
		return token, nil
	})
	if err != nil {
		logger.Error("host-local token issuance unavailable", "err", err)
		stopLocalTokens = func() {}
	} else {
		defer stopLocalTokens()
	}

	if err := upg.Ready(); err != nil {
		_ = httpSrv.Close()
		if metricsSrv != nil {
			_ = metricsSrv.Close()
		}
		awaitOwnershipStop(shutdownWatchdogBudget, stopOwnership, slog.Default())
		return fmt.Errorf("upgrade ready: %w", err)
	}
	// Tell systemd (Type=notify) we are the live process and retarget MAINPID to
	// this PID, so a handoff successor is tracked. NotifyReady is a no-op (returns
	// nil) when $NOTIFY_SOCKET is unset, so a non-nil error here means we ARE under
	// systemd and the notify failed - which is fatal: systemd would kill us at
	// TimeoutStartSec, and a successor that fails to retarget MAINPID after
	// upg.Ready() already told the parent to drain would be left unmanaged.
	if err := upgrade.NotifyReady(); err != nil {
		_ = httpSrv.Close()
		if metricsSrv != nil {
			_ = metricsSrv.Close()
		}
		awaitOwnershipStop(shutdownWatchdogBudget, stopOwnership, slog.Default())
		return fmt.Errorf("sd_notify: %w", err)
	}

	if relayRPC != nil {
		relayRPC.Commit()
	}
	reloadStartupReady.Store(true)
	firstRun := startFirstRunExperience(logger, services, srv.Router(), store, cfg, ownerAndReady, serveOpts)

	select {
	case err := <-serveErr:
		if err != nil {
			awaitOwnershipStop(shutdownWatchdogBudget, stopOwnership, slog.Default())
			return fmt.Errorf("http server: %w", err)
		}
	case <-upg.Exit():
		slog.Info("shutdown/handoff initiated, draining")
	}
	// The example install deploys through the API and the store; end it
	// before ownership and the HTTP server it goes through are torn down.
	if firstRun != nil {
		firstRun.Stop()
	}
	// Mark unready for both the signal and clean self-stop paths.
	prx.SetDraining(true)
	handedOff := upgradeSignals.SuccessfulHandoff()
	stopLocalTokens()
	stopReloadControl()

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	httpQuiesced := true
	if handedOff && ctx.Err() == nil {
		if err := httpListener.Quiesce(shutdownCtx); err != nil {
			httpQuiesced = false
			slog.Warn("HTTP accept drain", "err", err)
		}
		if metricsLn != nil {
			if err := metricsLn.(*handoffListener).Quiesce(shutdownCtx); err != nil {
				slog.Warn("metrics accept drain", "err", err)
			}
		}
	}
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		httpQuiesced = false
		slog.Warn("http shutdown", "err", err)
	}
	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("metrics shutdown", "err", err)
		}
	}
	// Stop and join the Elector. Its synchronous OnLose callback cancels the
	// owner span and waits for the watcher/scheduler/monitor/autoscaler to exit
	// before Run returns, so jobs and the store remain valid through handoff.
	// Bounded by shutdownWatchdogBudget: a hang here must not block forever,
	// since the deferred store.Close() below would otherwise race a goroutine
	// this call left running.
	awaitOwnershipStop(shutdownWatchdogBudget, stopOwnership, slog.Default())
	// Stop the session reporter (clustered only). Cancel triggers a final
	// flush so the last known counts are persisted before the store closes.
	if reporterService != nil {
		reporterService.Stop()
	}
	// Stop the pool syncer (clustered only). Nil out the on-miss hook after
	// the syncer goroutine exits so a late-arriving request cannot invoke
	// SyncSlug with an already-cancelled context.
	if syncerService != nil {
		syncerService.Stop()
		prx.SetOnMissSync(nil)
	}
	// Stop the metrics-history collector. It only reads in-memory manager/proxy
	// state, so ordering relative to the store close is not load-bearing.
	if historyService != nil {
		historyService.Stop()
	}
	// Drain in-flight scheduled job runs before the store closes.
	jobsDrained := jobsMgr.Stop(shutdownCtx)

	if relayRPC != nil {
		relayRPC.Close()
	}
	preservingReload := false
	var relayLease *upgrade.RelayLease
	if handedOff && httpQuiesced && jobsDrained && !handoffIncomplete.Load() && ctx.Err() == nil && cfg.Server.ReloadSessions == "preserve" {
		backends, stable := prx.SealRelay()
		if stable {
			relayLease, err = upgrade.NewRelayLease(relayDir)
			if err == nil {
				err = relayLease.Publish(backends)
			}
			if err != nil {
				if relayLease != nil {
					_ = relayLease.Close()
					relayLease = nil
				}
				slog.Warn("cannot publish relay lease; using bounded reload drain", "err", err)
			}
		}
		if relayLease != nil {
			// Only an explicitly capable child can release this parent early.
			// Legacy successors retain the process-exit fence and bounded drain.
			if upg.(*upgrade.RepeatableUpgrader).SuccessorRelayAware() {
				srv.Close()
				stopBeforeStoreCloses(recheckService, slog.Default())
				client := newRelayClient(relayDir, cfg.Auth.Secret)
				if usageRecorder != nil {
					usageRecorder.RelayPersistence(client)
				}
				mgr.RetireControlPlane()
				if stopRelayWatch != nil {
					stopRelayWatch()
				}
				if stopWorkloadMetrics != nil {
					stopWorkloadMetrics()
				}
				if err := store.Close(); err != nil {
					return fmt.Errorf("close retiring database: %w", err)
				}
				recheckService = services.Go("relay authorization", serviceMemoryStopBudget, func(actx context.Context) {
					runRelayAuthorization(actx, prx, client, every, max(90*time.Second, 3*every))
				})
				upg.(*upgrade.RepeatableUpgrader).ReleaseUpgradeFence()
				preservingReload = upg.(*upgrade.RepeatableUpgrader).RetireControlPlane()
			}
			if !preservingReload {
				_ = relayLease.Close()
				relayLease = nil
			} else {
				defer relayLease.Close()
			}
		}
	}
	// Drain live WebSocket (hijacked) app sessions: http.Server.Shutdown does
	// not wait for hijacked connections, so wait here up to drain_timeout, then
	// force-close stragglers. App backends (separate processes) remain alive
	// during this window, so sessions keep flowing until drained or force-closed.
	if !preservingReload {
		if jobsDrained && !handoffIncomplete.Load() {
			upg.(*upgrade.RepeatableUpgrader).ReleaseUpgradeFence()
		}
		if handedOff && cfg.Server.ReloadSessions == "preserve" {
			slog.Warn("session-preserving reload fell back to bounded drain", "http_quiesced", httpQuiesced)
		}
	}
	if n := prx.ActiveUpgradedConns(); n > 0 {
		drainCtx := context.Background()
		drainTimeout := cfg.Server.DrainTimeout
		if handedOff && ctx.Err() == nil {
			// The upgrader requested retirement after a successful handoff,
			// rather than a SIGINT/SIGTERM service shutdown.
			drainTimeout = cfg.Server.EffectiveReloadDrainTimeout()
			drainCtx = ctx
		}
		slog.Info("draining upgraded connections", "count", n, "timeout", drainTimeout)

		forced := 0
		if preservingReload {
			slog.Info("control plane retired; preserving established multiplex sessions", "count", n)
			forced = prx.PreserveUpgraded(ctx, relayLease, cfg.Server.DrainTimeout, cfg.Server.MaxRetiredLifetime)
		} else {
			forced = prx.DrainUpgradedForReload(drainCtx, drainTimeout, cfg.Server.DrainTimeout)
		}
		if forced > 0 {

			slog.Warn("drain timeout reached, force-closed upgraded connections", "count", forced)
		} else {
			slog.Info("upgraded connections drained cleanly")
		}
	}
	// Retiring proxies still enforce access revocations until their final
	// connection closes. Join the sweep before its store is closed below.
	stopBeforeStoreCloses(recheckService, slog.Default())
	// The tracer closes after every span producer (lifecycle watcher, job
	// runs, proxied requests and upgraded sessions) has stopped, so spans
	// ended during shutdown are exported. The flush gets its own bounded
	// context because the steps above may have spent shutdownCtx.
	if tracer != nil {
		flushCtx, cancelFlush := context.WithTimeout(context.Background(), 5*time.Second)
		if err := tracer.Shutdown(flushCtx); err != nil {
			slog.Warn("tracer shutdown", "err", err)
		}
		cancelFlush()
	}
	if stopUsage != nil {
		stopUsage()
	}
	if preservingReload {
		// Session accounting can retire independently of the native log pipes.
		_ = relayLease.Publish(nil)
		mgr.WaitOwnedOutput(ctx)
		slog.Info("retired process exiting", "pid", os.Getpid())
	}

	switch cfg.Server.ShutdownApps {
	case "stop":
		if handedOff {
			slog.Info("leaving app processes running for handoff successor despite server.shutdown_apps=stop")
			break
		}
		slog.Info("stopping app processes (server.shutdown_apps=stop)")
		if err := mgr.StopAll(); err != nil {
			slog.Warn("stop app processes", "err", err)
		}
	default: // "adopt"
		slog.Info("leaving app processes running for re-adoption (server.shutdown_apps=adopt)")
	}

	if stopWorkloadMetrics != nil {
		stopWorkloadMetrics()
	}
	slog.Info("shutdown complete")
	return nil
}

// validateStoredProducerTopology catches legacy declarations and
// configuration-only topology drift before the server can admit traffic. In
// particular, an app whose stored isolation or placement inherits a changed
// fleet default has not passed through any PATCH/deploy validation. Execution
// repeats these checks at the physical process boundary, but startup must fail
// first: otherwise an upgraded server can look healthy until the next cron tick
// silently rejects a previously supported producer.
func validateStoredProducerTopology(store *db.Store, runtimeCfg config.RuntimeConfig) error {
	apps, err := store.ListApps(0, 0)
	if err != nil {
		return fmt.Errorf("list apps: %w", err)
	}
	var topologyErrs []error
	for _, app := range apps {
		schedules, err := store.ListSchedulesByApp(app.ID)
		if err != nil {
			return fmt.Errorf("list schedules for %s: %w", app.Slug, err)
		}
		for _, schedule := range schedules {
			if !schedule.Enabled || (schedule.DeployTrigger == schedulespec.DeployTriggerNever && schedule.OnSuccess != "roll") {
				continue
			}

			// A producer publishes behind locks that only local native processes
			// inherit, so every tier the app's processes run on must be native:
			// the placed replica tiers and, under elastic isolation, the default
			// tier its workers spawn on. Worker isolation itself is not judged.
			placement := app.PlacementMap()
			if len(placement) == 0 {
				placement = map[string]int{runtimeCfg.DefaultTierName(): app.Replicas}
			}
			roles := make(map[string]string, len(placement)+1)
			for tier := range placement {
				roles[tier] = "is placed on"
			}
			mode, policyErr := store.ServingWorkerIsolation(app, runtimeCfg.DefaultWorkerIsolation)
			if policyErr != nil {
				return fmt.Errorf("resolve serving isolation for %s: %w", app.Slug, policyErr)
			}
			if mode != "multiplex" {
				spawnTier := runtimeCfg.DefaultTierName()
				if spawnTier == "" {
					spawnTier = process.DefaultTier
				}
				if _, placed := roles[spawnTier]; !placed {
					roles[spawnTier] = "starts elastic workers on"
				}
			}
			tiers := make([]string, 0, len(roles))
			for tier := range roles {
				tiers = append(tiers, tier)
			}
			sort.Strings(tiers)
			for _, tier := range tiers {
				runtimeName, ok := runtimeCfg.RuntimeForTier(tier)
				// Config loading synthesizes the local tier, but preserve the zero-value
				// fallback for maintenance callers and focused tests that construct a
				// RuntimeConfig directly.
				if !ok && len(runtimeCfg.Tiers) == 0 && tier == runtimeCfg.DefaultTierName() {
					runtimeName = runtimeCfg.Mode
					if runtimeName == "" {
						runtimeName = "native"
					}
					ok = true
				}
				if !ok {
					topologyErrs = append(topologyErrs, fmt.Errorf("app %q producer schedule %q: the app %s unresolved tier %q; restore that tier in runtime.tiers or move/disable the producer before restarting", app.Slug, schedule.Name, roles[tier], tier))
					continue
				}
				if runtimeName != "native" {
					topologyErrs = append(topologyErrs, fmt.Errorf("app %q producer schedule %q: the app %s tier %q using runtime %q; data-producing schedules require a local native tier, so move the app or disable its producer policy before restarting", app.Slug, schedule.Name, roles[tier], tier, runtimeName))
				}
			}
		}
	}
	return errors.Join(topologyErrs...)
}

// appNavHomeURL is the dashboard the app switcher links back to.
//
// It must be absolute whenever apps are served from their own origin: a bare
// "/" on an app page then addresses the app origin's root, which serves nothing
// - and that origin exists precisely so app pages cannot reach the dashboard by
// a relative path. config.Load requires base_url in exactly that case, so the
// fallback only ever applies to a single-origin deployment, where "/" is both
// correct and immune to a stale base_url.
func appNavHomeURL(cfg *config.Config) string {
	if cfg.Server.BaseURL != "" {
		return strings.TrimSuffix(cfg.Server.BaseURL, "/") + "/"
	}
	return "/"
}

// registerBrandingRoutes wires all branding-aware frontend routes onto mux.
// It is extracted from runServe so the integration test can call the identical
// production registration logic without duplicating the wiring.
//
// Routes registered:
//   - /apps/, /users, /audit-log, /login  - SPA shell (branded or stock)
//   - /favicon.ico, app-origin favicon    - effective platform tab identity
//   - /branding/                          - operator asset files (only when assets present)
//   - /.shinyhub/branding.json            - public branding metadata
//   - /.shinyhub/apps.json                - app list (optional auth)
//   - /                                   - landing page or SPA shell
func registerBrandingRoutes(mux *http.ServeMux, cfg *config.Config, srv *api.Server, store *db.Store, appUserLookup auth.UserLookup) {
	brandingActive := cfg.Branding.IsActive()
	resolved := cfg.Branding.ResolvedAssets()

	// serveShell serves the SPA shell (ui.ShellHTML: index.html pointed at the
	// immutable versioned asset tree), branded when branding is active. Under
	// SHINYHUB_DEV_STATIC the shell is re-read from disk per request so live
	// reload still works.
	pub := ui.PublicBranding(cfg.Branding, resolved)
	// One canonical handler backs explicit links on standalone ShinyHub pages
	// and the browser's implicit /favicon.ico request for custom landing pages.
	// PlatformURL stays under /app/ so it is also reachable on an isolated app
	// origin, whose virtual-host boundary rejects every other frontend path.
	platformFavicon := ui.FaviconHandler(cfg.Branding)
	mux.Handle("GET "+favicon.RootURL, platformFavicon)
	mux.Handle("GET "+favicon.PlatformURL, platformFavicon)
	devStatic := os.Getenv("SHINYHUB_DEV_STATIC") != ""
	serveShell := func(w http.ResponseWriter, r *http.Request) {
		// An authenticated request (the request that fetches the shell is itself
		// behind forward auth or a session) gets the shell pre-marked "in" so the
		// dashboard chrome paints immediately, with no boot splash and no flash of
		// the login form. This needs a per-request render, so it leaves the cached
		// ServeFileFS fast path only when there is a reason to.
		authed := auth.UserFromContext(r.Context()) != nil
		// The shell names the build's versioned asset URLs, so a stored copy
		// must be revalidated before reuse: an old shell would keep pointing an
		// upgraded server's visitors at the previous build's assets. A caller
		// that already set a stricter policy (no-store on an auth-varying
		// root) keeps it.
		if w.Header().Get("Cache-Control") == "" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		// gzip is negotiated only for a plain GET/HEAD with no Range: a Range
		// request keeps behaving exactly as before (identity bytes), since
		// neither shell path serves partial content against a compressed
		// representation.
		gzipEligible := (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
			r.Header.Get("Range") == "" && ui.AcceptsGzip(r)
		if !brandingActive && !authed {
			if gzipEligible {
				if raw, err := ui.ShellHTML(); err == nil {
					key := ""
					if !devStatic {
						key = "shell:stock"
					}
					ui.ServeHTML(w, r, raw, key)
					return
				}
			}
			if devStatic {
				http.ServeFileFS(w, r, ui.Static(), "index.html")
				return
			}
			raw, err := ui.ShellHTML()
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(raw))
			return
		}
		raw, err := ui.ShellHTML()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		out := raw
		if brandingActive {
			out, err = ui.RenderIndex(raw, pub)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
		if authed {
			out = ui.StampAuthenticated(out)
		}
		if gzipEligible {
			key := ""
			if !devStatic {
				key = fmt.Sprintf("shell:branding=%#v:authed=%t", pub, authed)
			}
			ui.ServeHTML(w, r, out, key)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(out)
	}

	// notFound serves the dashboard's own styled not-found page (instead of
	// Go's plain-text default) for any path in the dashboard's route space
	// that matches nothing. /api/ and /app/ are registered as their own,
	// more specific mux patterns elsewhere and never reach it.
	notFound := ui.NotFoundHandler(cfg.Branding.Environment)

	// SPA routes: /apps/<slug>..., /users, /audit-log, /login. The handler
	// 404s anything outside the IsUIPath allowlist, so legitimate unknowns
	// still return 404 rather than rendering the SPA shell.
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ui.IsUIPath(r.URL.Path) {
			notFound.ServeHTTP(w, r)
			return
		}
		serveShell(w, r)
	})
	// Register every exact SPA route from the single source of truth in the ui
	// package, so the mux registrations and IsUIPath cannot drift (a route in one
	// but not the other would silently 404 on deep links). Includes /login (always
	// serves the shell even when landing_page is set), /home (stable authenticated
	// home the SPA resolves to Overview/Launchpad), and /tokens.
	for _, route := range ui.ExactUIRoutes() {
		mux.Handle(route, spa)
	}
	mux.Handle("/apps/", spa)     // prefix: /apps/<slug> and /apps/<slug>/<tab>
	mux.Handle("/projects/", spa) // prefix: /projects/<slug>

	if brandingActive && len(resolved) > 0 {
		mux.Handle("/branding/", ui.BrandingAssetHandler(resolved))
	}

	// /.shinyhub/* routes use optional-identity: resolve a session/bearer user
	// if present, but do NOT 401 anonymous callers. This mirrors the /app/*
	// userLookup wiring.
	optionalUser := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if u := access.ResolveOptionalUser(r, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup); u != nil {
				r = r.WithContext(auth.WithUser(r.Context(), u))
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/.shinyhub/branding.json", srv.HandleBrandingJSON)
	mux.HandleFunc("/.shinyhub/apps.json", optionalUser(srv.HandleAppsJSON))

	landingFile := cfg.Branding.LandingFile()
	rootBehavior := cfg.Branding.EffectiveRootBehavior()

	// isShinyHubUser reports whether the request is authenticated as a ShinyHub
	// user by any method the app accepts. ResolveOptionalUser checks the context
	// user first (forward-auth, when enabled its middleware wraps this mux) then
	// the session cookie, so this covers both. Edge SSO that ShinyHub is NOT
	// configured to trust reaches neither, so such a visitor is correctly treated
	// as anonymous and sees the landing page. This is the root dispatcher's signal.
	isShinyHubUser := func(r *http.Request) bool {
		return access.ResolveOptionalUser(r, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup) != nil
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			notFound.ServeHTTP(w, r)
			return
		}
		if landingFile != "" {
			// `/` now varies by auth state, so it must never be shared-cached: a
			// proxy could otherwise hand one viewer's response to another.
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("Vary", "Cookie")
			// auto: anonymous sees the landing page, a signed-in user falls through
			// to the SPA home. landing: always the landing page (a pure portal).
			if rootBehavior == "landing" || !isShinyHubUser(r) {
				// Operator-supplied trusted HTML may use inline scripts/styles, which
				// the strict SPA CSP (set by SecurityHeaders) would block. Relax it to
				// the inline-permitting policy for this one response.
				w.Header().Set("Content-Security-Policy", api.LandingPageCSP())
				http.ServeFile(w, r, landingFile)
				return
			}
		}
		serveShell(w, r)
	})
}

// sweepOrphanTempfiles removes stale entries from each app's
// .shinyhub-upload-tmp/ directory left behind by interrupted PUT uploads.
// Failures are logged and otherwise ignored — startup must succeed even when
// a single app's data dir is unreadable.
func sweepOrphanTempfiles(appDataRoot string) {
	entries, err := os.ReadDir(appDataRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("sweep app-data dir", "err", err)
		}
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		appDir := filepath.Join(appDataRoot, e.Name())
		if err := data.CleanupUploadTemp(appDir, time.Hour); err != nil {
			slog.Warn("sweep upload temp", "slug", e.Name(), "err", err)
		}
	}
}

// apiTimeoutHandler wraps the API router with a 30s per-request timeout,
// exempting routes that are either long-lived by design or stream a
// large request body. See isLongLivedAPIRoute for the matrix.
func apiTimeoutHandler(h http.Handler) http.Handler {
	timed := http.TimeoutHandler(h, 30*time.Second, `{"error":"request timeout"}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLongLivedAPIRoute(r.Method, r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		timed.ServeHTTP(w, r)
	})
}

// isLongLivedAPIRoute reports whether method+path identifies an API
// route that must bypass the per-request timeout. Matching is exact on
// both the HTTP method and the full /api/apps/{slug}/... shape so an
// unrelated route that merely ends in one of these words (or uses a
// different method) keeps the 30s timeout. Qualifying routes:
//
//   - GET /api/apps/{slug}/logs and
//     GET /api/apps/{slug}/schedules/{id}/runs/{run_id}/logs —
//     server-sent log streams that stay open by design.
//   - POST /api/apps/{slug}/deploy and POST /api/apps/{slug}/drafts —
//     bundle uploads, body can be hundreds of MB.
//   - POST /api/apps/{slug}/restart, POST|PUT /api/apps/{slug}/rollback,
//     POST /api/apps/{slug}/stop — pool swaps that stop and relaunch
//     every replica under the per-slug deploy lock. These can
//     legitimately exceed 30s (dependency-heavy launches). Letting
//     http.TimeoutHandler fire would return "request timeout" to the
//     client while the swap keeps mutating runtime + DB state, leaving
//     the two divergent and the operator misinformed.
//   - PUT /api/apps/{slug}/data/<rel> — per-app data upload, also
//     arbitrary-size. Without this exemption http.TimeoutHandler swaps
//     the response writer mid-stream at 30s; the handler keeps writing
//     to a now-disconnected recorder, the file may still complete on
//     disk, and the client sees an ambiguous "request timeout" body
//     instead of either a clean success or a clean failure.
//
// All other API routes keep the 30s timeout so a slow handler cannot
// pin a server goroutine indefinitely.
func isLongLivedAPIRoute(method, path string) bool {
	const prefix = "/api/apps/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := path[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return false // bare /api/apps/{slug}, no sub-resource
	}
	sub := rest[slash+1:] // path after "{slug}/"

	switch sub {
	case "logs":
		return method == http.MethodGet
	case "deploy", "drafts", "restart", "stop":
		return method == http.MethodPost
	case "rollback":
		return method == http.MethodPost || method == http.MethodPut
	}
	if method == http.MethodPut && strings.HasPrefix(sub, "data/") && len(sub) > len("data/") {
		return true
	}
	if method == http.MethodGet && isScheduleRunLogsPath(sub) {
		return true
	}
	return false
}

// isScheduleRunLogsPath matches the sub-resource
// "schedules/{id}/runs/{run_id}/logs" with non-empty id and run_id.
func isScheduleRunLogsPath(sub string) bool {
	seg := strings.Split(sub, "/")
	return len(seg) == 5 &&
		seg[0] == "schedules" && seg[1] != "" &&
		seg[2] == "runs" && seg[3] != "" &&
		seg[4] == "logs"
}

// apiHandler assembles the middleware chain mounted at /api/: the body cap
// outermost so it sees the raw request body, then Observe around the timeout
// handler (not the inner router) so server metrics and traces record the
// status/latency the client actually sees, including timeout 503s and
// recovered-panic 500s. Observe is a no-op unless metrics or tracing are
// enabled. Tests build the production chain through this function so a route
// exemption is proven against the same wrappers serve installs.
func apiHandler(srv *api.Server) http.Handler {
	return bodyLimitHandler(srv.Observe(apiTimeoutHandler(srv.Router())))
}

// maxAPIJSONBody caps the request body for ordinary JSON API endpoints so a
// malicious or buggy client cannot exhaust server memory with an unbounded
// body. Bulk-upload routes (bundle deploy, per-app data PUT) are exempt; their
// handlers apply their own, much larger, http.MaxBytesReader limits.
const maxAPIJSONBody = 4 << 20 // 4 MiB

// bodyLimitHandler wraps r.Body in an http.MaxBytesReader for every /api route
// except the bulk-upload routes (see isLargeUploadRoute), bounding the memory a
// single request can consume. It is mounted outermost on the /api handler so it
// sees the raw ResponseWriter and the real request body before any inner
// wrapper, and so the bulk-upload handlers can still install their own limits.
func bodyLimitHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && !isLargeUploadRoute(r.Method, r.URL.Path) {
			r.Body = http.MaxBytesReader(w, r.Body, maxAPIJSONBody)
		}
		next.ServeHTTP(w, r)
	})
}

// isLargeUploadRoute reports whether method+path is a bulk-upload API route that
// legitimately carries a body larger than maxAPIJSONBody and applies its own
// size limit downstream: POST /api/apps/{slug}/deploy and
// POST /api/apps/{slug}/drafts (bundle zip) and PUT /api/apps/{slug}/data/<rel>
// (per-app data file).
func isLargeUploadRoute(method, path string) bool {
	const prefix = "/api/apps/"
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	rest := path[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return false
	}
	sub := rest[slash+1:]
	if sub == "deploy" || sub == "drafts" {
		return method == http.MethodPost
	}
	if method == http.MethodPut && strings.HasPrefix(sub, "data/") && len(sub) > len("data/") {
		return true
	}
	return false
}

// needsUnboundedDeadline reports whether a request must run without the
// server's connection-level read/write deadlines. These are the streaming and
// long-running surfaces that ReadTimeout/WriteTimeout would otherwise sever
// mid-flight:
//
//   - /app/* — the reverse proxy to app processes, which carries Shiny
//     WebSockets and arbitrarily long streaming responses.
//   - /internal/runtime-bundle/* — streams a deployment bundle (large body).
//   - /internal/fargate-bundle/* — compatibility alias for older runners.
//   - the long-lived /api/ routes enumerated by isLongLivedAPIRoute (SSE log
//     streams, bundle uploads, lifecycle swaps, per-app data uploads).
//
// Every other control-plane route keeps the deadlines so a slow-read or
// slow-body client cannot pin a connection (and its goroutine) indefinitely.
func needsUnboundedDeadline(method, path string) bool {
	switch {
	case strings.HasPrefix(path, "/app/"):
		return true
	case strings.HasPrefix(path, "/internal/fargate-bundle/"):
		return true
	case strings.HasPrefix(path, "/internal/runtime-bundle/"):
		return true
	case strings.HasPrefix(path, "/api/"):
		return isLongLivedAPIRoute(method, path)
	}
	return false
}

// deadlineExemptions clears the per-connection read and write deadlines for the
// streaming and long-running routes identified by needsUnboundedDeadline. The
// server-level ReadTimeout/WriteTimeout then act as a slow-client backstop on
// ordinary responses without severing legitimate long-lived connections
// (WebSocket proxying, SSE log tails, large uploads, dependency-heavy launches).
func deadlineExemptions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if needsUnboundedDeadline(r.Method, r.URL.Path) {
			rc := http.NewResponseController(w)
			// Best-effort: the underlying writer supports deadlines; ignore
			// ErrNotSupported so a future wrapper can't break streaming.
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
		}
		next.ServeHTTP(w, r)
	})
}
