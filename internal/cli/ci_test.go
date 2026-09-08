package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

type ciRoundTrip func(*http.Request) (*http.Response, error)

func (f ciRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func ciTestTransport(t *testing.T, f ciRoundTrip) {
	t.Helper()
	old := httpClient.Transport
	httpClient.Transport = f
	t.Cleanup(func() { httpClient.Transport = old })
}
func ciResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func ciCredential() string {
	return fmt.Sprintf(`{"token":"shk_fake_deployment","token_type":"Token","expires_at":%q}`, time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339))
}

func TestCIExchangeAndChild(t *testing.T) {
	credentials := isolatedCredentials(t)
	ciTestTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://hub.example/api/auth/trusted-publishing" || r.Method != "POST" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected exchange: %s %s", r.Method, r.URL)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["identity_token"] != "fake-identity" || body["policy"] != "production" {
			t.Fatalf("body=%v", body)
		}
		return ciResponse(201, ciCredential()), nil
	})
	root := &cobra.Command{Use: "shinyhub", SilenceErrors: true}
	AddCommandsTo(root)
	existing, _, _ := root.Find([]string{"ci"})
	root.RemoveCommand(existing)
	ran := false
	root.AddCommand(newCICommand(func(cmd *cobra.Command, args []string, host, token string) error {
		ran = true
		if host != "https://hub.example" || token != "shk_fake_deployment" {
			t.Fatal("wrong child credentials")
		}
		if !reflect.DeepEqual(args, []string{"--host", "https://hub.example", "--output=json", "plan", ".", "--detailed-exitcode"}) {
			t.Fatalf("args=%q", args)
		}
		return &ciChildExit{&ExitCodeError{Code: 2}}
	}))
	root.SetIn(strings.NewReader("fake-identity\n"))
	root.SetArgs([]string{"ci", "--host", "https://hub.example/", "--policy", "production", "--identity-token-file", "-", "--output", "json", "--", "plan", ".", "--detailed-exitcode"})
	err := root.Execute()
	if !ran || ExitCode(err) != 2 {
		t.Fatalf("ran=%v err=%v exit=%d", ran, err, ExitCode(err))
	}
	var out bytes.Buffer
	if reportTo(&out, false, formatJSON, err) != 2 || out.Len() != 0 {
		t.Fatalf("duplicate child report: %s", out.String())
	}
	if _, err := os.Stat(credentials); !os.IsNotExist(err) {
		t.Fatal("CI wrote credentials")
	}
}

func TestCIRejectsUnsafeInputsBeforeExchange(t *testing.T) {
	isolatedCredentials(t)
	ciTestTransport(t, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network request"); return nil, nil })
	for _, args := range [][]string{
		{"--host", "http://hub.example", "--", "deploy", "."},
		{"--host", "https://user:secret@hub.example", "--", "deploy", "."},
		{"--host", "https://hub.example?query=secret", "--", "deploy", "."},
		{"--host", "https://hub.example", "--", "deploy", ".", "--host=https://elsewhere.example"},
		{"--host", "https://hub.example", "--", "connect", "https://elsewhere.example"},
		{"--host", "https://hub.example", "deploy", "."},
		{"--", "deploy", "."},
	} {
		argv := append([]string{"ci", "--policy", "production", "--identity-token-file", "-"}, args...)
		if _, err := execCLIStdin(t, strings.NewReader("fake"), argv...); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

func TestCIRedirectsAndErrorsDoNotExposeTokens(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308, 401, 409, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			ciTestTransport(t, func(r *http.Request) (*http.Response, error) {
				calls++
				response := ciResponse(status, "fake-secret-identity")
				response.Header.Set("Location", "https://other.example/stolen")
				return response, nil
			})
			_, err := exchangeCIIdentity(context.Background(), "https://hub.example", "production", "fake-secret-identity")
			if err == nil || calls != 1 || strings.Contains(err.Error(), "fake-secret") {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestCIRejectsMalformedCredentials(t *testing.T) {
	for _, body := range []string{"{}", ciCredential() + "{}", strings.Replace(ciCredential(), "Token", "Bearer", 1), strings.Replace(ciCredential(), "shk_fake_deployment", "bad", 1), `{"token":"shk_fake","token_type":"Token","expires_at":"2000-01-01T00:00:00Z"}`, strings.Repeat(" ", 17000) + ciCredential()} {
		ciTestTransport(t, func(*http.Request) (*http.Response, error) { return ciResponse(201, body), nil })
		if _, err := exchangeCIIdentity(context.Background(), "https://hub.example", "production", "fake"); err == nil {
			t.Fatal("accepted malformed credential")
		}
	}
}

func TestCIGitHubIdentity(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://pipelines.actions.githubusercontent.com/token?existing=value")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "fake-request-token")
	ciTestTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("audience") != "https://hub.example" || r.URL.Query().Get("existing") != "value" || r.Header.Get("Authorization") != "Bearer fake-request-token" {
			t.Fatal("incorrect OIDC request")
		}
		return ciResponse(200, `{"value":"fake-assertion"}`), nil
	})
	if token, err := githubCIIdentity(context.Background(), "https://hub.example"); err != nil || token != "fake-assertion" {
		t.Fatalf("token=%s err=%v", token, err)
	}
	ciTestTransport(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("request sent to untrusted issuer")
		return nil, nil
	})
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://actions.githubusercontent.com.evil.example/token")
	if _, err := githubCIIdentity(context.Background(), "audience"); err == nil {
		t.Fatal("accepted untrusted issuer")
	}
}

func TestCIEnvironment(t *testing.T) {
	got := ciEnvironment([]string{"PATH=/bin", "SHINYHUB_HOST=old", "SHINYHUB_TOKEN=old", "CI=false", "ACTIONS_ID_TOKEN_REQUEST_TOKEN=fake", "ACTIONS_ID_TOKEN_REQUEST_URL=https://issuer"}, "https://hub.example", "shk_fake")
	want := []string{"PATH=/bin", "SHINYHUB_HOST=https://hub.example", "SHINYHUB_TOKEN=shk_fake", "CI=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment=%q", got)
	}
}

func TestCIChildProcessHelper(t *testing.T) {
	if os.Getenv("SHINYHUB_TEST_CI_CHILD") != "1" {
		return
	}
	if os.Getenv("SHINYHUB_TOKEN") != "shk_fake" || os.Getenv("SHINYHUB_HOST") != "https://hub.example" || os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") != "" {
		os.Exit(91)
	}
	fmt.Fprintln(os.Stdout, "child result")
	fmt.Fprintln(os.Stderr, "child diagnostic")
	os.Exit(5)
}

func TestCIActualSubprocess(t *testing.T) {
	t.Setenv("SHINYHUB_TEST_CI_CHILD", "1")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "fake-request-token")
	var stdout, stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := runCIChild(cmd, []string{"-test.run=^TestCIChildProcessHelper$"}, "https://hub.example", "shk_fake")
	if ExitCode(err) != 5 || stdout.String() != "child result\n" || stderr.String() != "child diagnostic\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", ExitCode(err), stdout.String(), stderr.String())
	}
	var report bytes.Buffer
	if reportTo(&report, false, formatJSON, err) != 5 || report.Len() != 0 {
		t.Fatal("child error rendered twice")
	}
}

func TestCIGitHubDoesNotFollowRedirects(t *testing.T) {
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://pipelines.actions.githubusercontent.com/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "fake-request-token")
	calls := 0
	ciTestTransport(t, func(*http.Request) (*http.Response, error) {
		calls++
		response := ciResponse(307, "fake-request-token")
		response.Header.Set("Location", "https://pipelines.actions.githubusercontent.com/other")
		return response, nil
	})
	_, err := githubCIIdentity(context.Background(), "https://hub.example")
	if calls != 1 || err == nil || strings.Contains(err.Error(), "fake-request-token") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
