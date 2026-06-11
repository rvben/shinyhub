package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// envFileEntry is a single KEY=VALUE pair parsed from a dotenv-style file,
// annotated with whether the caller asked for it to be stored as a secret.
type envFileEntry struct {
	Key    string
	Value  string
	Secret bool
}

// parseEnvFile reads a dotenv-style file from r. The grammar is:
//
//	[export ]KEY=VALUE          unquoted; trailing whitespace stripped
//	[export ]KEY="value"        double-quoted; \n \t \" \\ interpreted
//	[export ]KEY='value'        single-quoted; literal
//	# comment                   ignored
//	<blank>                     ignored
//
// Returns a deterministic slice ordered by first appearance (so plan output
// is reproducible). Duplicate keys are an error: an apply with two values
// for the same variable is almost certainly a mistake, and choosing the
// last-wins or first-wins rule silently would hide it.
func parseEnvFile(r io.Reader) ([]envFileEntry, error) {
	scanner := bufio.NewScanner(r)
	// Allow long values (env vars can be up to 64 KiB on the server).
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var out []envFileEntry
	seen := make(map[string]struct{})
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimLeft(line, " \t")

		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE, got %q", lineNo, raw)
		}
		key := strings.TrimSpace(line[:eq])
		if !envKeyRegex.MatchString(key) {
			return nil, fmt.Errorf("line %d: invalid key %q (must match [A-Z_][A-Z0-9_]*)", lineNo, key)
		}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("line %d: duplicate key %q", lineNo, key)
		}
		seen[key] = struct{}{}

		val, err := parseEnvValue(line[eq+1:])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out = append(out, envFileEntry{Key: key, Value: val})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}
	return out, nil
}

// parseEnvValue decodes the right-hand side of a KEY=VALUE pair. The quoting
// rules match Docker Compose / docker run --env-file and bash-ish dotenv
// parsers (dotenv-linter, python-dotenv): trailing comments are only
// recognised on unquoted lines.
func parseEnvValue(raw string) (string, error) {
	raw = strings.TrimLeft(raw, " \t")
	if raw == "" {
		return "", nil
	}
	switch raw[0] {
	case '"':
		end := findClosingQuote(raw, '"')
		if end < 0 {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		return unescapeDouble(raw[1:end]), nil
	case '\'':
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		return raw[1 : 1+end], nil
	default:
		// Strip a trailing inline comment (` #...`). The leading whitespace
		// requirement avoids eating `#` inside a URL fragment.
		if i := strings.Index(raw, " #"); i >= 0 {
			raw = raw[:i]
		}
		return strings.TrimRight(raw, " \t"), nil
	}
}

// findClosingQuote walks the string from index 1 and returns the index of
// the matching closing quote, honouring backslash escapes. Returns -1 when
// the quote is never closed.
func findClosingQuote(s string, q byte) int {
	escaped := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == q {
			return i
		}
	}
	return -1
}

// unescapeDouble decodes the common escape sequences inside a double-quoted
// dotenv value. Unknown escapes are kept as the literal character (matching
// docker's permissive behaviour) so an unusual file never silently corrupts.
func unescapeDouble(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte(s[i+1])
		}
		i++
	}
	return b.String()
}

// envServerVar mirrors the wire format of GET /api/apps/<slug>/env. The
// server omits Value for secret entries.
type envServerVar struct {
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	Secret    bool   `json:"secret"`
	Set       bool   `json:"set"`
	UpdatedAt int64  `json:"updated_at"`
}

// envApplyPlan describes what `env apply` would do (or did). Values are
// deliberately omitted so JSON output is safe to log even when secrets are
// present in the source file.
type envApplyPlan struct {
	Adds    []envApplyOp `json:"adds"`
	Updates []envApplyOp `json:"updates"`
	Deletes []envApplyOp `json:"deletes"`
	Skipped []envApplyOp `json:"skipped"`
}

type envApplyOp struct {
	Key    string `json:"key"`
	Secret bool   `json:"secret,omitempty"`
	// Reason is set on Updates only ("value", "secret-flag", "secret-rotate").
	Reason string `json:"reason,omitempty"`
}

// diffEnvApply computes the set of operations required to bring the server's
// state in line with the desired state. The desired state is `desired`; the
// current server state is `current`. `prune` controls whether server-side
// keys not present in `desired` are scheduled for deletion.
//
// Secret values cannot be compared by content because the server masks them
// in GET responses. Two conservative rules apply:
//  1. Marking a non-secret key as secret (or vice versa) is always an Update
//     with reason "secret-flag".
//  2. Re-applying a secret that already exists is an Update with reason
//     "secret-rotate", because the caller may have changed the value and we
//     can't tell. Stale-no-op is preferable to stale-yes-actually-changed.
func diffEnvApply(desired []envFileEntry, current []envServerVar, prune bool) envApplyPlan {
	curr := make(map[string]envServerVar, len(current))
	for _, v := range current {
		curr[v.Key] = v
	}
	var plan envApplyPlan
	desiredKeys := make(map[string]struct{}, len(desired))
	for _, d := range desired {
		desiredKeys[d.Key] = struct{}{}
		c, exists := curr[d.Key]
		if !exists {
			plan.Adds = append(plan.Adds, envApplyOp{Key: d.Key, Secret: d.Secret})
			continue
		}
		switch {
		case c.Secret != d.Secret:
			plan.Updates = append(plan.Updates, envApplyOp{Key: d.Key, Secret: d.Secret, Reason: "secret-flag"})
		case d.Secret:
			plan.Updates = append(plan.Updates, envApplyOp{Key: d.Key, Secret: true, Reason: "secret-rotate"})
		case c.Value != d.Value:
			plan.Updates = append(plan.Updates, envApplyOp{Key: d.Key, Reason: "value"})
		default:
			plan.Skipped = append(plan.Skipped, envApplyOp{Key: d.Key, Secret: d.Secret})
		}
	}
	if prune {
		for k, v := range curr {
			if _, ok := desiredKeys[k]; !ok {
				plan.Deletes = append(plan.Deletes, envApplyOp{Key: k, Secret: v.Secret})
			}
		}
	}
	sortPlan(&plan)
	return plan
}

// sortPlan orders each bucket by key so output is deterministic.
func sortPlan(p *envApplyPlan) {
	for _, s := range []*[]envApplyOp{&p.Adds, &p.Updates, &p.Deletes, &p.Skipped} {
		sort.Slice(*s, func(i, j int) bool { return (*s)[i].Key < (*s)[j].Key })
	}
}

// applyEnvPlan executes the plan against the server. On the first failure
// it stops and returns the error along with the number of ops that
// succeeded — partial application is reported so the operator knows the
// server is now in a hybrid state.
func applyEnvPlan(cfg *cliConfig, slug string, plan envApplyPlan, desiredByKey map[string]envFileEntry, restart bool) (applied int, err error) {
	for _, op := range plan.Adds {
		if err := putEnv(cfg, slug, op.Key, desiredByKey[op.Key].Value, op.Secret, false); err != nil {
			return applied, fmt.Errorf("add %s: %w", op.Key, err)
		}
		applied++
	}
	for _, op := range plan.Updates {
		if err := putEnv(cfg, slug, op.Key, desiredByKey[op.Key].Value, desiredByKey[op.Key].Secret, false); err != nil {
			return applied, fmt.Errorf("update %s: %w", op.Key, err)
		}
		applied++
	}
	for _, op := range plan.Deletes {
		if err := deleteEnv(cfg, slug, op.Key, false); err != nil {
			return applied, fmt.Errorf("delete %s: %w", op.Key, err)
		}
		applied++
	}
	// Issue a final no-op request with ?restart=true so the restart happens
	// once at the end of the batch rather than after every key. We piggyback
	// on the last applied key when available; if the plan is empty there is
	// nothing to restart for.
	if restart && applied > 0 {
		last := lastAppliedKey(plan)
		if last != "" {
			if d, ok := desiredByKey[last]; ok {
				if err := putEnv(cfg, slug, last, d.Value, d.Secret, true); err != nil {
					return applied, fmt.Errorf("restart: %w", err)
				}
			}
		}
	}
	return applied, nil
}

// lastAppliedKey returns a key that the apply path will have just written
// to the server. Order matches applyEnvPlan: adds, then updates, then deletes.
func lastAppliedKey(plan envApplyPlan) string {
	if n := len(plan.Updates); n > 0 {
		return plan.Updates[n-1].Key
	}
	if n := len(plan.Adds); n > 0 {
		return plan.Adds[n-1].Key
	}
	return ""
}

// putEnv issues a PUT for a single env var. restart=true sets ?restart=true.
func putEnv(cfg *cliConfig, slug, key, value string, secret, restart bool) error {
	body, err := json.Marshal(map[string]any{"value": value, "secret": secret})
	if err != nil {
		return err
	}
	rawURL := cfg.Host + "/api/apps/" + slug + "/env/" + key
	if restart {
		rawURL += "?restart=true"
	}
	req, err := http.NewRequest("PUT", rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return httpError(cfg.Token, "set env", resp, out)
	}
	return nil
}

// deleteEnv issues a DELETE for a single env var. restart is currently
// unused on this path but kept symmetric with putEnv for future use.
func deleteEnv(cfg *cliConfig, slug, key string, restart bool) error {
	rawURL := cfg.Host + "/api/apps/" + slug + "/env/" + key
	if restart {
		rawURL += "?restart=true"
	}
	req, err := http.NewRequest("DELETE", rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return httpError(cfg.Token, "remove env", resp, out)
	}
	return nil
}

// fetchCurrentEnv retrieves the server-side env list for slug.
func fetchCurrentEnv(cfg *cliConfig, slug string) ([]envServerVar, error) {
	req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/env", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, httpError(cfg.Token, "list env", resp, body)
	}
	var parsed struct {
		Env []envServerVar `json:"env"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode env list: %w", err)
	}
	return parsed.Env, nil
}

// renderEnvPlanText writes a human-readable plan summary to w. Empty buckets
// are omitted so a small change reads at a glance.
func renderEnvPlanText(w io.Writer, plan envApplyPlan, dryRun bool, applied int) {
	if dryRun {
		fmt.Fprintln(w, "Plan (dry run — no changes applied):")
	}
	for _, op := range plan.Adds {
		flag := ""
		if op.Secret {
			flag = " (secret)"
		}
		fmt.Fprintf(w, "  + %s%s\n", op.Key, flag)
	}
	for _, op := range plan.Updates {
		flag := ""
		if op.Secret {
			flag = " (secret)"
		}
		fmt.Fprintf(w, "  ~ %s%s [%s]\n", op.Key, flag, op.Reason)
	}
	for _, op := range plan.Deletes {
		fmt.Fprintf(w, "  - %s\n", op.Key)
	}
	if len(plan.Adds)+len(plan.Updates)+len(plan.Deletes) == 0 {
		fmt.Fprintln(w, "  (no changes)")
	}
	if !dryRun {
		fmt.Fprintf(w, "Applied %d change(s); %d unchanged.\n", applied, len(plan.Skipped))
	}
}

// newEnvApplyCmd builds the `env apply` subcommand. It is factored out so
// newEnvCmd can compose it without making this file depend on cobra wiring.
func newEnvApplyCmd() *cobra.Command {
	var flags struct {
		dryRun  bool
		prune   bool
		secrets []string
		format  string
		restart bool
	}
	cmd := &cobra.Command{
		Use:   "apply <slug> <file>",
		Short: "Declaratively sync an app's env from a dotenv file",
		Args:  cobra.ExactArgs(2),
	}
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Print the plan and exit without writing")
	cmd.Flags().BoolVar(&flags.prune, "prune", false, "Delete server-side keys absent from the file")
	cmd.Flags().StringSliceVar(&flags.secrets, "secret", nil, "Comma-separated keys to mark as secret (repeatable)")
	cmd.Flags().StringVar(&flags.format, "format", "text", "Output format: text or json")
	cmd.Flags().BoolVar(&flags.restart, "restart", false, "Restart the app once after all changes are applied")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		path := args[1]

		switch flags.format {
		case "text", "json":
		default:
			return validationErr(fmt.Sprintf("--format must be text or json, got %q", flags.format), "")
		}
		envApplyFormat, fmtErr := resolveLegacyTextJSON(flags.format)
		if fmtErr != nil {
			return fmtErr
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		entries, err := parseEnvFile(f)
		f.Close()
		if err != nil {
			return err
		}

		secretSet := make(map[string]struct{}, len(flags.secrets))
		for _, raw := range flags.secrets {
			for _, k := range strings.Split(raw, ",") {
				k = strings.TrimSpace(k)
				if k != "" {
					secretSet[k] = struct{}{}
				}
			}
		}
		for k := range secretSet {
			if !envKeyRegex.MatchString(k) {
				return fmt.Errorf("--secret value %q is not a valid env key", k)
			}
		}
		desiredByKey := make(map[string]envFileEntry, len(entries))
		for i := range entries {
			if _, ok := secretSet[entries[i].Key]; ok {
				entries[i].Secret = true
			}
			desiredByKey[entries[i].Key] = entries[i]
		}
		// Any --secret key that's not actually in the file is almost
		// certainly a typo — fail loudly rather than silently no-op.
		for k := range secretSet {
			if _, ok := desiredByKey[k]; !ok {
				return fmt.Errorf("--secret %q not present in %s", k, path)
			}
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		current, err := fetchCurrentEnv(cfg, slug)
		if err != nil {
			return fmt.Errorf("fetch current env: %w", err)
		}
		plan := diffEnvApply(entries, current, flags.prune)

		w := cmd.OutOrStdout()
		if flags.dryRun {
			if envApplyFormat == formatJSON {
				return json.NewEncoder(w).Encode(plan)
			}
			renderEnvPlanText(w, plan, true, 0)
			return nil
		}

		applied, applyErr := applyEnvPlan(cfg, slug, plan, desiredByKey, flags.restart)
		if envApplyFormat == formatJSON {
			out := struct {
				envApplyPlan
				Applied int    `json:"applied"`
				Error   string `json:"error,omitempty"`
			}{envApplyPlan: plan, Applied: applied}
			if applyErr != nil {
				out.Error = applyErr.Error()
			}
			if err := json.NewEncoder(w).Encode(out); err != nil {
				return err
			}
			return applyErr
		}
		renderEnvPlanText(w, plan, false, applied)
		return applyErr
	}
	return cmd
}
