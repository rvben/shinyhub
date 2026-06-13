package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// looksLikeRApp reports whether dir is, heuristically, an R Shiny bundle: it has
// app.R, no app.py, and no shinyhub.toml that could redefine the run command.
// Used only to decide whether to emit a best-effort pre-flight warning, so a
// conservative heuristic (skip when uncertain) is correct.
func looksLikeRApp(dir string) bool {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	return has("app.R") && !has("app.py") && !has("shinyhub.toml")
}

// serverRuntimeAvailable does a best-effort GET /api/server-info and reports
// whether the named runtime is available. known is false when the server did
// not report runtimes (an older server) or the call failed, so callers stay
// silent rather than warn on uncertainty.
func serverRuntimeAvailable(cfg *cliConfig, lang string) (available, known bool) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/server-info", nil)
	if err != nil {
		return false, false
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	body, _ := io.ReadAll(resp.Body)
	var info struct {
		Runtimes map[string]bool `json:"runtimes"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.Runtimes == nil {
		return false, false
	}
	v, ok := info.Runtimes[lang]
	if !ok {
		return false, false
	}
	return v, true
}
