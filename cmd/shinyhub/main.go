package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/appenv"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/autoscale"
	"github.com/rvben/shinyhub/internal/backup"
	"github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/data"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fargate"
	"github.com/rvben/shinyhub/internal/history"
	"github.com/rvben/shinyhub/internal/identity"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/leader"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/logging"
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/sandbox"
	"github.com/rvben/shinyhub/internal/secrets"
	"github.com/rvben/shinyhub/internal/servertrace"
	"github.com/rvben/shinyhub/internal/tracing"
	"github.com/rvben/shinyhub/internal/ui"
	"github.com/rvben/shinyhub/internal/upgrade"
	"github.com/rvben/shinyhub/internal/worker"
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
		"                          Set HOST and TOKEN together to skip `shinyhub login` (CI).\n" +
		"  SHINYHUB_CONFIG         Credentials file path (default ~/.config/shinyhub/config.json).\n" +
		"  SHINYHUB_DEPLOY_TOKEN   (server) Pre-shared deploy token; clients pass it as SHINYHUB_TOKEN.\n\n" +
		"Run `shinyhub schema` for a full machine-readable contract.",
	SilenceErrors: true,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the ShinyHub server",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		logger := logging.New()
		slog.SetDefault(logger)
		return runServe(ctx, logger)
	},
}

var backupOut string

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
		"pg_restore on PATH.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadForMaintenance(serverConfigPath())
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		moved, err := backup.Restore(cfg, args[0])
		for _, p := range moved {
			fmt.Fprintf(cmd.ErrOrStderr(), "previous state preserved at %s\n", p)
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

		n, caRotated, err := store.RotateSecretsTx(reEnv, reCA)
		if err != nil {
			return fmt.Errorf("rotation failed (no changes committed - safe to retry): %w", err)
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"Rotated %d app-env secret(s)%s.\nNext: set SHINYHUB_AUTH_SECRET to the new value and start the server.\n",
			n, caRotatedSuffix(caRotated))
		return cli.RenderAction(cmd, "rotated",
			map[string]any{"env_secrets": n, "worker_ca_rotated": caRotated},
			fmt.Sprintf("rotated %d env secret(s); worker_ca_rotated=%v", n, caRotated))
	},
}

func caRotatedSuffix(rotated bool) string {
	if rotated {
		return " and the worker CA key"
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
	backupCmd.Flags().StringVar(&backupOut, "out", "", "Destination archive path (.tar.gz)")
	_ = backupCmd.MarkFlagRequired("out")
	migrateBackendCmd.Flags().StringVar(&migrateTargetDSN, "to", "", "Target Postgres DSN (overrides SHINYHUB_TARGET_DSN)")
	const configUsage = "Path to the server config file (overrides SHINYHUB_CONFIG; default ./shinyhub.yaml)"
	for _, c := range []*cobra.Command{serveCmd, backupCmd, restoreCmd, rotateSecretCmd, migrateBackendCmd} {
		c.Flags().StringVar(&configPath, "config", "", configUsage)
	}
}

var buildRootOnce sync.Once

// buildRoot wires the complete command tree: server-side commands plus the
// client CLI. The sync.Once guard makes it safe for main() and any number of
// tests to call; registration happens exactly once per process.
func buildRoot() *cobra.Command {
	buildRootOnce.Do(func() {
		rootCmd.AddCommand(serveCmd, backupCmd, restoreCmd, rotateSecretCmd, migrateBackendCmd, newWorkerCmd(), newSandboxCmd())
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
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics server", "err", err)
		}
	}()
	return srv, ln, nil
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

// runMaintenance periodically prunes the audit log and per-schedule run history
// to keep those tables bounded. It runs on the owner instance only (so HA
// standbys never prune concurrently) and is a no-op when no retention is
// configured. It prunes once promptly on start, then on each interval tick.
func runMaintenance(ctx context.Context, store *db.Store, cfg config.MaintenanceConfig) {
	auditRetention := time.Duration(cfg.AuditRetentionDays) * 24 * time.Hour
	keepRuns := cfg.ScheduleRunRetentionCount
	// The database-backed login limiter only writes rows on a Postgres backend,
	// so its ledger only needs sweeping there. Retention must comfortably exceed
	// the longest limiter window (login uses 1m) so an in-window row is never
	// dropped; raise it if a longer-window bucket is ever added.
	pruneRateLimits := store.IsPostgres()
	const rateLimitRetention = time.Hour
	if auditRetention <= 0 && keepRuns <= 0 && !pruneRateLimits {
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
		if keepRuns > 0 {
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
		if pruneRateLimits {
			if n, err := store.PruneRateLimitCounters(rateLimitRetention); err != nil {
				slog.Warn("prune_rate_limit_counters_failed", "err", err)
			} else if n > 0 {
				slog.Info("pruned_rate_limit_counters", "removed", n)
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
// operator to use an off-host tier (remote_docker or fargate).
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
					"(Postgres DSN detected); use an off-host runtime (remote_docker or fargate) instead",
				tier.Name, tier.Runtime,
			)
		}
	}
	return nil
}

// buildRuntime constructs a process.Runtime for a single tier from its TierConfig.
// Docker tiers share the daemon settings from cfg.Runtime.Docker; a burst tier
// may therefore point at the same daemon under a distinct tier name. Fargate tiers
// share cfg.Runtime.Fargate (one ECS cluster) but may use different launch types.
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
		nativeRT := process.NewNativeRuntime()
		// Enable warm-wake (SIGSTOP freeze + per-app cgroup reclaim) when
		// configured. If the delegated cgroup subtree cannot be prepared at
		// runtime (no systemd Delegate=memory, no cgroup v2, or no swap), the
		// runtime degrades gracefully: Suspend reports not-supported and the
		// watcher hibernates via Stop exactly as before.
		nativeRT.SetSnapshot(cfg.Runtime.Snapshot.Enabled, cfg.Runtime.Snapshot.ReclaimMinFraction)
		nativeRT.SetIsolation(sandbox.Level(cfg.Runtime.Native.Isolation))
		return nativeRT, nil
	case "fargate":
		return buildFargateRuntime(ctx, cfg, tier, bundleTokenKey)
	case "remote_docker":
		// Handled upstream: remote tiers are registered via NewRemoteRuntime before
		// RegisterRuntime; buildRuntime is not called for remote_docker tiers.
		return nil, fmt.Errorf("buildRuntime called for remote_docker tier; wire via NewRemoteRuntime instead")
	default:
		return nil, fmt.Errorf("unsupported runtime mode: %s", tier.Runtime)
	}
}

// deriveBundleTokenKey derives the 32-byte key used to mint and verify Fargate
// bundle tokens. Key material comes from the same auth secret as all other
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
		opts = append(opts, fargate.WithEC2Client(ec2.NewFromConfig(awsCfg)))
	}
	if fc.SecretsNamePrefix != "" {
		// Route apps' secret env vars through AWS Secrets Manager (referenced by
		// ARN from a per-app task-def secrets block) instead of plaintext task
		// overrides, so they never appear in ecs:DescribeTasks.
		opts = append(opts, fargate.WithSecretsStore(
			fargate.NewSecretsManagerStore(secretsmanager.NewFromConfig(awsCfg), fc.SecretsKMSKeyID)))
	}
	return fargate.New(ecs.NewFromConfig(awsCfg), fargate.Config{
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

// hostSampler samples PID-backed replicas (native) via gopsutil and reports
// PID-less replicas (docker/remote/fargate handles, whose RunHandle carries a
// ContainerID rather than a PID) as zero usage without error. Returning no error
// for the PID-less case is deliberate: the status endpoint treats a sampler error
// as a dead replica, so a running replica on a container/fargate tier must not be
// probed by PID (PID 0) and misreported as stopped.
type hostSampler struct{ gops process.GopsutilSampler }

func (h *hostSampler) Sample(handle process.RunHandle) (process.Stats, error) {
	if handle.PID == 0 {
		return process.Stats{}, nil
	}
	return h.gops.Sample(handle)
}

func runServe(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.Load(serverConfigPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
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
	defer upg.Stop()

	// SIGHUP -> upgrade; ctx cancel (SIGINT/SIGTERM) -> Stop (closes Exit).
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	defer signal.Stop(sighup)
	upgrade.WireSignals(ctx, upg, sighup, logger)

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
	if err := store.Migrate(); err != nil {
		return fmt.Errorf("db migrate: %w", err)
	}
	// Refuse to serve a database that a newer build already migrated past this
	// binary: the schema would carry columns/tables this code cannot read,
	// risking silent corruption. Fail fast with an actionable message instead.
	if err := store.VerifySchemaCompatibility(); err != nil {
		return fmt.Errorf("schema compatibility: %w", err)
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

	// Bootstrap admin user from env if provided and no users exist
	if adminUser := os.Getenv("SHINYHUB_ADMIN_USER"); adminUser != "" {
		adminPass := os.Getenv("SHINYHUB_ADMIN_PASSWORD")
		if adminPass == "" {
			return fmt.Errorf("SHINYHUB_ADMIN_PASSWORD must not be empty when SHINYHUB_ADMIN_USER is set")
		}
		_, err := store.GetUserByUsername(adminUser)
		if errors.Is(err, db.ErrNotFound) {
			hash, err := auth.HashPassword(adminPass)
			if err != nil {
				return fmt.Errorf("hash admin password: %w", err)
			}
			if err := store.CreateUser(db.CreateUserParams{
				Username:     adminUser,
				PasswordHash: hash,
				Role:         "admin",
			}); err != nil {
				slog.Warn("could not create admin user", "err", err)
			} else {
				slog.Info("admin user created", "username", adminUser)
			}
		} else if err != nil {
			return fmt.Errorf("check admin user: %w", err)
		}
	}

	var deployToken *auth.DeployToken
	if cfg.Auth.DeployToken != "" {
		if err := auth.ValidateDeployTokenFormat(cfg.Auth.DeployToken); err != nil {
			return fmt.Errorf("SHINYHUB_DEPLOY_TOKEN: %w", err)
		}
		sysUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, cfg.Auth.DeployTokenRole)
		if err != nil {
			return fmt.Errorf("upsert deploy system user: %w", err)
		}
		deployToken = auth.NewDeployToken(cfg.Auth.DeployToken, &auth.ContextUser{
			ID:       sysUser.ID,
			Username: sysUser.Username,
			Role:     sysUser.Role,
		})
		slog.Info("deploy token registered",
			"username", sysUser.Username,
			"role", sysUser.Role)
	}

	// Refuse local-only runtimes in a clustered (Postgres) deployment before
	// constructing any tier. Native and docker processes bind loopback on one
	// host; other CP instances cannot reach them, so they are incompatible with
	// shared state across nodes.
	if err := checkClusteredRuntimeTiers(cfg); err != nil {
		return err
	}

	// Build one runtime per configured tier. The first tier is the default
	// (config.Load synthesizes a single "local" tier from Mode when none are
	// declared, so single-node behavior is unchanged). The default tier's
	// runtime backs the Manager and every runtime-typed consumer (sampler,
	// jobs, recovery lister, sweeper); additional tiers are registered so
	// placement can route replicas to them.
	defaultTier := cfg.Runtime.DefaultTierName()
	// Find the full TierConfig for the default tier so LaunchType is carried through.
	defaultTierCfg := config.TierConfig{Name: defaultTier, Runtime: cfg.Runtime.Mode}
	for _, t := range cfg.Runtime.Tiers {
		if t.Name == defaultTier {
			defaultTierCfg = t
			break
		}
	}
	rt, err := buildRuntime(ctx, defaultTierCfg, cfg, bundleTokenKey)
	if err != nil {
		return err
	}
	slog.Info("runtime configured", "tier", defaultTier, "mode", defaultTierCfg.Runtime)
	mgr := process.NewManager(cfg.Storage.AppsDir, rt)
	mgr.SetDefaultTier(defaultTier)
	mgr.SetStopGrace(cfg.Server.StopGrace)
	for _, tierCfg := range cfg.Runtime.Tiers {
		if tierCfg.Name == defaultTier {
			continue
		}
		if tierCfg.Runtime == "remote_docker" {
			if workerReg == nil || dialer == nil {
				return fmt.Errorf("tier %q requires worker hosting to be enabled", tierCfg.Name)
			}
			mgr.RegisterRuntime(tierCfg.Name, worker.NewRemoteRuntime(workerReg, tierCfg.Name, dialer))
			slog.Info("remote runtime tier registered", "tier", tierCfg.Name, "mode", tierCfg.Runtime)
			continue
		}
		tierRT, err := buildRuntime(ctx, tierCfg, cfg, bundleTokenKey)
		if err != nil {
			return err
		}
		mgr.RegisterRuntime(tierCfg.Name, tierRT)
		slog.Info("runtime tier registered", "tier", tierCfg.Name, "mode", tierCfg.Runtime)
	}
	mgr.SetEnvResolver(func(slug string) ([]string, []string, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, nil, err
		}
		vars, err := store.ListAppEnvVars(app.ID)
		if err != nil {
			return nil, nil, err
		}
		env, secretEnv, err := appenv.Resolve(vars, secretsKey)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve env for app %s: %w", slug, err)
		}
		return env, secretEnv, nil
	})
	mgr.SetSharedMountResolver(func(slug string) ([]process.SharedMount, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, err
		}
		rows, err := store.ListSharedDataSources(app.ID)
		if err != nil {
			return nil, err
		}
		out := make([]process.SharedMount, 0, len(rows))
		for _, m := range rows {
			out = append(out, process.SharedMount{
				SourceSlug: m.SourceSlug,
				HostPath:   filepath.Join(absAppDataDir, m.SourceSlug),
			})
		}
		return out, nil
	})
	if err := mgr.SetAppDataRoot(absAppDataDir); err != nil {
		return fmt.Errorf("set app data root: %w", err)
	}

	// Tracing: shared ring buffer surfaced by the /traces handler, plus
	// platform-default OTEL_* env vars injected into every app process so
	// Shiny's built-in OpenTelemetry exporter reaches the configured backend
	// without per-app configuration. Both are no-ops when cfg.Tracing.Enabled
	// is false.
	traceBuffer := tracing.NewBuffer(cfg.Tracing.RingBufferSize, time.Duration(cfg.Tracing.SlowRequestMS)*time.Millisecond)
	mgr.SetPlatformDefaultEnvResolver(func(slug string, replica int) []string {
		env := tracing.EnvFor(cfg.Tracing, slug, replica)
		// Identity: every app receives its verification key and its own
		// slug, unconditionally (the key alone discloses nothing - it only
		// verifies tokens that are never minted while forwarding is
		// disabled - and unconditional injection means enabling the flag
		// later doesn't strand running apps without a key). The numeric ID
		// scopes the key so a delete-and-recreate under the same slug
		// cannot inherit its predecessor's key.
		app, err := store.GetApp(slug)
		if err != nil {
			slog.Warn("identity: resolve app for key derivation; skipping identity env", "slug", slug, "err", err)
			return env
		}
		key := identity.DeriveKey(cfg.Auth.Secret, app.ID)
		return append(env,
			"SHINYHUB_IDENTITY_KEY="+hex.EncodeToString(key),
			"SHINYHUB_APP_SLUG="+slug,
		)
	})
	// The Enabled conjunct is belt-and-braces on top of config validation:
	// wrapping apps that would export nowhere must be impossible.
	mgr.SetAutoInstrumentAppsDefault(cfg.Tracing.Enabled && cfg.Tracing.AutoInstrumentApps)

	prx := proxy.New()
	prx.SetTracing(cfg.Tracing, traceBuffer)
	// Trust forwarding headers only from the configured upstream proxies; a
	// direct client's X-Forwarded-* / Forwarded values are stripped before the
	// request reaches an app backend.
	prx.SetTrustedProxies(cfg.TrustedProxyNets)
	// Sign the sticky-routing cookie so a client cannot forge it to pin a
	// replica and bypass the per-replica session cap.
	prx.SetStickySecret(deriveStickyCookieKey(cfg.Auth.Secret))
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
	var reporterWG sync.WaitGroup
	var reporterCancel context.CancelFunc
	if isClustered(cfg) {
		flushCh := make(chan string, 16)
		prx.EnableImmediateFlush(flushCh)
		reporter := proxy.NewSessionReporter(prx, store, cfg.Server.InstanceID, flushCh)
		reporterCtx, cancelReporter := context.WithCancel(context.Background())
		reporterCancel = cancelReporter
		reporterWG.Add(1)
		go func() {
			defer reporterWG.Done()
			reporter.Run(reporterCtx)
		}()
		slog.Info("session reporter started",
			"interval", proxy.ReporterInterval,
			"stale_cutoff", proxy.ReplicaSessionStaleCutoff,
			"instance", cfg.Server.InstanceID)
	}

	// In clustered deployments every instance runs a pool syncer that
	// reconciles the proxy's backend pools against the DB replica table.
	// This lets standbys serve off-host apps without relying on the local
	// placement registry. Single-node deployments skip this entirely: the
	// deploy fast-path and RecoverProcesses remain the sole pool-population
	// paths, byte-for-byte unchanged.
	var syncerWG sync.WaitGroup
	var syncerCancel context.CancelFunc
	if isClustered(cfg) {
		transportBuilder := worker.NewReplicaTransportBuilder(dialer, store)
		syncer := proxy.NewPoolSyncer(prx, store, transportBuilder, slog.Default(), cfg.Auth.IdentityHeadersEnabled())
		syncerCtx, cancelSyncer := context.WithCancel(context.Background())
		syncerCancel = cancelSyncer
		syncerWG.Add(1)
		go func() {
			defer syncerWG.Done()
			syncer.Run(syncerCtx)
		}()
		// Wire the on-miss sync hook so a first-request for a freshly-started
		// app is served immediately without waiting for the next background tick.
		prx.SetOnMissSync(func(slug string) {
			syncer.SyncSlug(syncerCtx, slug)
		})
		slog.Info("pool syncer started", "interval", proxy.PoolSyncInterval)
	}

	srv := api.New(cfg, store, mgr, prx)
	srv.SetVersion(version)
	if isClustered(cfg) {
		srv.SetCluster(cfg.Server.InstanceID)
	}
	if deployToken != nil {
		srv.SetDeployToken(deployToken)
	}
	srv.SetSecretsKey(secretsKey)
	srv.SetTraceBuffer(traceBuffer)

	// When the Fargate secrets backend is configured, wire the per-app cleanup
	// (delete Secrets Manager entries + deregister task-def revisions) that runs
	// on app delete and on the startup tombstone reconcile. Every Fargate runtime
	// instance shares the cluster-wide secrets config, so the first one suffices.
	var fargateSecretsCleaner *fargate.Runtime
	if cfg.Runtime.Fargate.SecretsNamePrefix != "" {
		for _, tierName := range cfg.Runtime.TierOrder() {
			if frt, ok := mgr.RuntimeForTier(tierName).(*fargate.Runtime); ok {
				fargateSecretsCleaner = frt
				break
			}
		}
	}
	if fargateSecretsCleaner != nil {
		srv.SetSecretsCleaner(fargateSecretsCleaner)
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
	var metricsReg *metrics.Registry
	if cfg.Metrics.Enabled {
		reg := metrics.New(version)
		metricsReg = reg
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
		// Schedule last-success gauge: DB-backed (restart-safe), one sample per
		// schedule that has ever succeeded.
		if err := reg.Register(metrics.NewScheduleFreshnessCollector(func() ([]metrics.ScheduleSample, error) {
			rows, qerr := store.ScheduleFreshness()
			if qerr != nil {
				return nil, qerr
			}
			out := make([]metrics.ScheduleSample, len(rows))
			for i, fr := range rows {
				s := metrics.ScheduleSample{Slug: fr.Slug, Name: fr.Name}
				if fr.LastSuccessAt != nil {
					s.LastSuccessUnix = fr.LastSuccessAt.Unix()
					s.OK = true
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
		var mln net.Listener
		metricsSrv, mln, err = startMetricsListener(upg.Listen, cfg.Metrics.Addr, reg)
		if err != nil {
			return err
		}
		slog.Info("metrics listening", "addr", mln.Addr().String())
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
		tracer, err = servertrace.Setup(ctx, cfg.Tracing, version)
		if err != nil {
			return fmt.Errorf("server tracing setup: %w", err)
		}
		srv.SetTracer(tracer)
		slog.Info("server tracing enabled", "endpoint", cfg.Tracing.OTLPEndpoint, "protocol", cfg.Tracing.OTLPProtocol)
		// Tear down the tracer on any early error return below (idempotent with
		// the ordered shutdown path).
		defer func() { _ = tracer.Shutdown(context.Background()) }()
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
		slog.Info("proxy_access", attrs...)
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
	if defaultTierCfg.Runtime == "docker" {
		srv.SetSampler(&process.RuntimeSampler{Runtime: rt})
	} else {
		srv.SetSampler(&hostSampler{})
	}

	// Start the in-memory metrics-history collector unless disabled
	// (history_window == 0). It runs on every instance (recording the replicas it
	// runs locally) with its own sampler instance so its CPU deltas are
	// independent of the API's on-demand sampler. Cancelled + joined at shutdown
	// so it stops cleanly on SIGTERM and on a tableflip re-exec.
	var historyWG sync.WaitGroup
	var historyCancel context.CancelFunc
	if cfg.Metrics.HistoryWindow > 0 {
		histStore := history.NewStore(cfg.Metrics.HistoryWindow, cfg.Metrics.HistoryInterval)
		srv.SetHistory(histStore)
		var histSampler process.Sampler = &process.GopsutilSampler{}
		if defaultTierCfg.Runtime == "docker" {
			histSampler = &process.RuntimeSampler{Runtime: rt}
		}
		collector := history.NewCollector(mgr, prx, histSampler, histStore, cfg.Metrics.HistoryInterval)
		historyCtx, cancel := context.WithCancel(context.Background())
		historyCancel = cancel
		historyWG.Add(1)
		go func() {
			defer historyWG.Done()
			collector.Run(historyCtx)
		}()
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

	deployFn := func(slug, bundleDir string, index int) (*deploy.Result, error) {
		app, err := store.GetApp(slug)
		if err != nil {
			return nil, fmt.Errorf("get app for deploy: %w", err)
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
			// Pin a shared-mount consumer's restarted replica to the worker set
			// hosting its source data, matching the full-deploy placement so a
			// recovered replica lands beside the data it mounts.
			ColocateWorkers: srv.ColocationPins(app),
		}
		if deps, derr := store.ListDeployments(app.ID); derr == nil && len(deps) > 0 {
			p.ContentDigest = deps[0].ContentDigest
			p.DeploymentID = deps[0].ID
			p.AppVersion = deps[0].Version
		}
		return deploy.RunReplica(p, index)
	}

	// resumeFn restores a suspended replica via the Snapshotter path, mirroring
	// deployFn's Params shape (placement/tier so the route transport resolves).
	resumeFn := func(slug, bundleDir string, index int) (*deploy.Result, error) {
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
		if deps, derr := store.ListDeployments(app.ID); derr == nil && len(deps) > 0 {
			p.DeploymentID = deps[0].ID
			p.AppVersion = deps[0].Version
		}
		return deploy.ResumeReplica(p, index)
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
		Store:      store,
		Manager:    mgr,
		Proxy:      prx,
		RuntimeCfg: cfg.Runtime,
	}
	prx.SetSpawnFunc(func(slug string, slotID int) {
		go elasticSpawner.Spawn(slug, slotID)
	})
	prx.SetTerminateFunc(elasticSpawner.Terminate)

	// Let the proxy render a clear status page (instead of the endless loading
	// spinner) when a request hits an app that is crashed or stopped, surfacing
	// the crash reason recorded in apps.last_error.
	prx.SetAppStatusLookup(func(slug string) (string, string) {
		app, err := store.GetAppBySlug(slug)
		if err != nil {
			return "", ""
		}
		return app.Status, app.LastError
	})

	// Hold a request for a not-yet-routable app while its wake completes, so a
	// warm resume serves inline (no loading-page round-trip). 0 disables it.
	prx.SetWakeHoldTimeout(cfg.Lifecycle.WakeHold)

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

	// Wire lost-replica healing. For fargate tiers, ECS reachability is
	// validated at startup so the gate is always-true (fargate tasks are
	// never placed via a worker registry). For worker tiers, delegate to
	// the registry. Mixed deployments (fargate burst + worker tiers) are
	// handled by the combined predicate.
	//
	// Note: persistent RunTask failures still consume the normal crash-restart
	// budget, so always-true for fargate tiers cannot cause runaway
	// re-placement.
	hasFargateTier := false
	for _, name := range cfg.Runtime.TierOrder() {
		mode, _ := cfg.Runtime.RuntimeForTier(name)
		if mode == "fargate" {
			hasFargateTier = true
			break
		}
	}
	if workerReg != nil || hasFargateTier {
		watcher.EnableLostReplicaHealing(func(tier string) bool {
			mode, _ := cfg.Runtime.RuntimeForTier(tier)
			if mode == "fargate" {
				return true // ECS is always-reachable as a startup precondition
			}
			if workerReg != nil {
				_, ok := workerReg.WorkerForTier(tier)
				return ok
			}
			return false
		})
	}

	// reconcileCleaner is captured by ownerWork for the owner-scoped reconcile so
	// Fargate secrets are cleaned up only when this instance holds the lease.
	var reconcileCleaner lifecycle.AppSecretsCleaner
	if fargateSecretsCleaner != nil {
		reconcileCleaner = fargateSecretsCleaner
	}

	// Worker-down monitor: object created here, loop started inside ownerWork.
	// Only relevant when worker hosting is enabled.
	const (
		workerTimeout   = 90 * time.Second
		monitorInterval = 30 * time.Second
		// workerRetention is how long a down, non-revoked worker row with no
		// live replicas is kept before being reaped. It is far longer than the
		// down timeout so a briefly-flapping worker is never tombstoned.
		workerRetention = 24 * time.Hour
	)
	var monitor *lifecycle.WorkerDownMonitor
	if workerReg != nil {
		monitor = lifecycle.NewWorkerDownMonitor(store, workerTimeout, workerRetention, workerReg.MarkDown, func(slug string, index int, expectURL string) {
			prx.DeregisterReplicaIfTarget(slug, index, expectURL)
		}, mgr.EvictReplicaIfWorker, workerReg.Forget)
	}

	jobsMgr, err := jobs.NewManager(mgr, cfg.Runtime.TierOrder(), cfg.Runtime.DefaultTierName(), store, secretsKey, cfg.Storage.AppsDir, absAppDataDir)
	if err != nil {
		return fmt.Errorf("init jobs manager: %w", err)
	}
	// Cap scheduled jobs by the same per-replica memory/CPU ceiling as the app, so
	// a heavy refresh-data run cannot exceed the app's resource budget on a shared
	// native host.
	jobsMgr.SetResourceResolver(func(app *db.App) (memoryMB, cpuPct int) {
		defaultMem, defaultCPU := cfg.Runtime.DefaultResourcesForApp(app)
		return deploy.ResolveMemoryLimitMB(app.MemoryLimitMB, defaultMem),
			deploy.ResolveCPUQuotaPercent(app.CPUQuotaPercent, defaultCPU)
	})
	sched := scheduler.New(jobsMgr, store, cfg.Scheduler.Location)
	srv.SetJobs(jobsMgr, sched)

	// Push scheduled-run outcomes into Prometheus when metrics are enabled.
	if metricsReg != nil {
		jobsMgr.SetScheduleMetrics(metricsReg)
	}

	// Replica autoscaling is opt-in per app and gated by a global switch. When
	// enabled, the controller evaluates opted-in apps on its own interval and
	// drives the same incremental scale primitives the API exposes; it never
	// scales worker hosts. The loop is started inside ownerWork.
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

	// ownerWork runs the owner-only startup and background loops for one span of
	// ownership. It performs the five destructive boot-time reconciles/sweeps
	// exactly once (deferred until ownership so a coexisting new instance never
	// fails the old owner's in-flight deploy), starts the three background loops
	// bound to octx, then blocks until ownership is lost and stops them.
	// jobsMgr/sched/watcher/monitor/controller are created once above and reused
	// across spans (the scheduler supports stop/start cycles).
	// ownerReady is true only while this instance is the owner AND its worker
	// routing index has been refreshed for the current ownership span. The
	// mutation gates require it (not just IsOwner): the elector flips IsOwner true
	// before ownerWork runs, so without this a freshly-acquired owner could admit
	// deploy-placement / worker mutations against a stale index.
	var ownerReady atomic.Bool
	ownerWork := func(octx context.Context, epoch int64) {
		ownerReady.Store(false)       // closed at the start of every ownership span
		defer ownerReady.Store(false) // and on span exit (ownership lost / shutdown)
		slog.Info("became control-plane owner", "epoch", epoch, "instance", cfg.Server.InstanceID)

		// Rebuild the worker routing index from the authoritative DB so this new
		// active reflects every worker row the previous owner wrote before it died.
		// Fail-closed: retry until it succeeds or ownership is lost, so owner work
		// (placement-affecting reconciles, loops, and admitted mutations) never runs
		// on a stale index. A persistent failure means the DB is unusable, where no
		// owner work could function anyway.
		if workerReg != nil && !refreshUntilReady(octx, workerReg, registryRefreshBackoff, slog.Default()) {
			return // lost ownership before a fresh index; start no owner work
		}
		ownerReady.Store(true) // gate opens: the index is fresh

		// Rewrite any legacy relative bundle_dir rows to their canonical absolute
		// path under the configured apps_dir. Idempotent; must run before recovery
		// so process adoption sees absolute paths.
		if err := lifecycle.NormalizeBundleDirs(store, cfg.Storage.AppsDir); err != nil {
			slog.Error("bundle_dir backfill", "err", err)
		}
		// Fail any deploy interrupted mid-flight before recovery so adoption falls
		// back to the last good deployment.
		lifecycle.ReconcileInflightDeployments(store)
		// Finish any app deletion interrupted between the 'deleting' tombstone and
		// the row removal. Reconcile first so freshly-cleaned slugs are not flagged
		// as orphans by the next step.
		lifecycle.ReconcileDeletingApps(octx, store, cfg, reconcileCleaner)
		// Report (do not delete) slug dirs with no owning row. Run AFTER
		// ReconcileDeletingApps so freshly-cleaned slugs are not reported.
		lifecycle.LogOrphanAppDirs(store, cfg)
		// Re-adopt any processes that survived a server restart. Must run after
		// ReconcileInflightDeployments so recovery adopts the last-good deployment,
		// not a half-applied one.
		lifecycle.RecoverProcesses(store, mgr, prx, cfg.Runtime.DefaultMaxSessionsPerReplica, cfg.Auth.IdentityHeadersEnabled(), cfg.Runtime.DefaultWorkerIsolation)
		// Stop any native processes in the Manager that belong to elastic-mode
		// apps. Elastic workers are ephemeral and must not be re-adopted; the
		// pool starts empty and clients trigger fresh spawns on next request.
		lifecycle.ReapElasticOrphans(store, mgr)
		// Remove ShinyHub-managed containers no live replica re-adopted.
		if sweeper, ok := rt.(lifecycle.ContainerSweeper); ok {
			lifecycle.SweepOrphanContainers(mgr, sweeper)
		}
		// Sweep orphan Fargate tasks across ALL registered tiers.
		sweepCtx, cancelSweep := context.WithTimeout(octx, 60*time.Second)
		for _, tierName := range cfg.Runtime.TierOrder() {
			tierRT := mgr.RuntimeForTier(tierName)
			if fts, ok := tierRT.(lifecycle.FargateTaskSweeper); ok {
				lifecycle.SweepOrphanFargateTasks(sweepCtx, mgr, fts)
			}
		}
		cancelSweep()

		var loops sync.WaitGroup
		if monitor != nil {
			loops.Add(1)
			go func() { defer loops.Done(); monitor.Run(octx, monitorInterval) }()
			slog.Info("worker-down monitor started", "timeout", workerTimeout, "interval", monitorInterval, "retention", workerRetention)
		}
		loops.Add(1)
		go func() { defer loops.Done(); watcher.Start(octx) }()
		// Database housekeeping (audit + schedule-run retention) runs on the owner
		// only, so HA standbys never prune concurrently. A no-op unless retention
		// is configured.
		loops.Add(1)
		go func() { defer loops.Done(); runMaintenance(octx, store, cfg.Maintenance) }()
		// Warm-restore: re-boot and re-freeze the apps that were hibernated before
		// this restart, so their next access is a warm resume instead of a cold
		// boot (a frozen process does not survive a service restart, so the warm
		// state is re-created from scratch). Background - the boots take time - and
		// owner-gated by this span; only when warm-wake is enabled.
		if cfg.Runtime.Snapshot.Enabled && cfg.Runtime.Snapshot.RestoreOnStartup {
			loops.Add(1)
			go func() { defer loops.Done(); watcher.RestoreWarm(octx) }()
		}
		if err := sched.Start(octx); err != nil {
			slog.Error("start scheduler", "err", err)
		} else {
			slog.Info("scheduler started")
		}
		if controller != nil {
			loops.Add(1)
			go func() { defer loops.Done(); controller.Run(octx) }()
			slog.Info("autoscale controller started",
				"scan_interval", cfg.Runtime.Autoscale.ScanInterval,
				"cooldown", cfg.Runtime.Autoscale.Cooldown,
				"default_target", cfg.Runtime.Autoscale.DefaultTarget)
		}

		<-octx.Done()
		slog.Info("releasing control-plane ownership loops", "epoch", epoch)
		sched.Stop() // restartable on the next acquisition
		loops.Wait() // watcher/monitor/autoscale fully stopped
	}

	scope := leader.NewOwnerScope(ownerWork)
	// Stop the owner scope on any early error return below. Idempotent (Stop is a
	// no-op when idle / already stopped), so it only matters on a return between
	// here and the ordered shutdown path.
	defer scope.Stop()
	elector := leader.New(store, leader.Config{
		InstanceID: cfg.Server.InstanceID,
		TTL:        cfg.Server.LeaseTTL,
		RenewEvery: cfg.Server.LeaseRenewEvery,
		OnAcquire:  scope.Acquire,
		OnLose:     scope.Lose,
		Logger:     slog.Default(),
	})
	// Wire the ownership predicate into the watcher's wake trigger so a standby
	// that wins BeginWake defers to the active's reconciler rather than trying
	// to deploy. The raw IsOwner (not ownerAndReady) is intentional: the wake
	// trigger only needs to know "am I the active process-owner right now"; the
	// ownerAndReady gate is for API mutations that need a fresh worker index.
	watcher.SetIsOwner(elector.IsOwner)

	// Gate mutations on owner AND ready: the elector reports IsOwner true before
	// ownerWork refreshes the registry, so admitting owner-only mutations (main-API
	// deploy/placement, worker register/heartbeat) must wait for the fresh index.
	ownerAndReady := ownerAndReadyPredicate(elector.IsOwner, &ownerReady)
	srv.SetOwnership(ownerAndReady)
	if workerAPI != nil {
		workerAPI.SetOwnership(ownerAndReady)
	}
	electorCtx, cancelElector := context.WithCancel(context.Background())
	defer cancelElector()
	go elector.Run(electorCtx)

	mux := http.NewServeMux()
	// Observe wraps the timeout handler (not the inner router) so server metrics
	// and traces record the status/latency the client actually sees, including
	// timeout 503s and recovered-panic 500s. It is a no-op unless metrics or
	// tracing are enabled.
	mux.Handle("/api/", bodyLimitHandler(srv.Observe(apiTimeoutHandler(srv.Router()))))
	// Re-resolve JWT-claimed users against the live DB on every /app/* hit
	// so role demotions and account deletions take effect immediately.
	// Without this an admin's still-valid JWT keeps the admin-bypass path
	// open through the access middleware until the token expires — the same
	// staleness bug the API middleware already guards against via its own
	// userLookup wiring (see internal/api/router.go).
	appUserLookup := func(id int64) (*auth.ContextUser, error) {
		u, err := store.GetUserByID(id)
		if err != nil {
			return nil, err
		}
		return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role}, nil
	}
	emptyState := access.NeverDeployedMiddleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup, cfg.TrustedProxyNets)(prx)
	appHandler := access.Middleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup)(emptyState)
	mux.Handle("/app/", appHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", readyzHandler(prx, readyCh, store))
	mux.HandleFunc("/activez", activezHandler(ownerAndReady))
	mux.Handle("/static/", ui.Handler())

	// GET /internal/fargate-bundle/{digest} - streams the bundle zip to a Fargate
	// task that presents a short-lived HMAC capability token as a bearer credential.
	// Mounted directly on the main mux (not under /api/) so large bundle streams
	// are not subject to apiTimeoutHandler's 30-second cap.
	fargateBundleHandler := api.NewFargateBundleHandler(store, cfg.Storage.AppsDir, bundleTokenKey)
	internalMux := chi.NewRouter()
	internalMux.Get("/fargate-bundle/{digest}", fargateBundleHandler.Handle)
	mux.Handle("/internal/", http.StripPrefix("/internal", internalMux))

	registerBrandingRoutes(mux, cfg, srv, store, appUserLookup)

	var rootHandler http.Handler = mux
	if cfg.Auth.ForwardAuth.Enabled {
		faCfg := auth.ForwardAuthConfig{
			Enabled:             true,
			UserHeader:          cfg.Auth.ForwardAuth.UserHeader,
			EmailHeader:         cfg.Auth.ForwardAuth.EmailHeader,
			NameHeader:          cfg.Auth.ForwardAuth.NameHeader,
			GroupsHeader:        cfg.Auth.ForwardAuth.GroupsHeader,
			DefaultRole:         cfg.Auth.ForwardAuth.DefaultRole,
			GroupRoleMappings:   api.AuthMappings(cfg.Auth.GroupRoleMappings),
			RequireGroupsHeader: cfg.Auth.ForwardAuth.RequireGroupsHeader,
		}
		rootHandler = auth.ForwardAuthMiddleware(store, faCfg, cfg.TrustedProxyNets)(mux)
	}

	// Branding injects inline <script>/<style> into the SPA shell only when
	// active; the strict CSP allows exactly those blocks by SHA-256 hash (both
	// slices stay empty when branding is off, so no inline is served). Computed
	// from the same resolved branding the renderer uses so they cannot drift.
	var cspScriptSrc, cspStyleSrc []string
	if cfg.Branding.IsActive() {
		brandPub := ui.PublicBranding(cfg.Branding, cfg.Branding.ResolvedAssets())
		s, st, cspErr := ui.CSPInlineSources(brandPub)
		if cspErr != nil {
			return fmt.Errorf("compute branding CSP sources: %w", cspErr)
		}
		cspScriptSrc, cspStyleSrc = s, st
	} else {
		// Branding off: the raw shell is served, but its static inline
		// scripts (the pre-paint theme bootstrap) still need CSP hashes.
		s, cspErr := ui.StaticShellInlineScriptSources()
		if cspErr != nil {
			return fmt.Errorf("compute shell CSP sources: %w", cspErr)
		}
		cspScriptSrc = s
	}

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	// ReadTimeout/WriteTimeout bound slow-body and slow-read clients on ordinary
	// requests so they cannot pin a connection (and its goroutine) indefinitely.
	// Streaming and long-running routes (the reverse proxy, SSE log tails, large
	// uploads, lifecycle swaps) clear these per-connection deadlines via
	// deadlineExemptions so they are not severed mid-flight.
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           api.SecurityHeaders(cfg.TrustedProxyNets, cspScriptSrc, cspStyleSrc, deadlineExemptions(rootHandler)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := upg.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	// Startup pool adoption (single-node only): populate the proxy pool from
	// routable DB replica rows before the listener starts serving, so a ZDT
	// successor process can route requests to already-running app processes
	// immediately. Without this the pool is empty until RecoverProcesses runs
	// inside ownerWork (~10s after boot), causing 10-18s of loading-page
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
		startupSyncer.RunOnce(context.Background())
		slog.Info("startup pool adoption complete (single-node)")
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "version", version, "addr", addr)
		close(readyCh)
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	// Signal readiness: closes inherited-but-unused fds, writes the PID file, and
	// (when this is an upgrade child) tells the old process it may begin draining.
	// On error, close the servers we just started so we do not leak listeners and
	// goroutines, then return.
	if err := upg.Ready(); err != nil {
		_ = httpSrv.Close()
		if metricsSrv != nil {
			_ = metricsSrv.Close()
		}
		cancelElector()
		scope.Stop()
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
		cancelElector()
		scope.Stop()
		return fmt.Errorf("sd_notify: %w", err)
	}

	select {
	case err := <-serveErr:
		if err != nil {
			cancelElector()
			scope.Stop()
			return fmt.Errorf("http server: %w", err)
		}
	case <-upg.Exit():
		slog.Info("shutdown/handoff initiated, draining")
	}
	// Mark unready for both the signal and clean self-stop paths.
	prx.SetDraining(true)

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http shutdown", "err", err)
	}
	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("metrics shutdown", "err", err)
		}
	}
	if tracer != nil {
		if err := tracer.Shutdown(shutdownCtx); err != nil {
			slog.Warn("tracer shutdown", "err", err)
		}
	}
	// Stop the owner span: the Elector releases the lease, then OwnerScope
	// cancels the loop context and waits for the watcher/scheduler/monitor/
	// autoscaler to exit before we drain jobs and close the store.
	cancelElector()
	scope.Stop()
	// Stop the session reporter (clustered only). Cancel triggers a final
	// flush so the last known counts are persisted before the store closes.
	if reporterCancel != nil {
		reporterCancel()
		reporterWG.Wait()
	}
	// Stop the pool syncer (clustered only). Nil out the on-miss hook after
	// the syncer goroutine exits so a late-arriving request cannot invoke
	// SyncSlug with an already-cancelled context.
	if syncerCancel != nil {
		syncerCancel()
		syncerWG.Wait()
		prx.SetOnMissSync(nil)
	}
	// Stop the metrics-history collector. It only reads in-memory manager/proxy
	// state, so ordering relative to the store close is not load-bearing.
	if historyCancel != nil {
		historyCancel()
		historyWG.Wait()
	}
	// Drain in-flight scheduled job runs before the store closes.
	jobsMgr.Stop(shutdownCtx)

	// Drain live WebSocket (hijacked) app sessions: http.Server.Shutdown does
	// not wait for hijacked connections, so wait here up to drain_timeout, then
	// force-close stragglers. App backends (separate processes) remain alive
	// during this window, so sessions keep flowing until drained or force-closed.
	if n := prx.ActiveUpgradedConns(); n > 0 {
		slog.Info("draining upgraded connections", "count", n, "timeout", cfg.Server.DrainTimeout)
		if forced := prx.DrainUpgraded(cfg.Server.DrainTimeout); forced > 0 {
			slog.Warn("drain timeout reached, force-closed upgraded connections", "count", forced)
		} else {
			slog.Info("upgraded connections drained cleanly")
		}
	}

	switch cfg.Server.ShutdownApps {
	case "stop":
		slog.Info("stopping app processes (server.shutdown_apps=stop)")
		if err := mgr.StopAll(); err != nil {
			slog.Warn("stop app processes", "err", err)
		}
	default: // "adopt"
		slog.Info("leaving app processes running for re-adoption (server.shutdown_apps=adopt)")
	}

	slog.Info("shutdown complete")
	return nil
}

// registerBrandingRoutes wires all branding-aware frontend routes onto mux.
// It is extracted from runServe so the integration test can call the identical
// production registration logic without duplicating the wiring.
//
// Routes registered:
//   - /apps/, /users, /audit-log, /login  - SPA shell (branded or stock)
//   - /branding/                          - operator asset files (only when assets present)
//   - /.shinyhub/branding.json            - public branding metadata
//   - /.shinyhub/apps.json                - app list (optional auth)
//   - /                                   - landing page or SPA shell
func registerBrandingRoutes(mux *http.ServeMux, cfg *config.Config, srv *api.Server, store *db.Store, appUserLookup auth.UserLookup) {
	brandingActive := cfg.Branding.IsActive()
	resolved := cfg.Branding.ResolvedAssets()

	// serveShell serves the stock SPA shell: byte-for-byte the existing
	// ServeFileFS path when branding is inactive (preserving Last-Modified /
	// ETag / Range / conditional-GET and SHINYHUB_DEV_STATIC live reload),
	// or the branded render when active (re-reading index.html per request so
	// dev live-reload still works).
	pub := ui.PublicBranding(cfg.Branding, resolved)
	serveShell := func(w http.ResponseWriter, r *http.Request) {
		// An authenticated request (the request that fetches the shell is itself
		// behind forward auth or a session) gets the shell pre-marked "in" so the
		// dashboard chrome paints immediately, with no boot splash and no flash of
		// the login form. This needs a per-request render, so it leaves the cached
		// ServeFileFS fast path only when there is a reason to.
		authed := auth.UserFromContext(r.Context()) != nil
		if !brandingActive && !authed {
			http.ServeFileFS(w, r, ui.Static(), "index.html")
			return
		}
		raw, err := fs.ReadFile(ui.Static(), "index.html")
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(out)
	}

	// SPA routes: /apps/<slug>..., /users, /audit-log, /login. The handler
	// 404s anything outside the IsUIPath allowlist, so legitimate unknowns
	// still return 404 rather than rendering the SPA shell.
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ui.IsUIPath(r.URL.Path) {
			http.NotFound(w, r)
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
	mux.Handle("/apps/", spa) // prefix: /apps/<slug> and /apps/<slug>/<tab>

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
			http.NotFound(w, r)
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
//   - POST /api/apps/{slug}/deploy — bundle upload, body can be
//     hundreds of MB.
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
	case "deploy", "restart", "stop":
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
// size limit downstream: POST /api/apps/{slug}/deploy (bundle zip) and
// PUT /api/apps/{slug}/data/<rel> (per-app data file).
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
	if sub == "deploy" {
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
//   - /internal/fargate-bundle/* — streams a deployment bundle (large body).
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
