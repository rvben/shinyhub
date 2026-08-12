package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rvben/shinyhub/internal/deploy"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/spf13/cobra"
)

type doctorFlags struct {
	localOnly  bool
	remoteOnly bool
	slug       string
}

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`

	kind Kind `json:"-"`
	code int  `json:"-"`
}

type doctorSummary struct {
	Passed  int `json:"passed"`
	Warned  int `json:"warned"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

type doctorReport struct {
	Status    string        `json:"status"`
	Scope     string        `json:"scope"`
	AppDir    string        `json:"app_dir,omitempty"`
	Host      string        `json:"host,omitempty"`
	Slug      string        `json:"slug,omitempty"`
	Checks    []doctorCheck `json:"checks"`
	Summary   doctorSummary `json:"summary"`
	NextSteps []string      `json:"next_steps"`
}

type doctorLocalContext struct {
	dir     string
	slug    string
	appType string
	plan    *deploy.LaunchPlan
}

var doctorLookPath = exec.LookPath

func newDoctorCmd() *cobra.Command {
	f := &doctorFlags{}
	cmd := &cobra.Command{
		Use:   "doctor [dir]",
		Short: "Check whether an app and remote are ready to deploy",
		Long: `Doctor checks the complete path from a local app bundle to the selected
remote ShinyHub and lists every actionable problem in one run.

By default it validates the app directory and manifest, resolves the local
launch command, checks the required local executable, verifies the selected
server and credential, confirms the signed-in identity can deploy the target,
and matches the app runtime to what the server offers.

Use --local before connecting to check only local-run readiness. Use --remote
to check only the selected server; add --slug to verify access to an existing
deployment target. Doctor never changes local or remote state and never prints
credentials.

Exit codes:
  0  ready (warnings may be present)
  1  local configuration or app problem
  3  authentication, authorization, or network problem
  6  host is reachable but ShinyHub is not ready`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd, args, f)
		},
	}
	cmd.Flags().BoolVar(&f.localOnly, "local", false, "Check only local app readiness; do not read credentials or contact a server")
	cmd.Flags().BoolVar(&f.remoteOnly, "remote", false, "Check only the selected remote; do not inspect an app directory")
	cmd.Flags().StringVar(&f.slug, "slug", "", "App slug whose deployment permission to verify (default: derived from the app directory)")
	return cmd
}

func runDoctor(cmd *cobra.Command, args []string, f *doctorFlags) error {
	format, err := resolveFormat(false, false)
	if err != nil {
		return err
	}
	if f.localOnly && f.remoteOnly {
		return validationErr("--local and --remote cannot be combined", "omit both to run the complete check")
	}
	if f.remoteOnly && len(args) > 0 {
		return validationErr("an app directory does not apply to --remote", "drop the directory, or omit --remote to check the complete deployment path")
	}
	if f.localOnly && f.slug != "" {
		return validationErr("--slug does not apply to --local", "drop --slug, or use --remote to check deployment access")
	}
	if f.localOnly {
		for _, name := range []string{"host", "config"} {
			if cmd.Flags().Changed(name) {
				return validationErr(fmt.Sprintf("--%s does not apply to --local", name), "drop the flag, or omit --local to check the selected remote too")
			}
		}
	}

	report := doctorReport{Status: "ready", Scope: "all"}
	if f.localOnly {
		report.Scope = "local"
	}
	if f.remoteOnly {
		report.Scope = "remote"
	}

	var local doctorLocalContext
	if !f.remoteOnly {
		dir := "."
		if len(args) == 1 {
			dir = args[0]
		}
		local, report.Checks = runLocalDoctor(dir, f.slug, report.Checks)
		report.AppDir = local.dir
		report.Slug = local.slug
	} else if f.slug != "" {
		if !slugpkg.Valid(f.slug) {
			return validationErr(fmt.Sprintf("invalid slug %q", f.slug), "use lowercase letters, digits, and single hyphens; no leading or trailing hyphen")
		}
		report.Slug = f.slug
	}

	if !f.localOnly {
		report.Checks, report.Host = runRemoteDoctor(report.Checks, report.Slug, local.appType)
	}

	report.Summary = tallyDoctorChecks(report.Checks)
	if report.Summary.Failed > 0 {
		report.Status = "not_ready"
	}
	report.NextSteps = doctorNextSteps(report)
	if err := renderDoctorReport(cmd.OutOrStdout(), format, report); err != nil {
		return err
	}
	if report.Status == "not_ready" {
		return doctorFailure(report.Checks)
	}
	return nil
}

func runLocalDoctor(rawDir, requestedSlug string, checks []doctorCheck) (doctorLocalContext, []doctorCheck) {
	ctx := doctorLocalContext{dir: rawDir}
	abs, err := filepath.Abs(rawDir)
	if err == nil {
		ctx.dir = abs
	}
	info, statErr := os.Stat(ctx.dir)
	if statErr != nil {
		checks = append(checks, doctorFail("app-directory", fmt.Sprintf("cannot read %s: %v", ctx.dir, statErr), "Pass the directory containing app.py, app.R, or shinyhub.toml.", KindValidation, 1))
		return ctx, appendLocalSkipped(checks, "the app directory is unavailable")
	}
	if !info.IsDir() {
		checks = append(checks, doctorFail("app-directory", fmt.Sprintf("%s is not a directory", ctx.dir), "Pass an app directory, not a file.", KindValidation, 1))
		return ctx, appendLocalSkipped(checks, "the app directory is unavailable")
	}
	checks = append(checks, doctorPass("app-directory", ctx.dir))

	ctx.slug = requestedSlug
	if ctx.slug == "" {
		ctx.slug = sanitizeSlug(filepath.Base(ctx.dir))
	}
	if !slugpkg.Valid(ctx.slug) {
		checks = append(checks, doctorFail("app-slug", fmt.Sprintf("%q is not a valid deployment slug", ctx.slug), "Pass --slug with lowercase letters, digits, and single hyphens.", KindValidation, 1))
	} else {
		checks = append(checks, doctorPass("app-slug", ctx.slug))
	}

	manifest, manifestErr := deploy.LoadManifest(ctx.dir)
	if manifestErr != nil {
		checks = append(checks, doctorFail("manifest", manifestErr.Error(), "Fix shinyhub.toml, then run `shinyhub manifest validate`.", KindValidation, 1))
		checks = append(checks,
			doctorSkip("entrypoint", "the manifest is invalid"),
			doctorSkip("local-runtime", "the launch command could not be resolved"))
		return ctx, checks
	}
	if manifest == nil {
		checks = append(checks, doctorPass("manifest", "no shinyhub.toml; defaults apply"))
	} else {
		checks = append(checks, doctorPass("manifest", "shinyhub.toml is valid"))
	}

	plan, launchErr := deploy.ResolveLaunch(ctx.dir, deploy.LaunchOptions{Port: 4000, BindHost: "127.0.0.1", PrepHostDeps: true})
	if launchErr != nil {
		checks = append(checks, doctorFail("entrypoint", launchErr.Error(), "Add app.py or app.R at the bundle root, or declare [app] command in shinyhub.toml.", KindValidation, 1))
		checks = append(checks, doctorSkip("local-runtime", "the launch command could not be resolved"))
		return ctx, checks
	}
	ctx.plan = plan
	ctx.appType = plan.AppType
	entryDetail := "custom command: " + shellSummary(plan.Command)
	if plan.AppType != "" {
		entryDetail = plan.AppType + " app: " + shellSummary(plan.Command)
	}
	checks = append(checks, doctorPass("entrypoint", entryDetail))

	executable := ""
	if len(plan.Command) > 0 {
		executable = plan.Command[0]
	}
	if executable == "" {
		checks = append(checks, doctorFail("local-runtime", "the launch command has no executable", "Set a non-empty [app] command.", KindValidation, 1))
		return ctx, checks
	}
	resolved, lookupErr := resolveDoctorExecutable(ctx.dir, executable)
	if lookupErr != nil {
		checks = append(checks, doctorFail("local-runtime", fmt.Sprintf("%s is not available", executable), runtimeInstallFix(executable), KindValidation, 1))
		return ctx, checks
	}
	checks = append(checks, doctorPass("local-runtime", fmt.Sprintf("%s available at %s", executable, resolved)))
	return ctx, checks
}

func appendLocalSkipped(checks []doctorCheck, reason string) []doctorCheck {
	return append(checks,
		doctorSkip("app-slug", reason),
		doctorSkip("manifest", reason),
		doctorSkip("entrypoint", reason),
		doctorSkip("local-runtime", reason))
}

func resolveDoctorExecutable(dir, executable string) (string, error) {
	if strings.ContainsRune(executable, filepath.Separator) {
		path := executable
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
			return "", errors.New("not executable")
		}
		return path, nil
	}
	return doctorLookPath(executable)
}

func runRemoteDoctor(checks []doctorCheck, slug, appType string) ([]doctorCheck, string) {
	cfg, cfgErr := loadConfig()
	if cfgErr != nil {
		kind, code := classify(cfgErr)
		checks = append(checks, doctorFail("credentials", cfgErr.Error(), doctorCredentialFix(cfgErr), kind, code))
		return appendRemoteSkipped(checks, "no usable remote credential"), ""
	}
	host := cfg.Host
	credentialDetail := "credential loaded from " + configPath()
	if os.Getenv("SHINYHUB_TOKEN") != "" {
		credentialDetail = "credential supplied by SHINYHUB_TOKEN"
	} else if info, err := os.Stat(configPath()); err == nil && info.Mode().Perm()&0077 != 0 {
		checks = append(checks, doctorFail("credentials", fmt.Sprintf("%s is readable by other users (mode %04o)", configPath(), info.Mode().Perm()), "Run `chmod 600 "+configPath()+"`.", KindValidation, 1))
	} else {
		checks = append(checks, doctorPass("credentials", credentialDetail))
	}
	if len(checks) == 0 || checks[len(checks)-1].Name != "credentials" {
		checks = append(checks, doctorPass("credentials", credentialDetail))
	}

	if insecureRemoteHost(host) {
		checks = append(checks, doctorWarn("transport-security", "the remote uses unencrypted HTTP", "Use HTTPS before sending production credentials or app bundles."))
	} else {
		checks = append(checks, doctorPass("transport-security", transportSecurityDetail(host)))
	}

	info, probeErr := probeServer(cfg)
	if probeErr != nil {
		kind, code := classify(probeErr)
		if nr := new(serverNotReadyError); errors.As(probeErr, &nr) {
			kind, code = KindServerNotReady, 6
		}
		checks = append(checks, doctorFail("server", probeErr.Error(), "Check the URL and server logs, then retry; use `--host <name|url>` to select another remote.", kind, code))
		return appendRemoteAfterServerSkipped(checks), host
	}
	checks = append(checks, doctorPass("server", fmt.Sprintf("ShinyHub %s is ready%s", displayVersion(info.Version), runtimeSummary(info.Runtimes))))
	compatibility := diagnoseCompatibility(version, info)
	switch compatibility.Level {
	case compatibilityIncompatible:
		checks = append(checks, doctorFail("version-compatibility", compatibility.Detail, compatibility.Fix, KindValidation, 1))
		return appendRemoteAfterCompatibilitySkipped(checks), host
	case compatibilityWarning:
		checks = append(checks, doctorWarn("version-compatibility", compatibility.Detail, compatibility.Fix))
	default:
		checks = append(checks, doctorPass("version-compatibility", compatibility.Detail))
	}

	identity, authErr := doctorIdentity(cfg)
	if authErr != nil {
		kind, code := classify(authErr)
		checks = append(checks, doctorFail("authentication", authErr.Error(), "Reconnect with `shinyhub connect "+host+"`.", kind, code))
		checks = append(checks,
			doctorSkip("deploy-permission", "authentication failed"),
			doctorSkip("remote-runtime", "authentication failed"))
		return checks, host
	}
	checks = append(checks, doctorPass("authentication", fmt.Sprintf("signed in as %s (%s)", identity.Username, identity.Role)))

	permission := doctorDeployPermission(cfg, identity.CanCreateApps, slug)
	checks = append(checks, permission)
	checks = append(checks, doctorRemoteRuntime(info.Runtimes, appType))
	return checks, host
}

type doctorIdentityResult struct {
	Username      string
	Role          string
	CanCreateApps bool
}

func doctorIdentity(cfg *cliConfig) (doctorIdentityResult, error) {
	var result doctorIdentityResult
	req, err := http.NewRequest(http.MethodGet, cfg.Host+"/api/auth/me", nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return result, httpError(cfg.Token, "doctor authentication", resp, body)
	}
	var payload struct {
		User struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
		CanCreateApps bool `json:"can_create_apps"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return result, fmt.Errorf("decode authentication response: %w", err)
	}
	if payload.User.Username == "" {
		return result, errors.New("authentication response did not identify a user")
	}
	return doctorIdentityResult{Username: payload.User.Username, Role: payload.User.Role, CanCreateApps: payload.CanCreateApps}, nil
}

func doctorDeployPermission(cfg *cliConfig, canCreate bool, slug string) doctorCheck {
	if slug == "" {
		if canCreate {
			return doctorPass("deploy-permission", "this identity may create apps; pass --slug to check an existing target")
		}
		return doctorFail("deploy-permission", "this identity cannot create apps", "Ask a ShinyHub administrator for the developer role, or pass --slug for an app you manage.", KindAuth, 3)
	}
	req, err := http.NewRequest(http.MethodGet, cfg.Host+"/api/apps/"+url.PathEscape(slug), nil)
	if err != nil {
		return doctorFail("deploy-permission", err.Error(), "Check the target slug.", KindValidation, 1)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		kind, code := classify(err)
		return doctorFail("deploy-permission", err.Error(), "Retry after the server is reachable.", kind, code)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		var payload struct {
			CanManage bool `json:"can_manage"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return doctorFail("deploy-permission", "the app response could not be decoded", "Upgrade the CLI or server so their API versions agree.", KindInternal, 1)
		}
		if payload.CanManage {
			return doctorPass("deploy-permission", fmt.Sprintf("may deploy updates to existing app %q", slug))
		}
		return doctorFail("deploy-permission", fmt.Sprintf("may view app %q but cannot manage it", slug), "Ask the app owner or an administrator for manager access.", KindAuth, 3)
	}
	if resp.StatusCode == http.StatusNotFound {
		if canCreate {
			return doctorPass("deploy-permission", fmt.Sprintf("app %q is new and this identity may create it", slug))
		}
		return doctorFail("deploy-permission", fmt.Sprintf("app %q does not exist and this identity cannot create it", slug), "Ask a ShinyHub administrator for the developer role or choose an app you manage.", KindAuth, 3)
	}
	err = httpError(cfg.Token, "check deploy permission", resp, body)
	kind, code := classify(err)
	return doctorFail("deploy-permission", err.Error(), "Check access to the target app, then retry.", kind, code)
}

func doctorRemoteRuntime(runtimes map[string]bool, appType string) doctorCheck {
	if appType == "" {
		available := availableRuntimes(runtimes)
		if len(available) == 0 {
			return doctorWarn("remote-runtime", "the server reports no built-in app runtimes", "Ask the administrator to verify runtime configuration; custom/container commands may still work.")
		}
		return doctorPass("remote-runtime", "available: "+strings.Join(available, ", "))
	}
	available, reported := runtimes[appType]
	if !reported {
		return doctorWarn("remote-runtime", fmt.Sprintf("the server did not report whether %s is available", appType), "Upgrade the server for runtime preflight, or confirm the runtime with an administrator.")
	}
	if !available {
		binary := map[string]string{"python": "uv", "r": "Rscript"}[appType]
		return doctorFail("remote-runtime", fmt.Sprintf("the server cannot run this %s app", appType), "Ask the administrator to install "+binary+" or configure a container runtime.", KindValidation, 1)
	}
	return doctorPass("remote-runtime", appType+" is available on the server")
}

func appendRemoteSkipped(checks []doctorCheck, reason string) []doctorCheck {
	return append(checks,
		doctorSkip("transport-security", reason),
		doctorSkip("server", reason),
		doctorSkip("version-compatibility", reason),
		doctorSkip("authentication", reason),
		doctorSkip("deploy-permission", reason),
		doctorSkip("remote-runtime", reason))
}

func appendRemoteAfterServerSkipped(checks []doctorCheck) []doctorCheck {
	return append(checks,
		doctorSkip("version-compatibility", "the server is unavailable"),
		doctorSkip("authentication", "the server is unavailable"),
		doctorSkip("deploy-permission", "the server is unavailable"),
		doctorSkip("remote-runtime", "the server is unavailable"))
}

func appendRemoteAfterCompatibilitySkipped(checks []doctorCheck) []doctorCheck {
	return append(checks,
		doctorSkip("authentication", "the server API protocol is incompatible"),
		doctorSkip("deploy-permission", "the server API protocol is incompatible"),
		doctorSkip("remote-runtime", "the server API protocol is incompatible"))
}

func doctorPass(name, detail string) doctorCheck {
	return doctorCheck{Name: name, Status: "pass", Detail: detail}
}

func doctorWarn(name, detail, fix string) doctorCheck {
	return doctorCheck{Name: name, Status: "warn", Detail: detail, Fix: fix}
}

func doctorFail(name, detail, fix string, kind Kind, code int) doctorCheck {
	if kind == "" {
		kind = KindInternal
	}
	if code == 0 {
		code = 1
	}
	return doctorCheck{Name: name, Status: "fail", Detail: detail, Fix: fix, kind: kind, code: code}
}

func doctorSkip(name, reason string) doctorCheck {
	return doctorCheck{Name: name, Status: "skip", Detail: reason}
}

func tallyDoctorChecks(checks []doctorCheck) doctorSummary {
	var summary doctorSummary
	for _, check := range checks {
		switch check.Status {
		case "pass":
			summary.Passed++
		case "warn":
			summary.Warned++
		case "fail":
			summary.Failed++
		case "skip":
			summary.Skipped++
		}
	}
	return summary
}

func doctorNextSteps(report doctorReport) []string {
	if report.Status != "ready" {
		return []string{"Apply the fixes above.", "Run `shinyhub doctor` again."}
	}
	steps := []string{}
	if report.Scope != "remote" && report.AppDir != "" {
		steps = append(steps, "shinyhub run "+shellQuote(doctorCommandPath(report.AppDir))+" --check")
	}
	if report.Scope != "local" && report.AppDir != "" && report.Slug != "" {
		steps = append(steps, "shinyhub deploy "+shellQuote(doctorCommandPath(report.AppDir))+" --slug "+report.Slug+" --wait")
	} else if report.Scope == "remote" {
		steps = append(steps, "Remote connection is ready.")
	}
	return steps
}

func doctorCommandPath(abs string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(cwd, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return abs
	}
	if rel == "" {
		return "."
	}
	return rel
}

func renderDoctorReport(out io.Writer, format outputFormat, report doctorReport) error {
	if format == formatJSON {
		return json.NewEncoder(out).Encode(report)
	}
	s := stylerFor(out)
	fmt.Fprintln(out, "ShinyHub doctor")
	if report.AppDir != "" {
		fmt.Fprintf(out, "App:    %s\n", report.AppDir)
	}
	if report.Host != "" {
		fmt.Fprintf(out, "Server: %s\n", report.Host)
	}
	fmt.Fprintln(out)
	for _, check := range report.Checks {
		mark := s.glyphOK()
		switch check.Status {
		case "fail":
			mark = s.failMark()
		case "warn":
			mark = s.yellow("!")
		case "skip":
			mark = s.dim("-")
		default:
			mark = s.green(mark)
		}
		fmt.Fprintf(out, "%s %-20s %s\n", mark, check.Name, check.Detail)
		if check.Fix != "" {
			fmt.Fprintf(out, "  %-20s %s\n", "Fix:", check.Fix)
		}
	}
	fmt.Fprintln(out)
	if report.Status == "ready" {
		fmt.Fprintf(out, "%s READY — %d checks passed", s.green(s.glyphOK()), report.Summary.Passed)
		if report.Summary.Warned > 0 {
			fmt.Fprintf(out, ", %d warning(s)", report.Summary.Warned)
		}
		fmt.Fprintln(out, ".")
	} else {
		fmt.Fprintf(out, "%s NOT READY — %d blocking problem(s), %d warning(s).\n", s.failMark(), report.Summary.Failed, report.Summary.Warned)
	}
	if len(report.NextSteps) > 0 {
		fmt.Fprintln(out, "Next:")
		for _, step := range report.NextSteps {
			fmt.Fprintf(out, "  %s\n", step)
		}
	}
	return nil
}

func doctorFailure(checks []doctorCheck) error {
	failed := 0
	chosenKind, chosenCode := KindValidation, 1
	for _, check := range checks {
		if check.Status != "fail" {
			continue
		}
		failed++
		if check.code == 6 || (chosenCode != 6 && check.code == 3) {
			chosenCode, chosenKind = check.code, check.kind
		}
	}
	return &ExitCodeError{Code: chosenCode, Kind: chosenKind, Reported: true, Err: &hintedMsgError{
		msg:  fmt.Sprintf("doctor found %d blocking problem(s)", failed),
		hint: "apply the fixes in the doctor report, then run `shinyhub doctor` again",
	}}
}

func doctorCredentialFix(err error) string {
	var hinted hintedError
	if errors.As(err, &hinted) && hinted.Hint() != "" {
		return hinted.Hint()
	}
	return "Run `shinyhub connect https://hub.example.com`, or set SHINYHUB_HOST and SHINYHUB_TOKEN in CI."
}

func insecureRemoteHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

func transportSecurityDetail(raw string) string {
	u, _ := url.Parse(raw)
	if u.Scheme == "http" {
		return "HTTP on loopback (local development)"
	}
	return "HTTPS"
}

func runtimeInstallFix(executable string) string {
	switch executable {
	case "uv":
		return "Install uv from https://docs.astral.sh/uv/getting-started/installation/, then rerun doctor."
	case "Rscript":
		return "Install R and ensure Rscript is on PATH, then rerun doctor."
	default:
		return "Install " + executable + " or update [app] command in shinyhub.toml."
	}
}

func shellSummary(argv []string) string {
	parts := argv
	if len(parts) > 5 {
		parts = append(append([]string{}, parts[:5]...), "…")
	}
	return strings.Join(parts, " ")
}
