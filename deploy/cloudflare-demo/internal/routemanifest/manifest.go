// Package routemanifest derives the demo admission contract from the server.
package routemanifest

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/rvben/shinyhub/internal/hubroute"
	"github.com/rvben/shinyhub/internal/ui"
)

type route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Manifest is the generated contract consumed by the edge admission policy.
type Manifest struct {
	UIExact    []string `json:"uiExact"`
	UIPatterns []string `json:"uiPatterns"`
	API        []route  `json:"api"`
	Assets     []string `json:"assets"`
	Apps       []string `json:"apps"`
	Projects   []string `json:"projects"`
}

// Build reads registered routes and embedded assets without serving requests.
func Build() (Manifest, error) {
	m := Manifest{UIExact: append([]string{"/", "/invite"}, hubroute.ExactUIRoutes()...), UIPatterns: hubroute.UIPathPatterns()}
	dir, err := os.MkdirTemp("", "demo-route-manifest-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(dir)
	srv := api.New(&config.Config{Storage: config.StorageConfig{AppsDir: dir}}, nil, nil, nil)
	defer srv.Close()
	err = chi.Walk(srv.Router().(chi.Routes), func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		m.API = append(m.API, route{Method: method, Path: path})
		return nil
	})
	if err != nil {
		return m, err
	}
	// Never generate a production allowlist from a developer's override tree.
	if os.Getenv("SHINYHUB_DEV_STATIC") != "" {
		return m, fmt.Errorf("unset SHINYHUB_DEV_STATIC to enumerate shipped assets")
	}
	err = fs.WalkDir(ui.Static(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			m.Assets = append(m.Assets, path)
		}
		return nil
	})
	if err != nil {
		return m, err
	}
	data, err := os.ReadFile("deploy/cloudflare-demo/fleet.toml")
	if err != nil {
		return m, err
	}
	f, problems := fleet.ParseManifest(data, "fleet.toml")
	if len(problems) != 0 {
		return m, fmt.Errorf("invalid demo fleet: %v", problems)
	}
	for _, app := range f.Apps {
		m.Apps = append(m.Apps, app.Slug)
	}
	for _, project := range f.Projects {
		m.Projects = append(m.Projects, project.Slug)
	}
	sort.Strings(m.UIExact)
	sort.Strings(m.Assets)
	sort.Strings(m.Apps)
	sort.Strings(m.Projects)
	sort.Slice(m.API, func(i, j int) bool { return m.API[i].Path+" "+m.API[i].Method < m.API[j].Path+" "+m.API[j].Method })
	return m, nil
}
