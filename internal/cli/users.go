package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"
)

// validGlobalRoles mirrors the server's auth.IsValidGlobalRole set. The CLI
// validates client-side so a bad --role fails fast with a clear message instead
// of a round-trip 400.
var validGlobalRoles = []string{"viewer", "developer", "operator", "admin"}

func validateRole(role string) error {
	for _, r := range validGlobalRoles {
		if role == r {
			return nil
		}
	}
	return validationErr(
		fmt.Sprintf("invalid role %q", role),
		"--role must be one of: "+joinComma(validGlobalRoles))
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// newUsersCmd builds a fresh users command tree each time it is called. The
// subcommands wrap the admin-only /api/users CRUD endpoints so a CLI-first
// operator can onboard and manage teammates without curl or the web UI.
func newUsersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "users", Short: "Manage user accounts (admin)"}
	cmd.AddCommand(
		newUsersListCmd(),
		newUsersCreateCmd(),
		newUsersSetRoleCmd(),
		newUsersResetPasswordCmd(),
		newUsersRevokeSessionsCmd(),
		newUsersDeleteCmd(),
	)
	return cmd
}

func newUsersRevokeSessionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke-sessions <username>",
		Short: "Sign a user out of every active session",
		Long: "Sign a user out of every active session.\n\n" +
			"Invalidates all of the user's outstanding session tokens immediately\n" +
			"(they must log in again). Use when a session may be compromised or a\n" +
			"leaver's browser is still signed in. API tokens are separate\n" +
			"credentials: list them with `shinyhub tokens list --all` and revoke\n" +
			"individually.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			username := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			id, err := lookupUserID(cfg, username)
			if err != nil {
				return err
			}
			if _, err := doAPI(cfg, apiCall{Method: "POST", Path: "/api/users/" + strconv.FormatInt(id, 10) + "/revoke-sessions", Op: "revoke sessions"}); err != nil {
				return err
			}
			return renderAction(cmd, "revoked",
				map[string]any{"username": username},
				fmt.Sprintf("%s: all sessions revoked", username))
		},
	}
}

// lookupUserID resolves a username to its numeric id via GET /api/users/{username}
// so the by-id PATCH/DELETE endpoints can target a user the operator named.
func lookupUserID(cfg *cliConfig, username string) (int64, error) {
	res, err := doAPI(cfg, apiCall{Method: "GET", Path: "/api/users/" + username, Op: "look up user"})
	if err != nil {
		return 0, err
	}
	out := res.Body
	var u struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(out, &u); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	return u.ID, nil
}

func newUsersListCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List all user accounts",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
	}
	addListFlags(cmd, f)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		// The server orders users by username and paginates server-side.
		users, total, err := getPaginatedList(cfg, "list users", "/api/users", f)
		if err != nil {
			return err
		}
		return renderServerList(cmd, f, users, total, nil, func(w io.Writer, items []map[string]any) {
			if len(items) == 0 {
				fmt.Fprintln(w, "No users.")
				return
			}
			t := newTable("ID", "USERNAME", "ROLE", "CREATED").alignRight(0)
			for _, u := range items {
				t.row(dimTxt(u["id"]), txt(u["username"]), txt(u["role"]), dimTxt(u["created_at"]))
			}
			t.render(w)
		})
	}
	return cmd
}

func newUsersCreateCmd() *cobra.Command {
	var username, password, role string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a user account",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&username, "username", "", "Username (required)")
	cmd.Flags().StringVar(&password, "password", "", "Password, at least 15 characters (required)")
	cmd.Flags().StringVar(&role, "role", "developer", "Role: viewer, developer, operator, or admin")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("password")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateRole(role); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]any{"username": username, "password": password, "role": role})
		res, err := doAPI(cfg, apiCall{Method: "POST", Path: "/api/users", Body: body, Op: "create user"})
		if err != nil {
			return err
		}
		out := res.Body
		var u struct {
			ID   int64  `json:"id"`
			Role string `json:"role"`
		}
		_ = json.Unmarshal(out, &u)
		return renderAction(cmd, "created",
			map[string]any{"id": u.ID, "username": username, "role": u.Role},
			fmt.Sprintf("created user %q (id %d, role %s). Share with them:\n  shinyhub login --host %s --username %s",
				username, u.ID, u.Role, cfg.Host, username))
	}
	return cmd
}

func newUsersSetRoleCmd() *cobra.Command {
	var role string
	cmd := &cobra.Command{
		Use:   "set-role <username>",
		Short: "Change a user's role",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&role, "role", "", "New role: viewer, developer, operator, or admin (required)")
	_ = cmd.MarkFlagRequired("role")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		username := args[0]
		if err := validateRole(role); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		id, err := lookupUserID(cfg, username)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]any{"role": role})
		if _, err := doAPI(cfg, apiCall{Method: "PATCH", Path: "/api/users/" + strconv.FormatInt(id, 10), Body: body, Op: "set user role"}); err != nil {
			return err
		}
		return renderAction(cmd, "role_updated",
			map[string]any{"id": id, "username": username, "role": role},
			fmt.Sprintf("set %q role to %s", username, role))
	}
	return cmd
}

func newUsersResetPasswordCmd() *cobra.Command {
	var password string
	cmd := &cobra.Command{
		Use:   "reset-password <username>",
		Short: "Reset a user's password",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&password, "password", "", "New password, at least 15 characters (required)")
	_ = cmd.MarkFlagRequired("password")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		username := args[0]
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		id, err := lookupUserID(cfg, username)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]any{"password": password})
		if _, err := doAPI(cfg, apiCall{Method: "PATCH", Path: "/api/users/" + strconv.FormatInt(id, 10) + "/password", Body: body, Op: "reset password"}); err != nil {
			return err
		}
		return renderAction(cmd, "password_reset",
			map[string]any{"id": id, "username": username},
			fmt.Sprintf("reset password for %q", username))
	}
	return cmd
}

func newUsersDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <username>",
		Short:   "Permanently delete a user account",
		Aliases: []string{"rm"},
		Args:    cobra.ExactArgs(1),
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		username := args[0]
		if !yes {
			// Mirror `apps delete`: refuse on a non-TTY without --yes so
			// automation gets a clear, actionable error; prompt on a TTY.
			if !isStdinTTY() {
				return confirmationRequiredError(
					"users delete requires interactive confirmation", "--yes")
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "This will permanently delete user %q. Type the username to confirm: ", username)
			var confirm string
			if _, err := fmt.Fscan(cmd.InOrStdin(), &confirm); err != nil {
				return fmt.Errorf("read confirmation: %w", err)
			}
			if confirm != username {
				return fmt.Errorf("confirmation did not match username %q - aborted", username)
			}
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		id, err := lookupUserID(cfg, username)
		if err != nil {
			return err
		}
		if _, err := doAPI(cfg, apiCall{Method: "DELETE", Path: "/api/users/" + strconv.FormatInt(id, 10), Op: "delete user"}); err != nil {
			return err
		}
		return renderAction(cmd, "deleted",
			map[string]any{"id": id, "username": username},
			fmt.Sprintf("deleted user %q", username))
	}
	return cmd
}
