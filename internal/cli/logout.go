package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// logoutFlags holds the parsed flags for a single `logout` invocation.
type logoutFlags struct {
	all bool
}

// newLogoutCmd builds a fresh logout command each time it is called.
func newLogoutCmd() *cobra.Command {
	f := &logoutFlags{}
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Sign out of the current ShinyHub server",
		Long: `Logout removes the saved credential for the current server and asks that
server to revoke the current JWT (best-effort - the local credential is removed
even if the server cannot be reached). API-key callers have nothing to revoke
server-side; the credential is still removed.

Only the current server is affected: other servers you are signed in to keep
their credentials, and one of them becomes current. Use ` + "`--host`" + ` to sign out
of a specific saved server, or ` + "`--all`" + ` to sign out of every one.`,
		Args: cobra.NoArgs,
	}
	cmd.Flags().BoolVar(&f.all, "all", false, "Sign out of every saved server and remove the credentials file")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runLogoutWith(cmd, f)
	}
	return cmd
}

func runLogoutWith(cmd *cobra.Command, f *logoutFlags) error {
	out := cmd.OutOrStdout()
	st, err := loadStore()
	if err != nil {
		return err
	}

	if f.all {
		// --host picks one server; --all is every server. Together they say two
		// different things, and the destructive reading wins silently, so the
		// combination is refused. Only the flag counts: SHINYHUB_HOST is ambient
		// (a CI shell often exports it for every command) and rejecting on it
		// would break `logout --all` in exactly the scripts that need it.
		if err := rejectHostFlag("--all already signs out of every saved server",
			fmt.Sprintf("run `shinyhub logout --host %s` for that one server, or `shinyhub logout --all` for all of them", hostFlagOverride)); err != nil {
			return err
		}
		return logoutAll(cmd, st)
	}

	// Resolve the same way every other command does, so `logout` signs out of
	// exactly the server the next command would have talked to - including the
	// env-only case, where there is no stored entry to remove but there is a
	// live credential the user needs to be told about.
	cfg, err := st.resolve(hostFlagOverride, os.Getenv("SHINYHUB_HOST"), os.Getenv("SHINYHUB_TOKEN"))
	if err != nil {
		// Two different failures arrive here and only one is benign. An auth
		// kind means there is nothing to sign out of, which is the idempotent
		// case. A validation kind means the target itself did not resolve - an
		// unknown name, or a URL with no scheme - and reporting that as success
		// would tell someone who mistyped `--host prodd` that they had signed
		// out of prod while the credential sat untouched on disk.
		if kind, _ := classify(err); kind == KindValidation {
			return err
		}
		fmt.Fprintln(out, "Not logged in.")
		return nil
	}

	// Best-effort revoke: server-side cleanup is desirable but must not block
	// local cleanup. Network errors and 4xx/5xx are warnings, not failures.
	if err := revokeServerSession(cfg); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not revoke session on server: %v\n", err)
	}

	_, stored := st.Hosts[cfg.Host]
	delete(st.Hosts, cfg.Host)
	if st.CurrentHost == cfg.Host {
		st.CurrentHost = ""
		if remaining := st.sortedHosts(); len(remaining) > 0 {
			st.CurrentHost = remaining[0]
		}
	}

	removedFile, err := persistAfterLogout(st, stored)
	if err != nil {
		return err
	}

	fields := map[string]any{
		"host":                cfg.Host,
		"current_host":        st.CurrentHost,
		"remaining_hosts":     len(st.Hosts),
		"credentials_removed": removedFile,
	}
	var prose string
	switch {
	case st.CurrentHost != "":
		prose = fmt.Sprintf("Logged out of %s. Now using %s", cfg.Host, st.label(st.CurrentHost))
	case removedFile:
		prose = fmt.Sprintf("Logged out of %s. Removed %s", cfg.Host, configPath())
	default:
		prose = fmt.Sprintf("Logged out of %s.", cfg.Host)
	}
	if err := renderAction(cmd, "logged_out", fields, prose); err != nil {
		return err
	}
	warnEnvCredentialsRemain(cmd)
	return nil
}

// logoutAll signs out of every saved server and removes the credentials file.
// Each revoke is attempted so no server is left holding a session the user
// believes is gone, and one failure does not stop the rest.
func logoutAll(cmd *cobra.Command, st *credentialStore) error {
	hosts := st.sortedHosts()
	if len(hosts) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Not logged in.")
		return nil
	}
	for _, host := range hosts {
		cred := st.Hosts[host]
		if cred.Token == "" {
			continue
		}
		if err := revokeServerSession(&cliConfig{Host: host, Token: cred.Token}); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not revoke session on %s: %v\n", host, err)
		}
	}
	st.Hosts = map[string]hostCredential{}
	st.CurrentHost = ""
	removedFile, err := persistAfterLogout(st, true)
	if err != nil {
		return err
	}
	prose := fmt.Sprintf("Logged out of %d server(s).", len(hosts))
	if removedFile {
		prose = fmt.Sprintf("Logged out of %d server(s). Removed %s", len(hosts), configPath())
	}
	if err := renderAction(cmd, "logged_out", map[string]any{
		"hosts":               hosts,
		"current_host":        "",
		"remaining_hosts":     0,
		"credentials_removed": removedFile,
	}, prose); err != nil {
		return err
	}
	warnEnvCredentialsRemain(cmd)
	return nil
}

// persistAfterLogout writes what is left of the store, deleting the file
// outright once nothing is left. An empty file would be indistinguishable from
// a corrupt one to the next reader, and leaving a stale credentials file behind
// after the user asked to log out is the wrong default. changed is false when
// the credential came from the environment rather than the file, in which case
// there is nothing on disk to rewrite.
func persistAfterLogout(st *credentialStore, changed bool) (bool, error) {
	if len(st.Hosts) > 0 {
		if !changed {
			return false, nil
		}
		return false, saveStore(st)
	}
	path := configPath()
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("remove %s: %w", path, err)
	}
	return true, nil
}

// warnEnvCredentialsRemain warns when the environment still supplies
// credentials. SHINYHUB_TOKEN overrides (and survives the removal of) the
// on-disk file, so without an explicit `unset` the very next command would
// silently re-authenticate from env - making the "Logged out" message a lie.
// This matters most for API keys (shk_ prefix), which have no server-side
// revocation endpoint: the env-sourced key stays valid until the user removes
// it from their shell.
func warnEnvCredentialsRemain(cmd *cobra.Command) {
	if os.Getenv("SHINYHUB_TOKEN") == "" {
		return
	}
	vars := "SHINYHUB_TOKEN"
	if os.Getenv("SHINYHUB_HOST") != "" {
		vars = "SHINYHUB_HOST and SHINYHUB_TOKEN"
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Note: %s still set in your environment; subsequent commands will continue to authenticate. Run `unset %s` to fully sign out.\n",
		vars, strings.ReplaceAll(vars, " and ", " "))
}

// revokeServerSession POSTs to /api/auth/logout so the server can revoke the
// caller's JWT by jti. Any 2xx response is success; non-2xx and transport
// errors are returned to the caller for warning-level reporting.
func revokeServerSession(cfg *cliConfig) error {
	req, err := http.NewRequest("POST", cfg.Host+"/api/auth/logout", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return &httpStatusError{
			Status: resp.StatusCode,
			msg:    fmt.Sprintf("server returned %s", resp.Status),
		}
	}
	return nil
}
