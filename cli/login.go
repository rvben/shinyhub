package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with a ShinyHost server",
	RunE:  runLogin,
}

var loginFlags struct {
	host     string
	token    string
	username string
	password string
}

func init() {
	loginCmd.Flags().StringVar(&loginFlags.host, "host", "", "ShinyHost server URL (e.g. https://shiny.example.com)")
	loginCmd.Flags().StringVar(&loginFlags.token, "token", "", "API token (skips username/password)")
	loginCmd.Flags().StringVar(&loginFlags.username, "username", "", "Username")
	loginCmd.Flags().StringVar(&loginFlags.password, "password", "", "Password")
	loginCmd.MarkFlagRequired("host")
}

func runLogin(cmd *cobra.Command, args []string) error {
	f := loginFlags
	if f.token != "" {
		return saveConfig(&cliConfig{Host: f.host, Token: f.token})
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
	json.NewDecoder(resp.Body).Decode(&result)
	if err := saveConfig(&cliConfig{Host: f.host, Token: result.Token}); err != nil {
		return err
	}
	fmt.Println("Logged in successfully.")
	return nil
}
