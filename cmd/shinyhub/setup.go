package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rvben/shinyhub/internal/auth"
	shinycli "github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const defaultServerConfigPath = "shinyhub.yaml"

type setupFlags struct {
	configPath        string
	adminUser         string
	adminPasswordFile string
}

type setupResult struct {
	ConfigPath    string
	DatabaseDSN   string
	URL           string
	Username      string
	CreatedConfig bool
	CreatedAdmin  bool
}

// Indirection seams keep the real setup password off echoed stdin while making
// the interactive flow deterministic in tests.
var (
	setupIsStdinTTY   = func() bool { return term.IsTerminal(int(syscall.Stdin)) }
	setupReadPassword = func() (string, error) {
		b, err := term.ReadPassword(int(syscall.Stdin))
		return string(b), err
	}
)

func newInitCmd() *cobra.Command {
	f := &setupFlags{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up ShinyHub for its first run",
		// The terminal prompt states the password minimum before you type it.
		// An unattended caller never sees that prompt, so the requirement has to
		// be here too, or the only way to learn it is to have a run rejected.
		Long: fmt.Sprintf(`Creates a private server configuration with a cryptographically random
auth secret, prepares the database, and creates the first administrator.

Existing configuration and users are never overwritten. In a terminal, missing
credentials are prompted for without echoing the password. For unattended setup,
set SHINYHUB_ADMIN_USER and SHINYHUB_ADMIN_PASSWORD, or pass --admin-user and
--admin-password-file.

The administrator password must be at least %d characters, whichever way it is
supplied.`, auth.MinPasswordLength),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := runSetup(cmd, f)
			if err != nil {
				return err
			}
			return shinycli.RenderAction(cmd, "initialized", map[string]any{
				"config_path":    result.ConfigPath,
				"database_dsn":   result.DatabaseDSN,
				"url":            result.URL,
				"username":       result.Username,
				"created_config": result.CreatedConfig,
				"created_admin":  result.CreatedAdmin,
			}, setupResultProse(result))
		},
	}
	cmd.Flags().StringVar(&f.configPath, "config", "", "Path to the server config file (overrides SHINYHUB_CONFIG; default ./shinyhub.yaml)")
	cmd.Flags().StringVar(&f.adminUser, "admin-user", "", "Username for the first administrator (default admin in a terminal)")
	cmd.Flags().StringVar(&f.adminPasswordFile, "admin-password-file", "", fmt.Sprintf("Read the first administrator password from a file (at least %d characters)", auth.MinPasswordLength))
	return cmd
}

// maybeRunInteractiveSetup turns the most natural first command — `shinyhub
// serve` — into the complete happy path. Environment-driven and configured
// deployments are left untouched. Non-interactive callers get a precise,
// copyable recovery instruction instead of a partial setup.
func maybeRunInteractiveSetup(cmd *cobra.Command) (*setupResult, error) {
	path := serverConfigPath()
	// Either way of supplying the secret means the deployment is configured
	// and wants nothing set up for it. Recognising only the environment
	// variable would make the file - the form this project recommends - look
	// like an uninitialised install.
	if os.Getenv("SHINYHUB_AUTH_SECRET") != "" || os.Getenv("SHINYHUB_AUTH_SECRET_FILE") != "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect server config %s: %w", path, err)
	}

	if !setupIsStdinTTY() {
		// An uninitialised install is a setup state with a documented remedy,
		// not a fault: telling a supervisor this start is worth retrying would
		// be wrong, since it never clears without someone running init.
		return nil, &shinycli.ExitCodeError{
			Code: 1,
			Kind: shinycli.KindValidation,
			Err: fmt.Errorf("ShinyHub is not initialized: run `shinyhub init` in a terminal, then `shinyhub serve`; " +
				"for unattended setup, set SHINYHUB_AUTH_SECRET_FILE (or SHINYHUB_AUTH_SECRET), SHINYHUB_ADMIN_USER, and SHINYHUB_ADMIN_PASSWORD"),
		}
	}

	w := cmd.ErrOrStderr()
	fmt.Fprintln(w, "Welcome to ShinyHub")
	fmt.Fprintln(w, "Let's create your local administrator. This takes less than a minute.")
	fmt.Fprintln(w)
	result, err := runSetup(cmd, &setupFlags{configPath: path})
	if err != nil {
		return nil, err
	}
	printServeSetupResult(w, result)
	return &result, nil
}

func runSetup(cmd *cobra.Command, f *setupFlags) (setupResult, error) {
	path := setupConfigPath(f.configPath)
	result := setupResult{ConfigPath: path}

	_, statErr := os.Stat(path)
	configExists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return result, fmt.Errorf("inspect server config %s: %w", path, statErr)
	}

	if !configExists {
		maintenanceCfg, err := config.LoadForMaintenance(path)
		if err != nil {
			return result, fmt.Errorf("prepare defaults: %w", err)
		}
		if os.Getenv("SHINYHUB_AUTH_SECRET") == "" && sqliteDatabaseExists(maintenanceCfg.Database.DSN) {
			return result, setupValidationErr(
				"set SHINYHUB_AUTH_SECRET to the secret this database was created with, or move the database aside to start a fresh install",
				"found an existing database at %s but no server config or SHINYHUB_AUTH_SECRET; refusing to generate a replacement secret because existing encrypted data may depend on the original one", maintenanceCfg.Database.DSN)
		}

		secret := os.Getenv("SHINYHUB_AUTH_SECRET")
		if secret == "" {
			secret, err = generateSetupSecret()
			if err != nil {
				return result, err
			}
		}
		if err := validateSetupSecret(secret); err != nil {
			return result, err
		}
		if err := writeInitialConfig(path, secret); err != nil {
			return result, err
		}
		result.CreatedConfig = true
	}

	cfg, err := config.Load(path)
	if err != nil {
		return result, fmt.Errorf("load config: %w", err)
	}
	result.DatabaseDSN = cfg.Database.DSN
	result.URL = setupServerURL(cfg)

	if !cfg.Auth.LocalLoginEnabled() {
		if cfg.HasSSOLoginPath() {
			return result, nil
		}
		return result, setupValidationErr(
			"set auth.local_login: true so setup can create a local administrator, or configure an SSO provider (GitHub, Google, OIDC, or forward-auth)",
			"local login is disabled and no SSO login path is configured")
	}

	if err := prepareSetupDatabaseDir(cfg.Database.DSN); err != nil {
		return result, err
	}
	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		return result, fmt.Errorf("open database: %w", err)
	}
	defer store.Close()

	// A first migration contains dozens of useful-but-noisy informational lines.
	// Setup presents one meaningful progress result instead; normal server startup
	// retains the migration logs.
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	err = store.Migrate()
	slog.SetDefault(previousLogger)
	if err != nil {
		return result, fmt.Errorf("prepare database: %w", err)
	}
	if path, ok := setupSQLiteFilePath(cfg.Database.DSN); ok {
		if err := os.Chmod(path, 0o600); err != nil {
			return result, fmt.Errorf("protect database file: %w", err)
		}
	}

	users, err := store.ListUsers()
	if err != nil {
		return result, fmt.Errorf("inspect users: %w", err)
	}
	for _, user := range users {
		if user.PrincipalType != "service_account" && user.Role == "admin" && db.HasLocalPassword(user.PasswordHash) {
			result.Username = user.Username
			return result, nil
		}
	}

	username, password, err := setupCredentials(cmd, f)
	if err != nil {
		return result, err
	}
	if _, err := store.GetUserByUsername(username); err == nil {
		return result, setupValidationErr(
			adminNameHint+" with a different name, or give the existing user the admin role and a local password",
			"user %q already exists but is not a usable local administrator", username)
	} else if !errors.Is(err, db.ErrNotFound) {
		return result, fmt.Errorf("check administrator username: %w", err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return result, fmt.Errorf("hash administrator password: %w", err)
	}
	if err := store.CreateUser(db.CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		Role:         "admin",
	}); err != nil {
		return result, fmt.Errorf("create administrator: %w", err)
	}
	result.Username = username
	result.CreatedAdmin = true
	return result, nil
}

func setupCredentials(cmd *cobra.Command, f *setupFlags) (string, string, error) {
	username := strings.TrimSpace(f.adminUser)
	if username == "" {
		username = strings.TrimSpace(os.Getenv("SHINYHUB_ADMIN_USER"))
	}

	password := ""
	if f.adminPasswordFile != "" {
		b, err := os.ReadFile(f.adminPasswordFile)
		if err != nil {
			return "", "", fmt.Errorf("read administrator password file: %w", err)
		}
		password = strings.TrimRight(string(b), "\r\n")
	} else {
		password = os.Getenv("SHINYHUB_ADMIN_PASSWORD")
	}

	if !setupIsStdinTTY() {
		if username == "" || password == "" {
			return "", "", setupValidationErr(
				"set SHINYHUB_ADMIN_USER and SHINYHUB_ADMIN_PASSWORD, or pass --admin-user and --admin-password-file",
				"administrator credentials are required for unattended setup")
		}
		if err := validateSetupUsername(username); err != nil {
			return "", "", err
		}
		if err := validateSetupPassword(password); err != nil {
			return "", "", err
		}
		return username, password, nil
	}

	reader := bufio.NewReader(cmd.InOrStdin())
	for username == "" {
		line, err := promptSetupLine(reader, cmd.ErrOrStderr(), "Administrator username [admin]: ")
		if err != nil {
			return "", "", fmt.Errorf("read administrator username: %w", err)
		}
		if strings.TrimSpace(line) == "" {
			line = "admin"
		}
		if err := validateSetupUsername(line); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%v\n", err)
			continue
		}
		username = line
	}
	if err := validateSetupUsername(username); err != nil {
		return "", "", err
	}

	if password != "" {
		if err := validateSetupPassword(password); err != nil {
			return "", "", err
		}
		return username, password, nil
	}

	// The length requirement goes in the prompt, not only in the rejection.
	// A minimum a person learns by having their first choice refused reads as
	// an arbitrary obstacle; one stated up front is just the rule.
	passwordPrompt := fmt.Sprintf("Administrator password (at least %d characters): ", auth.MinPasswordLength)
	for {
		first, err := promptSetupPassword(cmd.ErrOrStderr(), passwordPrompt)
		if err != nil {
			return "", "", fmt.Errorf("read administrator password: %w", err)
		}
		if err := validateSetupPassword(first); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%v\n", err)
			continue
		}
		confirm, err := promptSetupPassword(cmd.ErrOrStderr(), "Confirm password: ")
		if err != nil {
			return "", "", fmt.Errorf("confirm administrator password: %w", err)
		}
		if first != confirm {
			fmt.Fprintln(cmd.ErrOrStderr(), "Passwords do not match. Try again.")
			continue
		}
		return username, first, nil
	}
}

// bootstrapAdminFromEnv creates the administrator named by SHINYHUB_ADMIN_USER
// when that username is not taken yet. It is the unattended counterpart to
// `shinyhub init`, and it only ever creates: an existing username is left
// exactly as it is, password included.
func bootstrapAdminFromEnv(store *db.Store, localLoginEnabled bool, logger *slog.Logger) error {
	adminUser := os.Getenv("SHINYHUB_ADMIN_USER")
	if adminUser == "" {
		return nil
	}
	adminPass := os.Getenv("SHINYHUB_ADMIN_PASSWORD")
	if adminPass == "" {
		return fmt.Errorf("SHINYHUB_ADMIN_PASSWORD must not be empty when SHINYHUB_ADMIN_USER is set")
	}
	if !localLoginEnabled {
		logger.Warn("SHINYHUB_ADMIN_USER is set but local login is disabled (auth.local_login: false); this admin cannot sign in with a password - grant admin via SSO (e.g. group_role_mappings) instead",
			"username", adminUser)
	}
	// `shinyhub init` refuses a password below the minimum, and so does every
	// password set through the API. This path accepts anything, which is what
	// makes a local dev instance possible - .air.toml bootstraps admin/admin.
	// Failing here would break that, so it warns instead: an operator
	// bootstrapping a real deployment is told that the password they just set is
	// one their own users would be refused, rather than finding out when
	// somebody guesses it.
	if err := auth.ValidateNewPassword(adminPass); err != nil {
		logger.Warn("SHINYHUB_ADMIN_PASSWORD is weaker than ShinyHub accepts anywhere else; change it before this deployment is reachable by anyone else",
			"username", adminUser, "reason", err.Error(), "minimum_characters", auth.MinPasswordLength)
	}
	_, err := store.GetUserByUsername(adminUser)
	switch {
	case errors.Is(err, db.ErrNotFound):
		hash, err := auth.HashPassword(adminPass)
		if err != nil {
			return fmt.Errorf("hash admin password: %w", err)
		}
		if err := store.CreateUser(db.CreateUserParams{
			Username:     adminUser,
			PasswordHash: hash,
			Role:         "admin",
		}); err != nil {
			logger.Warn("could not create admin user", "err", err)
		} else {
			logger.Info("admin user created", "username", adminUser)
		}
	case err != nil:
		return fmt.Errorf("check admin user: %w", err)
	default:
		// Bootstrap creates, it never resets. Silence here reads as "the
		// password in my environment is the password that works", and the login
		// that then fails looks like a broken build rather than a credential
		// this path was never going to change.
		logger.Info("admin user already exists; SHINYHUB_ADMIN_PASSWORD was not applied and the stored password is unchanged",
			"username", adminUser)
	}
	return nil
}

func ensureUsableFirstLogin(cfg *config.Config, store *db.Store, configPath string) error {
	users, err := store.ListUsers()
	if err != nil {
		return fmt.Errorf("inspect users: %w", err)
	}
	for _, user := range users {
		if user.PrincipalType != "service_account" && user.Role == "admin" && db.HasLocalPassword(user.PasswordHash) {
			return nil
		}
	}
	if !cfg.Auth.LocalLoginEnabled() || cfg.HasSSOLoginPath() {
		return nil
	}
	// A server with no administrator is a known, actionable setup state with a
	// documented remedy, not an unexpected fault. Classifying it as "internal"
	// tells a supervisor the start is worth retrying, which it never is until
	// someone runs init.
	initCmd := "shinyhub init"
	if configPath != defaultServerConfigPath {
		initCmd = fmt.Sprintf("shinyhub init --config %s", shellQuote(configPath))
	}
	return &shinycli.ExitCodeError{
		Code: 1,
		Kind: shinycli.KindValidation,
		Err:  fmt.Errorf("no usable local administrator exists; run `%s` to create one, then start the server again", initCmd),
	}
}

func setupConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if path := os.Getenv("SHINYHUB_CONFIG"); path != "" {
		return path
	}
	return defaultServerConfigPath
}

func generateSetupSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate authentication secret: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// setupValidationErr marks a setup failure the operator fixes by supplying
// something different: a flag, an environment variable, a longer password. The
// same reasoning as the uninitialised-install path above applies to all of
// them. Reporting one as "internal" tells a supervisor the run is worth
// retrying unchanged, and none of these ever clears on its own.
//
// hint comes first because the message takes the format arguments. It carries
// the remedy and nothing else: the envelope renders hint as its own field, so a
// remedy folded into the message reaches a reader who renders the two
// separately as an empty hint beside a sentence that does too much work.
func setupValidationErr(hint, format string, args ...any) error {
	return shinycli.ValidationError(fmt.Sprintf(format, args...), hint)
}

// secretHint is shared by both auth-secret failures: either way the operator
// has supplied a secret ShinyHub will not accept, and both ways out are the
// same two.
const secretHint = "unset SHINYHUB_AUTH_SECRET to have setup generate one, or set it to at least 32 random characters (openssl rand -hex 32)"

// adminNameHint names both ways of supplying the administrator username, so a
// reader who reached setup through the environment is not told only about the
// flag, or the other way round.
const adminNameHint = "pass --admin-user or set SHINYHUB_ADMIN_USER"

func validateSetupSecret(secret string) error {
	switch {
	case secret == "change-me-to-a-random-string":
		return setupValidationErr(secretHint, "SHINYHUB_AUTH_SECRET is the placeholder value")
	case len(secret) < 32:
		return setupValidationErr(secretHint, "SHINYHUB_AUTH_SECRET must be at least 32 characters (got %d)", len(secret))
	}
	return nil
}

func validateSetupUsername(username string) error {
	if username == "" {
		return setupValidationErr(adminNameHint, "administrator username cannot be empty")
	}
	if username != strings.TrimSpace(username) || strings.ContainsAny(username, "\t\r\n ") {
		return setupValidationErr(adminNameHint+" with a name that has no spaces or tabs in it",
			"administrator username cannot contain whitespace")
	}
	if len(username) > 128 {
		return setupValidationErr(adminNameHint+" with a shorter name",
			"administrator username must be 128 characters or fewer")
	}
	if db.IsSystemUser(username) {
		return setupValidationErr("ShinyHub keeps this name for one of its own system accounts; "+adminNameHint+" with a different name",
			"administrator username %q is reserved", username)
	}
	return nil
}

func validateSetupPassword(password string) error {
	if err := auth.ValidateNewPassword(password); err != nil {
		// %s, not %w: nothing matches on the policy error, and the envelope's
		// message is a sentence rather than a wrap chain.
		return setupValidationErr(
			fmt.Sprintf("supply a password of %d to 72 characters in SHINYHUB_ADMIN_PASSWORD or --admin-password-file", auth.MinPasswordLength),
			"administrator %s", err)
	}
	return nil
}

func writeInitialConfig(path, secret string) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("server config %s appeared during setup; nothing was overwritten", path)
		}
		return fmt.Errorf("create server config %s: %w", path, err)
	}
	removeOnError := true
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close server config: %w", closeErr)
		}
		if err != nil && removeOnError {
			_ = os.Remove(path)
		}
	}()

	content := "# Generated by `shinyhub init`. Keep this file private.\n" +
		"server:\n" +
		"  # First-run installations are local-only until you deliberately change this.\n" +
		"  host: \"127.0.0.1\"\n" +
		"auth:\n" +
		fmt.Sprintf("  secret: %q\n", secret)
	if _, err = io.WriteString(f, content); err != nil {
		return fmt.Errorf("write server config: %w", err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync server config: %w", err)
	}
	removeOnError = false
	return nil
}

func prepareSetupDatabaseDir(dsn string) error {
	path, ok := setupSQLiteFilePath(dsn)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	return nil
}

func sqliteDatabaseExists(dsn string) bool {
	path, ok := setupSQLiteFilePath(dsn)
	if !ok {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func setupSQLiteFilePath(dsn string) (string, bool) {
	if db.IsPostgresDSN(dsn) || strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory") {
		return "", false
	}
	path := strings.TrimPrefix(strings.SplitN(dsn, "?", 2)[0], "file:")
	return path, path != ""
}

func setupServerURL(cfg *config.Config) string {
	if cfg.Server.BaseURL != "" {
		return strings.TrimRight(cfg.Server.BaseURL, "/")
	}
	host := cfg.Server.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%d", host, cfg.Server.Port)
}

func promptSetupLine(r *bufio.Reader, w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", io.EOF
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func promptSetupPassword(w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	password, err := setupReadPassword()
	fmt.Fprintln(w)
	return password, err
}

func printServeSetupResult(w io.Writer, result setupResult) {
	if result.CreatedConfig {
		fmt.Fprintf(w, "✓ Private configuration created at %s\n", result.ConfigPath)
	} else {
		fmt.Fprintf(w, "✓ Configuration ready at %s\n", result.ConfigPath)
	}
	if result.CreatedAdmin {
		fmt.Fprintf(w, "✓ Administrator %q created\n", result.Username)
	} else if result.Username != "" {
		fmt.Fprintf(w, "✓ Existing user %q found; no accounts changed\n", result.Username)
	} else {
		fmt.Fprintln(w, "✓ SSO configuration ready; your identity provider will create users at sign-in")
	}
	if result.DatabaseDSN != "" {
		fmt.Fprintf(w, "✓ Data will be stored in %s\n", result.DatabaseDSN)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Starting ShinyHub. Open %s when the server is ready.\n\n", result.URL)
}

func setupResultProse(result setupResult) string {
	var lines []string
	if result.CreatedConfig {
		lines = append(lines, fmt.Sprintf("Private configuration created at %s", result.ConfigPath))
	} else {
		lines = append(lines, fmt.Sprintf("Configuration ready at %s", result.ConfigPath))
	}
	if result.CreatedAdmin {
		lines = append(lines, fmt.Sprintf("Administrator %q created", result.Username))
	} else if result.Username != "" {
		lines = append(lines, fmt.Sprintf("Existing user %q found; no accounts changed", result.Username))
	} else {
		lines = append(lines, "SSO configuration ready; your identity provider will create users at sign-in")
	}
	if result.DatabaseDSN != "" {
		lines = append(lines, fmt.Sprintf("Data will be stored in %s", result.DatabaseDSN))
	}
	if result.ConfigPath == defaultServerConfigPath {
		lines = append(lines, fmt.Sprintf("Ready. Run `shinyhub serve`, then open %s", result.URL))
	} else {
		lines = append(lines, fmt.Sprintf("Ready. Run `shinyhub serve --config %s`, then open %s", shellQuote(result.ConfigPath), result.URL))
	}
	return strings.Join(lines, "\n")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
