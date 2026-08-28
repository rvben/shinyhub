package cloudflaredemo

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/fleet"
)

type demoThemeContract struct {
	Canvas        string `json:"canvas"`
	Surface       string `json:"surface"`
	SurfaceRaised string `json:"surfaceRaised"`
	SurfaceHover  string `json:"surfaceHover"`
	Line          string `json:"line"`
	Text          string `json:"text"`
	TextSoft      string `json:"textSoft"`
	Accent        string `json:"accent"`
	AccentBright  string `json:"accentBright"`
}

func TestCloudflareDemoConfigurationIsEphemeralAndBounded(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("SHINYHUB_AUTH_SECRET", strings.Repeat("a", 64))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN", "shk_"+strings.Repeat("b", 64))
	t.Setenv("SHINYHUB_DEPLOY_TOKEN_ROLE", "admin")
	t.Setenv("SHINYHUB_APPS_DIR", filepath.Join(dataRoot, "apps"))
	t.Setenv("SHINYHUB_APP_DATA_DIR", filepath.Join(dataRoot, "app-data"))

	cfg, err := config.Load("shinyhub.yaml")
	if err != nil {
		t.Fatalf("load Cloudflare demo config: %v", err)
	}
	if cfg.Server.BaseURL != "https://demo.shinyhub.dev" {
		t.Fatalf("base URL = %q", cfg.Server.BaseURL)
	}
	if cfg.Server.AppOrigin != "https://apps.demo.shinyhub.dev" {
		t.Fatalf("app origin = %q", cfg.Server.AppOrigin)
	}
	if cfg.Runtime.Mode != "native" || cfg.Runtime.MaxReplicas != 1 {
		t.Fatalf("runtime = %q with max replicas %d", cfg.Runtime.Mode, cfg.Runtime.MaxReplicas)
	}
	if !cfg.Auth.LocalLoginEnabled() {
		t.Fatal("the public read-only demo account requires local login")
	}
}

func TestCloudflareDemoFleetIsCurated(t *testing.T) {
	data, err := os.ReadFile("fleet.toml")
	if err != nil {
		t.Fatal(err)
	}
	manifest, problems := fleet.ParseManifest(data, "fleet.toml")
	if len(problems) != 0 {
		t.Fatalf("fleet manifest problems: %v", problems)
	}
	if len(manifest.Apps) != 6 {
		t.Fatalf("demo fleet has %d apps, want 6", len(manifest.Apps))
	}
	for _, app := range manifest.Apps {
		if app.Visibility != "public" {
			t.Errorf("app %q visibility = %q", app.Slug, app.Visibility)
		}
		if app.Config.Replicas == nil || *app.Config.Replicas != 1 {
			t.Errorf("app %q must declare one replica", app.Slug)
		}
	}
}

func TestCloudflareDemoOneClickEntryIsReadOnlyAndCoveredBySmokeTest(t *testing.T) {
	worker, err := os.ReadFile("src/index.ts")
	if err != nil {
		t.Fatal(err)
	}
	workerSource := string(worker)
	for _, required := range []string{
		`DEMO_VIEWER_USERNAME = "demo-viewer"`,
		`url.pathname === DEMO_STYLE_PATH`,
		`url.pathname === DEMO_SCRIPT_PATH`,
		`url.pathname === DEMO_SESSION_PATH`,
		`request.method !== "POST"`,
		`request.headers.get("sec-fetch-site") === "cross-site"`,
		`new URL("/api/auth/session", url)`,
	} {
		if !strings.Contains(workerSource, required) {
			t.Errorf("demo Worker is missing one-click entry contract %q", required)
		}
	}

	entry, err := os.ReadFile("src/demo-login.ts")
	if err != nil {
		t.Fatal(err)
	}
	entrySource := string(entry)
	for _, required := range []string{
		`Enter the live demo`,
		`Read-only access`,
		`aria-controls="login-form"`,
		`rel="stylesheet" href="${DEMO_STYLE_PATH}"`,
		`script src="${DEMO_SCRIPT_PATH}"`,
		`@media (prefers-reduced-motion: reduce)`,
	} {
		if !strings.Contains(entrySource, required) {
			t.Errorf("demo entry UI is missing %q", required)
		}
	}

	smoke, err := os.ReadFile("../../scripts/demo-smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(smoke), `/__demo/session`) {
		t.Fatal("production smoke test does not exercise one-click demo entry")
	}
}

func TestCloudflareDemoDeploysAfterSuccessfulReleases(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/demo.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflowSource := string(workflow)
	for _, required := range []string{
		`workflows: ["release"]`,
		`github.event.workflow_run.conclusion == 'success'`,
		`github.event.workflow_run.head_sha`,
		`node-version: "24"`,
		`Verify Cloudflare deployment credentials`,
		`CLOUDFLARE_ACCOUNT_ID: ${{ vars.CLOUDFLARE_ACCOUNT_ID }}`,
		`CLOUDFLARE_API_TOKEN must contain only the bare token value`,
		`CLOUDFLARE_ACCOUNT_ID must be a 32-character lowercase hexadecimal account ID`,
		`npx wrangler whoami --account "$CLOUDFLARE_ACCOUNT_ID"`,
		`run: npm run deploy`,
		`SHINYHUB_DEMO_EXPECTED_VERSION`,
		`./scripts/demo-smoke.sh`,
	} {
		if !strings.Contains(workflowSource, required) {
			t.Errorf("demo deployment workflow is missing %q", required)
		}
	}

	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	dockerfileSource := string(dockerfile)
	for _, required := range []string{
		`COPY package.json ./`,
		`-X main.version=${VERSION}`,
		`"shinyhub-bookmarks==0.3.0"`,
		`COPY examples/bookmarking-demo/app.py /opt/shinyhub-demo/apps/bookmarking-demo/app.py`,
	} {
		if !strings.Contains(dockerfileSource, required) {
			t.Errorf("demo image is missing contract %q", required)
		}
	}

	smoke, err := os.ReadFile("../../scripts/demo-smoke.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`$app_url/app/bookmarking-demo/`,
		`$websocket_base/app/bookmarking-demo/websocket/`,
	} {
		if !strings.Contains(string(smoke), required) {
			t.Errorf("demo smoke test is missing bookmarking app contract %q", required)
		}
	}
}

func TestDocumentationDeployUsesScopedCloudflareCredentials(t *testing.T) {
	workflow, err := os.ReadFile("../../.github/workflows/docs.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflowSource := string(workflow)
	for _, required := range []string{
		`group: documentation-production`,
		`cancel-in-progress: true`,
		`name: documentation`,
		`url: https://shinyhub.dev`,
		`Verify Cloudflare deployment credentials`,
		`CLOUDFLARE_API_TOKEN: ${{ secrets.CLOUDFLARE_API_TOKEN }}`,
		`CLOUDFLARE_ACCOUNT_ID: ${{ vars.CLOUDFLARE_ACCOUNT_ID }}`,
		`CLOUDFLARE_API_TOKEN must contain only the bare token value`,
		`CLOUDFLARE_ACCOUNT_ID must be a 32-character lowercase hexadecimal account ID`,
		`accountId: ${{ vars.CLOUDFLARE_ACCOUNT_ID }}`,
		`wranglerVersion: "4.126.0"`,
		`command: pages deploy site --project-name=shinyhub-dev`,
	} {
		if !strings.Contains(workflowSource, required) {
			t.Errorf("documentation deployment workflow is missing %q", required)
		}
	}
	if strings.Contains(workflowSource, `accountId: ${{ secrets.CLOUDFLARE_ACCOUNT_ID }}`) {
		t.Error("documentation deployment must source the non-secret account ID from the environment variable")
	}
}

func TestCloudflareDemoViewerHasCompleteIdentityClaims(t *testing.T) {
	bootstrap, err := os.ReadFile("bootstrap-viewer.py")
	if err != nil {
		t.Fatal(err)
	}
	bootstrapSource := string(bootstrap)
	for _, required := range []string{
		`USERNAME = "demo-viewer"`,
		`DISPLAY_NAME = "Demo Viewer"`,
		`GROUPS = ("analytics-readers", "demo-users")`,
		`UPDATE users SET display_name = ?`,
		`INSERT INTO user_groups`,
	} {
		if !strings.Contains(bootstrapSource, required) {
			t.Errorf("demo viewer bootstrap is missing %q", required)
		}
	}

	entrypoint, err := os.ReadFile("entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entrypoint), `python /opt/shinyhub-demo/bootstrap-viewer.py /data/shinyhub.db`) {
		t.Fatal("container entrypoint does not populate the demo viewer identity")
	}

	dockerfile, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), `COPY deploy/cloudflare-demo/bootstrap-viewer.py /opt/shinyhub-demo/bootstrap-viewer.py`) {
		t.Fatal("container image does not include the demo viewer bootstrap")
	}
}

func TestCloudflareDemoAppsUseAccessibleDarkThemeContract(t *testing.T) {
	contractData, err := os.ReadFile("../demo/theme-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var theme demoThemeContract
	if err := json.Unmarshal(contractData, &theme); err != nil {
		t.Fatal(err)
	}

	for name, pair := range map[string][2]string{
		"body text on canvas":  {theme.Text, theme.Canvas},
		"body text on surface": {theme.Text, theme.Surface},
		"soft text on surface": {theme.TextSoft, theme.Surface},
		"accent on canvas":     {theme.Accent, theme.Canvas},
		"accent on surface":    {theme.AccentBright, theme.Surface},
	} {
		if ratio := contrastRatio(t, pair[0], pair[1]); ratio < 4.5 {
			t.Errorf("%s contrast = %.2f:1, want >= 4.5:1", name, ratio)
		}
	}

	fullPalette := []string{
		theme.Canvas, theme.Surface, theme.SurfaceRaised, theme.SurfaceHover,
		theme.Line, theme.Text, theme.TextSoft, theme.Accent, theme.AccentBright,
	}
	for _, path := range []string{
		"../demo/apps/operations-dashboard/app.py",
		"../demo/apps/r-shiny-gallery/app.R",
		"../../examples/dash-demo/assets/style.css",
		"../../examples/identity-demo/app.py",
		"../../examples/bookmarking-demo/app.py",
	} {
		assertThemeAdapterContains(t, path, fullPalette)
	}
	assertThemeAdapterContains(t, "../../examples/streamlit-demo/.streamlit/config.toml", []string{
		theme.Canvas, theme.Surface, theme.Text, theme.Accent,
	})

	for path, marker := range map[string]string{
		"../demo/apps/operations-dashboard/app.py":             `.metric-value { margin-top: 8px; color: var(--text);`,
		"../demo/apps/r-shiny-gallery/app.R":                   `color-scheme:dark`,
		"../../examples/dash-demo/app.py":                      `html.Label("Input value", htmlFor="n")`,
		"../../examples/dash-demo/assets/style.css":            `color-scheme: dark`,
		"../../examples/identity-demo/app.py":                  `ui.h1("See the identity your app receives.")`,
		"../../examples/bookmarking-demo/app.py":               `color-scheme: dark`,
		"../../examples/streamlit-demo/.streamlit/config.toml": `base = "dark"`,
		"Dockerfile": `COPY examples/streamlit-demo/.streamlit`,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), marker) {
			t.Errorf("%s is missing dark-theme contract %q", path, marker)
		}
	}
}

func assertThemeAdapterContains(t *testing.T, path string, colors []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ToLower(string(data))
	for _, color := range colors {
		if !strings.Contains(source, strings.ToLower(color)) {
			t.Errorf("%s is missing theme color %s", path, color)
		}
	}
}

func contrastRatio(t *testing.T, foreground, background string) float64 {
	t.Helper()
	foregroundLuminance := relativeLuminance(t, foreground)
	backgroundLuminance := relativeLuminance(t, background)
	lighter := math.Max(foregroundLuminance, backgroundLuminance)
	darker := math.Min(foregroundLuminance, backgroundLuminance)
	return (lighter + 0.05) / (darker + 0.05)
}

func relativeLuminance(t *testing.T, color string) float64 {
	t.Helper()
	if len(color) != 7 || color[0] != '#' {
		t.Fatalf("invalid color %q", color)
	}
	channels := make([]float64, 3)
	for i := range channels {
		value, err := strconv.ParseUint(color[1+i*2:3+i*2], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		channel := float64(value) / 255
		if channel <= 0.04045 {
			channels[i] = channel / 12.92
		} else {
			channels[i] = math.Pow((channel+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}
