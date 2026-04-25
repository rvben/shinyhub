package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with a ShinyHub server",
	RunE:  runLogin,
}

var loginFlags struct {
	host     string
	token    string
	username string
	password string
}

func init() {
	loginCmd.Flags().StringVar(&loginFlags.host, "host", "", "ShinyHub server URL (e.g. https://shiny.example.com)")
	loginCmd.Flags().StringVar(&loginFlags.token, "token", "", "API token (skips username/password)")
	loginCmd.Flags().StringVar(&loginFlags.username, "username", "", "Username")
	loginCmd.Flags().StringVar(&loginFlags.password, "password", "", "Password")
	loginCmd.MarkFlagRequired("host")
}

func runLogin(cmd *cobra.Command, args []string) error {
	f := loginFlags
	if f.token != "" {
		// Verify the token is accepted by the server before persisting it.
		if err := verifyToken(f.host, f.token); err != nil {
			return fmt.Errorf("token rejected by server: %w", err)
		}
		if err := saveConfig(&cliConfig{Host: f.host, Token: f.token}); err != nil {
			return err
		}
		fmt.Printf("Logged in. Saved credentials to %s\n", configPath())
		return nil
	}

	body, _ := json.Marshal(map[string]string{"username": f.username, "password": f.password})
	resp, err := http.Post(f.host+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("login failed: %s", resp.Status)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}
	if result.Token == "" {
		return fmt.Errorf("server returned empty token")
	}
	if err := saveConfig(&cliConfig{Host: f.host, Token: result.Token}); err != nil {
		return err
	}
	fmt.Printf("Logged in. Saved credentials to %s\n", configPath())
	return nil
}

// verifyToken does a GET /api/auth/me round-trip to confirm the token is
// accepted by the server before it is persisted to the config file.
func verifyToken(host, token string) error {
	req, err := http.NewRequest("GET", host+"/api/auth/me", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %s: %s", resp.Status, out)
	}
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %s: %s", resp.Status, out)
	}
	return nil
}
