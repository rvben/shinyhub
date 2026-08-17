package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	deploypkg "github.com/rvben/shinyhub/internal/deploy"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/spf13/cobra"
)

type planFlags struct {
	deployFlags
	detailedExitcode bool
	failOnChanges    bool
	out              string
	force            bool
	expiresIn        time.Duration
	details          bool
}

// deploymentSource is shared by plan and deploy so repository selection,
// directory safety, slug derivation, and visibility normalization cannot drift.
type deploymentSource struct {
	Dir        string
	Slug       string
	Label      string
	Git        string
	Branch     string
	Subdir     string
	Visibility string
	cleanup    func()
}

type deploymentLaunchPreview struct {
	Runtime               string   `json:"runtime"`
	Command               []string `json:"command"`
	CommandScope          string   `json:"command_scope"`
	DependencyPreparation []string `json:"dependency_preparation"`
	ReadinessPath         string   `json:"readiness_path"`
	ReadinessStatus       string   `json:"readiness_status"`
	StartupTimeoutSeconds int      `json:"startup_timeout_seconds"`
}

type deploymentRemotePreview struct {
	Exists               bool   `json:"exists"`
	Status               string `json:"status,omitempty"`
	Access               string `json:"access,omitempty"`
	DeployCount          int    `json:"deploy_count,omitempty"`
	ContentDigest        string `json:"content_digest,omitempty"`
	LastDeploymentStatus string `json:"last_deployment_status,omitempty"`
	ManagedBy            string `json:"managed_by,omitempty"`
	RedeployInFlight     bool   `json:"redeploy_in_flight,omitempty"`
	ResourceRevision     string `json:"resource_revision,omitempty"`
}

type deploymentManifestPreview struct {
	Present bool     `json:"present"`
	Effects []string `json:"effects"`
}

type deploymentPlan struct {
	Status        string                    `json:"status"`
	Action        string                    `json:"action"`
	ChangeStatus  string                    `json:"change_status"`
	Changes       *bool                     `json:"changes"`
	Host          string                    `json:"host"`
	AppURL        string                    `json:"app_url"`
	Slug          string                    `json:"slug"`
	Source        string                    `json:"source"`
	Permission    string                    `json:"permission"`
	Visibility    string                    `json:"visibility"`
	Lifecycle     string                    `json:"lifecycle"`
	Remote        deploymentRemotePreview   `json:"remote"`
	Bundle        *bundlePreview            `json:"bundle"`
	Launch        deploymentLaunchPreview   `json:"launch"`
	Manifest      deploymentManifestPreview `json:"manifest"`
	Warnings      []string                  `json:"warnings"`
	DeployCommand string                    `json:"deploy_command"`
	ExitCode      int                       `json:"exit_code"`
	Start         bool                      `json:"start"`
	SavedPlan     *savedPlanSummary         `json:"saved_plan,omitempty"`
	Plan          planDocument              `json:"plan"`
}

type savedPlanSummary struct {
	Path      string    `json:"path"`
	PlanID    string    `json:"plan_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Integrity string    `json:"integrity"`
}

// prepareDeployment is the shared, side-effect-free preparation used by both
// plan and deploy. It builds the one upload archive and resolves the production
// launch contract from the same source tree, so a successful plan cannot be
// followed by a differently prepared deploy in the same CLI version.
func prepareDeployment(dir string) (*bundlePreview, *deploypkg.LaunchPlan, error) {
	bundle, err := buildBundlePreview(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("bundle: %w", err)
	}
	launch, err := deploypkg.ResolveLaunch(dir, deploypkg.LaunchOptions{
		Port: 4000, BindHost: "127.0.0.1", PrepHostDeps: true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("launch contract: %w", err)
	}
	return bundle, launch, nil
}

func newPlanCmd() *cobra.Command {
	f := &planFlags{}
	cmd := &cobra.Command{
		Use:   "plan [dir]",
		Short: "Preview one app deployment (read-only, no changes)",
		Long: `Build the exact bundle that deploy would upload, validate its launch
contract and shinyhub.toml, then compare its content digest with the selected
remote app. Plan makes only GET requests and never creates, updates, starts, or
deploys an app.

Pass --out to save the exact bundle and reviewed plan locally with mode 0600.
Saving a plan still makes no remote changes; apply it later with 'shinyhub apply'.

Pass '.' explicitly to plan the current directory. Source-selection flags are
the same as deploy, so the printed deploy command is directly reusable.

Exit codes:
  0  plan printed (default), or content is unchanged (--detailed-exitcode)
  1  local validation or protocol compatibility error
  2  --detailed-exitcode / --fail-on-changes only: new, changed, or unknown content
  3  network, authentication, or authorization error
  6  server not ready

Examples:
  shinyhub plan .
  shinyhub plan . --detailed-exitcode --output json
  shinyhub plan --git https://github.com/acme/sales --branch main --subdir app`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPlan(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.slug, "slug", "", "App slug; defaults to the directory or repository name")
	cmd.Flags().StringVar(&f.git, "git", "", "Git repository URL to clone and plan")
	cmd.Flags().StringVar(&f.branch, "branch", "", "Branch or tag to plan (default: repo default)")
	cmd.Flags().StringVar(&f.subdir, "subdir", "", "Subdirectory within repo containing the app")
	cmd.Flags().StringVar(&f.visibility, "visibility", "", "Visibility for a new app: private, shared, or public (default: server config)")
	cmd.Flags().BoolVar(&f.start, "start", false, "Show the effect of starting a previously stopped app after deploy")
	cmd.Flags().DurationVar(&f.waitForServer, "wait-for-server", 0, "Poll /api/server-info until the server is ready (e.g. 2m)")
	cmd.Flags().BoolVar(&f.detailedExitcode, "detailed-exitcode", false, "Exit 2 when content is new, changed, or cannot be compared")
	cmd.Flags().BoolVar(&f.failOnChanges, "fail-on-changes", false, "Alias for --detailed-exitcode (CI gate)")
	cmd.Flags().StringVar(&f.out, "out", "", "Save the exact plan and bundle to this path")
	cmd.Flags().BoolVar(&f.force, "force", false, "Replace an existing --out plan file atomically")
	cmd.Flags().DurationVar(&f.expiresIn, "expires-in", defaultPlanLifetime, "Saved plan lifetime (for example 30m or 24h)")
	cmd.Flags().BoolVar(&f.details, "details", false, "Show full bundle, launch, manifest, and saved-plan details")
	cmd.AddCommand(newPlanShowCmd())
	return cmd
}

func newPlanShowCmd() *cobra.Command {
	var details, files bool
	cmd := &cobra.Command{
		Use:   "show PLAN",
		Short: "Inspect and verify a saved plan offline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, err := readSavedPlan(args[0], time.Now(), true)
			if err != nil {
				return &ExitCodeError{Code: 1, Kind: KindValidation, Err: err}
			}
			format, err := resolveFormat(false, false)
			if err != nil {
				return err
			}
			if format == formatJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(loaded.Envelope)
			}
			plan := loaded.Envelope.Plan
			plan.SavedPlan = &savedPlanSummary{Path: args[0], PlanID: loaded.Envelope.PlanID, ExpiresAt: loaded.Envelope.ExpiresAt, Integrity: loaded.Envelope.Integrity}
			renderDeploymentPlanWith(cmd.OutOrStdout(), plan, planRenderOptions{
				Width: planOutputWidth(cmd.OutOrStdout()), Details: details || files,
			})
			if !time.Now().Before(loaded.Envelope.ExpiresAt) {
				fmt.Fprintf(cmd.OutOrStdout(), "\nExpired: %s. Re-run `shinyhub plan` before applying.\n", loaded.Envelope.ExpiresAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&details, "details", false, "Show full bundle, launch, manifest, and saved-plan details")
	cmd.Flags().BoolVar(&files, "files", false, "Show bundle files (also enables the complete detail view)")
	return cmd
}

func resolveDeploymentSource(args []string, f *deployFlags) (*deploymentSource, error) {
	source := &deploymentSource{cleanup: func() {}}
	if f.git != "" {
		if len(args) > 0 {
			return nil, fmt.Errorf("directory argument and --git cannot be used together; choose one source")
		}
		cloned, err := gitClone(f.git, f.branch, f.subdir)
		if err != nil {
			return nil, fmt.Errorf("git clone: %w", err)
		}
		root := cloned
		cleanSubdir := filepath.Clean(f.subdir)
		if f.subdir != "" && cleanSubdir != "." {
			for i := 0; i < len(strings.Split(cleanSubdir, string(filepath.Separator))); i++ {
				root = filepath.Dir(root)
			}
		}
		source.cleanup = func() { _ = os.RemoveAll(root) }
		source.Dir = cloned
		source.Git = f.git
		source.Branch = f.branch
		source.Subdir = f.subdir
		source.Label = f.git
		if f.branch != "" {
			source.Label += "@" + f.branch
		}
		if f.subdir != "" {
			source.Label += "#" + filepath.ToSlash(f.subdir)
		}
	} else {
		if f.branch != "" || f.subdir != "" {
			return nil, fmt.Errorf("--branch and --subdir require --git")
		}
		if len(args) == 0 {
			return nil, fmt.Errorf("missing directory argument: pass `.` to use the current directory or a path like `./app`")
		}
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return nil, err
		}
		source.Dir = abs
		source.Label = abs
	}
	info, err := os.Stat(source.Dir)
	if err != nil {
		source.cleanup()
		return nil, fmt.Errorf("source %s: %w", source.Label, err)
	}
	if !info.IsDir() {
		source.cleanup()
		return nil, fmt.Errorf("source %s is not a directory", source.Label)
	}

	if f.slug != "" {
		source.Slug = f.slug
	} else if f.git != "" {
		source.Slug = sanitizeSlug(strings.TrimSuffix(filepath.Base(f.git), ".git"))
	} else {
		source.Slug = sanitizeSlug(filepath.Base(source.Dir))
	}
	if !slugpkg.Valid(source.Slug) {
		source.cleanup()
		if f.slug == "" {
			return nil, fmt.Errorf("could not derive a valid slug from %q (got %q): pass --slug explicitly. Slug rule: %s",
				filepath.Base(source.Dir), source.Slug, slugpkg.HumanRule)
		}
		return nil, fmt.Errorf("invalid slug %q: must be %s", source.Slug, slugpkg.HumanRule)
	}
	visibility, err := resolveVisibilityFlag(f.visibility)
	if err != nil {
		source.cleanup()
		return nil, err
	}
	source.Visibility = visibility
	return source, nil
}

func runPlan(cmd *cobra.Command, args []string, f *planFlags) error {
	if f.force && f.out == "" {
		return fmt.Errorf("--force requires --out")
	}
	if cmd.Flags().Changed("expires-in") && f.out == "" {
		return fmt.Errorf("--expires-in requires --out")
	}
	if f.failOnChanges {
		f.detailedExitcode = true
	}
	format, err := resolveFormat(false, false)
	if err != nil {
		return err
	}
	source, err := resolveDeploymentSource(args, &f.deployFlags)
	if err != nil {
		return err
	}
	defer source.cleanup()

	bundle, launch, err := prepareDeployment(source.Dir)
	if err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	var info serverInfo
	if f.waitForServer > 0 {
		info, err = waitForServerReady(cfg, f.waitForServer, serverPollInterval, cmd.ErrOrStderr(), time.Now, time.Sleep)
		if err != nil {
			return &ExitCodeError{Code: 6, Err: err}
		}
	} else {
		info, err = probeServer(cfg)
		if err != nil {
			var notReady *serverNotReadyError
			if !errors.As(err, &notReady) || notReady.status != http.StatusNotFound {
				if errors.As(err, &notReady) {
					return &ExitCodeError{Code: 6, Err: err}
				}
				return err
			}
		}
	}
	warnings := []string{}
	if info.looksLikeShinyhub() {
		diagnosis := diagnoseCompatibility(version, info)
		if diagnosis.Level == compatibilityIncompatible {
			return compatibilityError(diagnosis)
		}
		if diagnosis.Level == compatibilityWarning {
			warnings = append(warnings, diagnosis.Detail+". "+diagnosis.Fix)
		}
	} else {
		warnings = append(warnings, "server capability metadata is unavailable; live content comparison may be limited")
	}

	remote, permission, err := fetchDeploymentTarget(cfg, source.Slug)
	if err != nil {
		return err
	}
	if remote.Exists && source.Visibility != "" {
		warnings = append(warnings, fmt.Sprintf("--visibility is ignored for existing apps; use `shinyhub apps access set %s %s`", source.Slug, source.Visibility))
	}
	if remote.ManagedBy != "" {
		warnings = append(warnings, fmt.Sprintf("this app is managed by %s; a later fleet apply may overwrite manifest-managed settings", remote.ManagedBy))
	}
	if remote.RedeployInFlight {
		warnings = append(warnings, "a replica redeploy is already in flight; wait for it to finish before deploying")
	}
	if remote.LastDeploymentStatus == "failed" {
		warnings = append(warnings, fmt.Sprintf("the most recent deployment failed; inspect it with `shinyhub apps logs %s` before retrying", source.Slug))
	}
	if launch.AppType != "" && info.Runtimes != nil && !info.Runtimes[launch.AppType] {
		warnings = append(warnings, fmt.Sprintf("the server reports no %s runtime; deployment may require a container runtime or administrator action", launch.AppType))
	}

	plan := assembleDeploymentPlan(cfg, source, bundle, launch, remote, permission, f.start, warnings, f.detailedExitcode)
	if f.out != "" {
		if !info.Capabilities.PlanApply {
			return fmt.Errorf("server %s does not support atomic saved-plan apply; upgrade it before using --out", cfg.Host)
		}
		if remote.Exists && remote.ResourceRevision == "" {
			return fmt.Errorf("server %s did not return a resource revision for %s; upgrade it before using --out", cfg.Host, source.Slug)
		}
		applyCommand := "shinyhub apply " + shellQuote(f.out)
		plan.Plan.Command = applyCommand
		plan.Plan.NextActions = []planNextAction{{Command: applyCommand, Description: "Apply the exact reviewed bundle", RequiresConfirmation: true}}
		envelope, err := buildSavedPlan(plan, bundle.Buffer.Bytes(), info.ProtocolVersion, f.expiresIn, time.Now())
		if err != nil {
			return err
		}
		if err := writeSavedPlan(f.out, envelope, bundle.Buffer.Bytes(), f.force); err != nil {
			return err
		}
		plan.SavedPlan = &savedPlanSummary{Path: f.out, PlanID: envelope.PlanID, ExpiresAt: envelope.ExpiresAt, Integrity: envelope.Integrity}
	}
	if format == formatJSON {
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(plan); err != nil {
			return err
		}
	} else {
		renderDeploymentPlanWith(cmd.OutOrStdout(), plan, planRenderOptions{
			Width: planOutputWidth(cmd.OutOrStdout()), Details: f.details,
		})
	}
	if plan.ExitCode == 2 {
		return &ExitCodeError{Code: 2, Err: errors.New("changes are pending"), Reported: true}
	}
	return nil
}

func fetchDeploymentTarget(cfg *cliConfig, slug string) (deploymentRemotePreview, string, error) {
	remote := deploymentRemotePreview{}
	req, err := http.NewRequest(http.MethodGet, cfg.Host+"/api/apps/"+url.PathEscape(slug), nil)
	if err != nil {
		return remote, "", err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return remote, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		var envelope struct {
			App              db.App `json:"app"`
			CanManage        *bool  `json:"can_manage"`
			RedeployInFlight bool   `json:"redeploy_in_flight"`
			ResourceRevision string `json:"resource_revision"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return remote, "", protocolFailure(cfg, &protocolError{op: "decode app", err: err})
		}
		if envelope.CanManage != nil && !*envelope.CanManage {
			return remote, "", &httpStatusError{Status: http.StatusForbidden,
				msg: fmt.Sprintf("may view app %q but cannot deploy it; ask the owner or an administrator for manager access", slug)}
		}
		permission := "manage existing app"
		if envelope.CanManage == nil {
			permission = "not reported by this server; deploy will verify"
		}
		managedBy := ""
		if envelope.App.ManagedBy != nil {
			managedBy = *envelope.App.ManagedBy
		}
		resourceRevision := envelope.ResourceRevision
		if resourceRevision == "" {
			resourceRevision = resp.Header.Get("X-Shinyhub-Resource-Revision")
		}
		return deploymentRemotePreview{
			Exists: true, Status: envelope.App.Status, Access: envelope.App.Access,
			DeployCount: envelope.App.DeployCount, ContentDigest: envelope.App.ContentDigest,
			LastDeploymentStatus: envelope.App.LastDeploymentStatus,
			ManagedBy:            managedBy, RedeployInFlight: envelope.RedeployInFlight,
			ResourceRevision: resourceRevision,
		}, permission, nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return remote, "", httpError(cfg.Token, "inspect app "+slug, resp, body)
	}
	identity, err := fetchRemoteIdentity(cfg.Host, cfg.Token)
	if err != nil {
		return remote, "", err
	}
	if identity.CanCreateAppsKnown && !identity.CanCreateApps {
		return remote, "", &httpStatusError{Status: http.StatusForbidden,
			msg: fmt.Sprintf("app %q is new and this identity cannot create apps; ask an administrator for the developer role", slug)}
	}
	if !identity.CanCreateAppsKnown {
		switch identity.Role {
		case "admin", "operator", "developer":
			return remote, "create permission inferred from legacy role; deploy will verify", nil
		default:
			return remote, "", &httpStatusError{Status: http.StatusForbidden,
				msg: fmt.Sprintf("app %q is new and this server does not report create permission for role %q", slug, identity.Role)}
		}
	}
	return remote, "create new app", nil
}

func assembleDeploymentPlan(cfg *cliConfig, source *deploymentSource, bundle *bundlePreview, launch *deploypkg.LaunchPlan, remote deploymentRemotePreview, permission string, start bool, warnings []string, detailed bool) deploymentPlan {
	manifest := deploymentManifestPreview{Present: launch.Manifest != nil, Effects: []string{}}
	if launch.Manifest != nil {
		manifest.Effects = summarizeManifest(launch.Manifest)
		if len(manifest.Effects) == 0 {
			manifest.Effects = []string{"manifest is valid; no persistent app, hook, schedule, or access overrides"}
		}
	}
	deps := make([]string, 0, len(launch.DepPrep))
	for _, step := range launch.DepPrep {
		deps = append(deps, step.Label)
	}
	runtime := launch.AppType
	command := append([]string{}, launch.Command...)
	if runtime == "" {
		runtime = "custom"
		if launch.Manifest != nil && len(launch.Manifest.App.Command) > 0 {
			command = append([]string{}, launch.Manifest.App.Command...)
		}
	} else {
		for i, arg := range command {
			arg = strings.ReplaceAll(arg, "127.0.0.1", "{host}")
			if arg == "4000" {
				arg = "{port}"
			}
			arg = strings.ReplaceAll(arg, "port=4000", "port={port}")
			arg = strings.ReplaceAll(arg, ":4000", ":{port}")
			command[i] = arg
		}
	}
	readinessStatus := "any 2xx or 3xx"
	if launch.ReadyStatus != 0 {
		readinessStatus = fmt.Sprintf("%d", launch.ReadyStatus)
	}

	plan := deploymentPlan{
		Status: "planned", Host: cfg.Host, AppURL: cfg.Host + "/app/" + source.Slug + "/",
		Slug: source.Slug, Source: source.Label, Permission: permission, Remote: remote,
		Bundle: bundle, Manifest: manifest, Warnings: warnings, Start: start,
		Launch: deploymentLaunchPreview{
			Runtime: runtime, Command: command,
			CommandScope:          "bundle-resolved base command; server runtime and tracing policy may wrap it",
			DependencyPreparation: deps,
			ReadinessPath:         launch.ReadyPath, ReadinessStatus: readinessStatus,
			StartupTimeoutSeconds: int(launch.Timeout.Seconds()),
		},
	}
	plan.DeployCommand = deploymentCommand(source, start, !remote.Exists)
	if remote.Exists {
		plan.Visibility = remote.Access
		if plan.Visibility == "" {
			plan.Visibility = "not reported"
		}
		if remote.Status == "stopped" && remote.DeployCount > 0 && !start {
			plan.Lifecycle = "deploy new version; keep app stopped"
		} else if remote.Status == "stopped" && remote.DeployCount > 0 {
			plan.Lifecycle = "deploy new version and start app"
		} else if remote.Status == "failed" || remote.Status == "crashed" {
			plan.Lifecycle = "deploy new version and attempt to recover the app"
		} else {
			plan.Lifecycle = "deploy new version and replace running replicas"
		}
		switch {
		case remote.ContentDigest == "":
			plan.Action, plan.ChangeStatus, plan.Changes = "update", "unknown", nil
		case remote.ContentDigest == bundle.Digest:
			unchanged := false
			plan.Action, plan.ChangeStatus, plan.Changes = "redeploy", "unchanged", &unchanged
		default:
			changed := true
			plan.Action, plan.ChangeStatus, plan.Changes = "update", "changed", &changed
		}
	} else {
		changed := true
		plan.Action, plan.ChangeStatus, plan.Changes = "create", "new", &changed
		plan.Lifecycle = "create app, deploy first version, and start it"
		if source.Visibility == "" {
			plan.Visibility = "server default"
		} else {
			plan.Visibility = source.Visibility
		}
	}
	if detailed && plan.ChangeStatus != "unchanged" {
		plan.ExitCode = 2
	}
	plan.Plan = deploymentPlanDocument(plan)
	return plan
}

func renderDeploymentPlan(out io.Writer, plan deploymentPlan) {
	renderDeploymentPlanWith(out, plan, planRenderOptions{Width: planOutputWidth(out)})
}

func deploymentCommand(source *deploymentSource, start, includeVisibility bool) string {
	parts := []string{"shinyhub", "deploy"}
	if source.Git != "" {
		parts = append(parts, "--git", shellQuote(source.Git))
		if source.Branch != "" {
			parts = append(parts, "--branch", shellQuote(source.Branch))
		}
		if source.Subdir != "" {
			parts = append(parts, "--subdir", shellQuote(source.Subdir))
		}
	} else {
		parts = append(parts, shellQuote(source.Label))
	}
	parts = append(parts, "--slug", source.Slug)
	if includeVisibility && source.Visibility != "" {
		parts = append(parts, "--visibility", source.Visibility)
	}
	if start {
		parts = append(parts, "--start")
	}
	parts = append(parts, "--wait")
	return strings.Join(parts, " ")
}
