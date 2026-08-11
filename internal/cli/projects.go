package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/appmetaspec"
	"github.com/rvben/shinyhub/internal/deploy"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
	"github.com/spf13/cobra"
)

func newProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projects",
		Short: "Manage app projects (grouping)",
		Long: "Projects group apps on the dashboard. An app names its project with\n" +
			"`shinyhub apps set <slug> --project <project>`, or with `project = \"...\"`\n" +
			"in its bundle manifest. A project referenced by an app exists whether or\n" +
			"not it has a display name; these commands manage the display metadata.",
	}
	cmd.AddCommand(newProjectsListCmd(), newProjectsSetCmd(), newProjectsRmCmd())
	return cmd
}

func newProjectsListCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects visible to you",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			items, total, err := getPaginatedList(cfg, "list projects", "/api/projects", f)
			if err != nil {
				return err
			}
			// renderServerList, not renderList: the server already applied
			// limit/offset (getPaginatedList forwards them), so renderList would
			// slice the page a second time and report len(page) as the total.
			// Passing total through extra does not help - listEnvelope
			// (listout.go:99-115) drops a colliding "total" key by design.
			return renderServerList(cmd, f, items, total, nil, func(w io.Writer, items []map[string]any) {
				if len(items) == 0 {
					fmt.Fprintln(w, "No projects yet. Run `shinyhub projects set <slug> --name \"...\"` to create one.")
					return
				}
				t := newTable("SLUG", "NAME", "APPS", "ICON").alignRight(2)
				for _, it := range items {
					// jsonInt (internal/cli/manifest_summary.go:136) reads the
					// float64 a JSON number decodes to. app_count is scoped to
					// the caller: it counts the apps THIS user can see.
					t.row(txt(it["slug"]), txt(it["name"]),
						txt(jsonInt(it["app_count"])), txt(it["icon_emoji"]))
				}
				t.render(w)
			})
		},
	}
	addListFlags(cmd, f)
	return cmd
}

type projectsSetFlags struct {
	name        string
	description string
	icon        string
}

func newProjectsSetCmd() *cobra.Command {
	f := &projectsSetFlags{}
	cmd := &cobra.Command{
		Use:   "set <slug>",
		Short: "Create or update a project's display metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectsSet(cmd, args[0], f)
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "",
		"Display name shown as the group heading (up to 128 characters; \"\" falls back to the slug)")
	cmd.Flags().StringVar(&f.description, "description", "",
		"One-line description of the project (up to 280 characters; \"\" clears it)")
	cmd.Flags().StringVar(&f.icon, "icon", "",
		"Emoji shown beside the group heading (a single emoji; \"\" clears it)")
	return cmd
}

func runProjectsSet(cmd *cobra.Command, slug string, f *projectsSetFlags) error {
	slug = strings.TrimSpace(slug)
	if !slugpkg.Valid(slug) {
		return validationErr("project slug must be "+slugpkg.HumanRule, "")
	}
	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		v, err := appmetaspec.NormalizeProjectName(f.name)
		if err != nil {
			return validationErr(err.Error(), "pass --name \"\" to fall back to the slug")
		}
		body["name"] = v
	}
	if cmd.Flags().Changed("description") {
		v, err := appmetaspec.NormalizeDescription(f.description)
		if err != nil {
			return validationErr(err.Error(), "pass --description \"\" to clear it")
		}
		body["description"] = v
	}
	if cmd.Flags().Changed("icon") {
		v := strings.TrimSpace(f.icon)
		if v != "" {
			if err := deploy.ValidateIconEmoji(v); err != nil {
				return validationErr(err.Error(), "pass --icon \"\" to clear it")
			}
		}
		body["icon_emoji"] = v
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	// Upsert shape: PATCH the existing project, and fall back to POST when it
	// does not exist yet, so one command covers both without the caller having
	// to know. POST carries the same metadata, so the create is not a two-step
	// "create bare then name it" that would leave a half-named project behind
	// if the second call failed.
	resp, respBody, err := projectJSON(cfg, http.MethodPatch, "/api/projects/"+slug, body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		create := map[string]any{"slug": slug}
		for k, v := range body {
			create[k] = v
		}
		resp, respBody, err = projectJSON(cfg, http.MethodPost, "/api/projects", create)
		if err != nil {
			return err
		}
	}
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "update project", resp, respBody)
	}
	status := "updated"
	if resp.StatusCode == http.StatusCreated {
		status = "created"
	}
	return renderAction(cmd, status, map[string]any{"slug": slug},
		fmt.Sprintf("project %s %s", slug, status))
}

func newProjectsRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <slug>",
		Short: "Delete a project's display metadata",
		Long: "Deletes the project. Refused while any app still names it: move those\n" +
			"apps first with `shinyhub apps set <app> --project <other>` or clear\n" +
			"them with `--project \"\"`. Deleting never touches apps.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			resp, body, err := projectJSON(cfg, http.MethodDelete, "/api/projects/"+args[0], nil)
			if err != nil {
				return err
			}
			if resp.StatusCode >= 400 {
				return httpError(cfg.Token, "delete project", resp, body)
			}
			return renderAction(cmd, "deleted", map[string]any{"slug": args[0]},
				fmt.Sprintf("project %s deleted", args[0]))
		},
	}
	return cmd
}

// projectJSON issues one JSON request and returns the response plus its already
// drained body, so callers can branch on the status code. The package has no
// shared JSON-mutation helper - every mutating command builds its own request
// (see share.go:90-106) - and `projects set` needs the status code twice (404 to
// fall back to POST, 201 to report "created" rather than "updated"), which is
// why this one is worth extracting. body may be nil for DELETE.
func projectJSON(cfg *cliConfig, method, path string, body map[string]any) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal request: %w", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, cfg.Host+path, rdr)
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out, nil
}
