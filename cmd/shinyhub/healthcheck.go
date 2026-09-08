package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/cli"
	"github.com/spf13/cobra"
)

const (
	defaultHealthcheckURL     = "http://127.0.0.1:8080/readyz"
	defaultHealthcheckTimeout = 3 * time.Second
	maxHealthcheckBody        = 4 << 10
)

// newHealthcheckCmd provides a readiness probe that works in the distroless
// server image, where shell-based curl/wget health checks are unavailable.
func newHealthcheckCmd() *cobra.Command {
	var target string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Exit successfully when a ShinyHub server is ready",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// --host is a persistent flag registered on the shared root command
			// for the developer subcommands (login, apps, deploy, ...), which
			// resolve it against the saved multi-host credentials store.
			// healthcheck is a standalone readiness probe with no credentials
			// and no saved hosts: it never reads hostFlagOverride, so accepting
			// --host here would silently do nothing. Reject it loudly instead of
			// letting the operator believe it pointed the probe somewhere.
			if cmd.Flags().Changed("host") {
				return cli.ValidationError(
					"--host has no effect on healthcheck",
					"use --url to point healthcheck at a specific readiness endpoint",
				)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			client := &http.Client{
				Timeout: timeout,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			}
			return checkReady(ctx, client, target)
		},
	}
	cmd.Flags().StringVar(&target, "url", defaultHealthcheckURL, "Readiness endpoint URL")
	cmd.Flags().DurationVar(&timeout, "timeout", defaultHealthcheckTimeout, "Readiness request timeout")
	return cmd
}

func checkReady(ctx context.Context, client *http.Client, target string) error {
	u, err := url.ParseRequestURI(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid healthcheck URL %q: expected an absolute http:// or https:// URL", target)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("create readiness request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ShinyHub is not ready at %s: %w", target, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHealthcheckBody))
	if readErr != nil {
		return fmt.Errorf("read readiness response from %s: %w", target, readErr)
	}
	if resp.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = "empty response"
		}
		return fmt.Errorf("ShinyHub is not ready at %s: %s (%s)", target, resp.Status, detail)
	}
	return nil
}
