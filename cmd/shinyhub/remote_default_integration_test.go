//go:build integration

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestRemoteDefaultTierServesReadiness(t *testing.T) {
	dbtest.RequirePostgres(t) // shares the real CLI build provisioned by TestMain
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			root := t.TempDir()
			dsn := filepath.Join(root, "state.db")
			if backend == "postgres" {
				_, dsn = dbtest.NewPostgres(t)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			joinToken := filepath.Join(root, "join-token")
			if err := os.WriteFile(joinToken, []byte("remote-default-test-join-token-0123456789"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := fmt.Sprintf(`server:
  host: 127.0.0.1
  port: %d
database:
  driver: %s
  dsn: %q
storage:
  apps_dir: %q
  app_data_dir: %q
auth:
  secret: remote-default-test-secret-0123456789
runtime:
  tiers:
    - name: workers
      runtime: remote_docker
worker:
  enabled: true
  join_token_file: %q
  ca_dir: %q
  listen_addr: 127.0.0.1:0
  advertise_hosts: [127.0.0.1]
`, port, backend, dsn, filepath.Join(root, "apps"), filepath.Join(root, "app-data"), joinToken, filepath.Join(root, "ca"))
			cfgPath := filepath.Join(root, "server.yaml")
			if err := os.WriteFile(cfgPath, []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(root, "serve.log")
			log, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, testBin, "serve", "--config", cfgPath, "--no-browser")
			cmd.Stdout, cmd.Stderr = log, log
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "SHINYHUB_") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env,
				"SHINYHUB_ADMIN_USER=remote-default-admin",
				"SHINYHUB_ADMIN_PASSWORD=remote-default-test-password",
			)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			t.Cleanup(func() {
				cancel()
				<-done
			})
			client := &http.Client{Timeout: time.Second}
			for {
				select {
				case err := <-done:
					done <- err // leave cleanup able to join the exited process
					body, _ := os.ReadFile(logPath)
					t.Fatalf("serve exited before readiness: %v\n%s", err, body)
				default:
				}
				response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/readyz", port))
				if err == nil {
					response.Body.Close()
					if response.StatusCode == http.StatusOK {
						return
					}
				}
				time.Sleep(25 * time.Millisecond)
			}
		})
	}
}
