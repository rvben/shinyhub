package fleet

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/rvben/shinyhub/internal/entitlementpolicy"
)

type Environment struct {
	Host string `toml:"host"`
}
type AppEnvironment struct {
	Config       OverlayConfig            `toml:"config"`
	Entitlements entitlementpolicy.Policy `toml:"entitlements"`
}

// OverlayConfig deliberately exposes only sizing, lifecycle and environment variables.
type OverlayConfig struct {
	Replicas                *int              `toml:"replicas"`
	MinWarmReplicas         *int              `toml:"min_warm_replicas"`
	MaxSessionsPerReplica   *int              `toml:"max_sessions_per_replica"`
	Autoscale               *AutoscaleConfig  `toml:"autoscale"`
	HibernateTimeoutMinutes *int              `toml:"hibernate_timeout_minutes"`
	RenderSeconds           *float64          `toml:"render_seconds"`
	Env                     map[string]string `toml:"env"`
}

func (o OverlayConfig) Merge(c Config) Config {
	if o.Replicas != nil {
		c.Replicas = o.Replicas
	}
	if o.MinWarmReplicas != nil {
		c.MinWarmReplicas = o.MinWarmReplicas
	}
	if o.MaxSessionsPerReplica != nil {
		c.MaxSessionsPerReplica = o.MaxSessionsPerReplica
	}
	if o.Autoscale != nil {
		c.Autoscale = o.Autoscale
	}
	if o.HibernateTimeoutMinutes != nil {
		c.HibernateTimeoutMinutes = o.HibernateTimeoutMinutes
	}
	if o.RenderSeconds != nil {
		c.RenderSeconds = o.RenderSeconds
	}
	if o.Env != nil {
		env := make(map[string]string, len(c.Env)+len(o.Env))
		for k, v := range c.Env {
			env[k] = v
		}
		for k, v := range o.Env {
			env[k] = v
		}
		c.Env = env
	}
	return c
}

func NormalizeEnvironmentHost(host string) (string, error) {
	u, err := url.Parse(host)
	if err == nil {
		u.Scheme = strings.ToLower(u.Scheme)
	}
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("host must be an absolute http(s) URL without credentials, query or fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
}

func (m *Manifest) EnvironmentNames() []string {
	names := make([]string, 0, len(m.Environments))
	for n := range m.Environments {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ResolveEnvironment fails closed on an unknown host. An explicit name can select
// an unlisted host, but cannot contradict a host bound to another environment.
func (m *Manifest) ResolveEnvironment(host, explicit string) error {
	if len(m.Environments) == 0 {
		if explicit != "" {
			return fmt.Errorf("environment %q is not declared", explicit)
		}
		return nil
	}
	target, err := NormalizeEnvironmentHost(host)
	if err != nil {
		return err
	}
	matched := ""
	for _, name := range m.EnvironmentNames() {
		normalized, _ := NormalizeEnvironmentHost(m.Environments[name].Host)
		if normalized == target {
			matched = name
		}
	}
	if explicit != "" {
		if _, ok := m.Environments[explicit]; !ok {
			return fmt.Errorf("environment %q is not declared", explicit)
		}
		if matched != "" && matched != explicit {
			return fmt.Errorf("host %s is bound to environment %q, not %q", host, matched, explicit)
		}
		matched = explicit
	}
	if matched == "" {
		return fmt.Errorf("host %s matches no declared environment; use --environment <name> to select explicitly", host)
	}
	m.ResolvedEnvironment = matched
	for i := range m.Apps {
		m.Apps[i].ResolvedEnvironment = matched
		if o, ok := m.Apps[i].Environments[matched]; ok {
			m.Apps[i].Config = o.Config.Merge(m.Apps[i].Config)
		}
	}
	return nil
}

func validateEnvironments(file string, m *Manifest) []Problem {
	var probs []Problem
	hosts := map[string]string{}
	for _, name := range m.EnvironmentNames() {
		if !ValidFleetID(name) {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("invalid environment name %q", name)})
		}
		host, err := NormalizeEnvironmentHost(m.Environments[name].Host)
		if err != nil {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("environment %q: %v", name, err)})
		} else if previous, ok := hosts[host]; ok {
			probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("environments %q and %q bind the same host", previous, name)})
		} else {
			hosts[host] = name
		}
	}
	for _, app := range m.Apps {
		names := make([]string, 0, len(app.Environments))
		for name := range app.Environments {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if _, ok := m.Environments[name]; !ok {
				probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("app %q: undeclared environment %q", app.Slug, name)})
			}
			if err := app.Environments[name].Entitlements.Validate(); err != nil {
				probs = append(probs, Problem{File: file, Msg: fmt.Sprintf("app %q environment %q entitlement policy: %v", app.Slug, name, err)})
			}
			merged := app.Environments[name].Config.Merge(app.Config)
			probs = append(probs, validateConfig(file, fmt.Sprintf("app %q environment %q", app.Slug, name), &merged)...)
		}
	}
	return probs
}

func (o OverlayConfig) Declares(key string) bool {
	switch key {
	case "replicas":
		return o.Replicas != nil
	case "min_warm_replicas":
		return o.MinWarmReplicas != nil
	case "max_sessions_per_replica":
		return o.MaxSessionsPerReplica != nil
	case "autoscale":
		return o.Autoscale != nil
	case "hibernate_timeout_minutes":
		return o.HibernateTimeoutMinutes != nil
	case "render_seconds":
		return o.RenderSeconds != nil
	}
	if strings.HasPrefix(key, "env.") {
		_, ok := o.Env[strings.TrimPrefix(key, "env.")]
		return ok
	}
	return false
}
