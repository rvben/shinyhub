package nativebroker

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func within(path, root string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) || filepath.Clean(path) != path || filepath.Clean(root) != root {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (p Policy) Validate() error {
	if p.ControlUID <= 0 || p.ControlGID <= 0 {
		return errors.New("control plane must use a non-root identity")
	}
	roots := []string{p.StateDir, p.RuntimeDir}
	if !within(p.Socket, p.RuntimeDir) || p.Socket == p.RuntimeDir {
		return errors.New("broker socket must be beneath runtime_dir")
	}
	ids, uids, gids, slugs := map[int64]bool{}, map[int]bool{}, map[int]bool{}, map[string]bool{}
	for _, a := range p.Apps {
		if a.ID <= 0 || ids[a.ID] || a.UID <= 0 || a.UID == p.ControlUID || uids[a.UID] || a.GID <= 0 || a.GID == p.ControlGID || gids[a.GID] {
			return errors.New("app identities must be unique and separate from the control plane")
		}
		if a.Slug == "" || strings.ContainsAny(a.Slug, "/\\\x00\r\n") || slugs[a.Slug] {
			return errors.New("invalid or duplicate app slug")
		}
		ids[a.ID], uids[a.UID], gids[a.GID], slugs[a.Slug] = true, true, true, true
		roots = append(roots, a.BundleRoot, a.DataRoot, a.CacheRoot)
	}
	for i, r := range roots {
		if r == "/" || !filepath.IsAbs(r) || filepath.Clean(r) != r || strings.ContainsAny(r, " \t\r\n\x00%") {
			return errors.New("broker roots must be absolute, canonical paths without whitespace or systemd specifiers")
		}
		for _, other := range roots[:i] {
			if within(r, other) || within(other, r) {
				return errors.New("broker storage roots must not overlap")
			}
		}
	}
	return nil
}
func (p Policy) app(l *Launch) (App, error) {
	if l == nil {
		return App{}, errors.New("launch is required")
	}
	for _, a := range p.Apps {
		if a.ID == l.AppID && a.Slug == l.Slug {
			return a, nil
		}
	}
	return App{}, errors.New("app identity is not registered in broker policy")
}
func (p Policy) validateLaunch(l *Launch, fdCount int) (App, error) {
	a, err := p.app(l)
	if err != nil {
		return a, err
	}
	if l.Kind != "replica" && l.Kind != "job" && l.Kind != "build" && l.Kind != "hook" {
		return a, errors.New("invalid workload kind")
	}
	if !within(l.Dir, a.BundleRoot) {
		return a, errors.New("working directory is outside the registered app bundle root")
	}
	if l.DataDir != "" && l.DataDir != a.DataRoot {
		return a, errors.New("data directory does not match broker policy")
	}
	if l.CacheDir != "" && !within(l.CacheDir, a.CacheRoot) {
		return a, errors.New("cache directory does not match broker policy")
	}
	if len(l.Argv) == 0 || !filepath.IsAbs(l.Argv[0]) {
		return a, errors.New("command must use an absolute executable")
	}
	for _, arg := range l.Argv {
		if strings.ContainsRune(arg, 0) {
			return a, errors.New("invalid command argument")
		}
	}
	if l.MemoryMB < 0 || l.MemoryMB > 1048576 || l.CPUPercent < 0 || l.CPUPercent > 100000 {
		return a, errors.New("invalid resource limits")
	}
	expected := l.LifetimeCount + 1 // one write-only log descriptor
	if l.Guarded {
		expected++
	}
	if l.LifetimeCount < 0 || expected > MaxFiles || expected != fdCount {
		return a, errors.New("launch descriptor count does not match request")
	}
	for _, e := range l.Env {
		if strings.ContainsRune(e, 0) || !strings.Contains(e, "=") {
			return a, errors.New("invalid environment entry")
		}
	}
	if len(l.Labels) > 32 {
		return a, errors.New("too many workload metadata fields")
	}
	for k, v := range l.Labels {
		if !strings.HasPrefix(k, "shinyhub.") || len(k) > 128 || len(v) > 1024 {
			return a, fmt.Errorf("invalid workload metadata")
		}
	}
	return a, nil
}
