package cli

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// goNoise is what a raw transport error reads like: the verb, the full internal
// URL, and the syscall that failed. None of it belongs in front of an operator,
// and its absence is what these tests are really checking.
var goNoise = []string{`Get "`, `Post "`, "dial tcp", "connect: ", "/api/"}

func assertNoGoNoise(t *testing.T, msg string) {
	t.Helper()
	for _, frag := range goNoise {
		if strings.Contains(msg, frag) {
			t.Errorf("message still carries Go transport plumbing %q: %s", frag, msg)
		}
	}
}

// closedPort returns an address nothing is listening on, by binding a port and
// releasing it. Asking the kernel for one is the only way to be sure; a
// hardcoded port is a port some other process on the machine may own.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// get drives a real request through the shared client wrapper, so what these
// tests see is what a command sees.
func get(t *testing.T, c *apiClient, rawURL string) error {
	t.Helper()
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("want a transport failure, got a response")
	}
	return err
}

// A refused connection is the failure an operator meets most often, usually
// because the server is down or because the saved address is stale. The message
// must name the server, say what happened in words, and drop the endpoint the
// operator never chose.
func TestTransport_ConnectionRefused(t *testing.T) {
	t.Setenv("SHINYHUB_HOST", "")
	addr := closedPort(t)

	err := get(t, httpClient, "http://"+addr+"/api/apps/metrics")

	want := fmt.Sprintf("cannot reach the ShinyHub server at http://%s (connection refused)", addr)
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	assertNoGoNoise(t, err.Error())
	if hint := hintOf(err); !strings.Contains(hint, "start the server") {
		t.Errorf("hint should lead with the likeliest fix, got %q", hint)
	}
	if kind, code := classify(err); kind != KindNetwork || code != 3 {
		t.Errorf("classify = (%s, %d), want (network, 3)", kind, code)
	}
	// The original is kept as the cause, so anything matching on transport
	// shapes still finds what it matches on.
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Error("the underlying *url.Error must survive as the cause")
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Error("the underlying syscall error must survive as the cause")
	}
}

// A server that accepts the connection and then says nothing is a different
// problem with a different fix, and the deadline it blew is the operator's own
// setting, so the message names it.
func TestTransport_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	client := &apiClient{&http.Client{Timeout: 150 * time.Millisecond}}
	err := get(t, client, srv.URL+"/api/apps/metrics")

	want := fmt.Sprintf("the ShinyHub server at %s did not answer within 150ms", srv.URL)
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	assertNoGoNoise(t, err.Error())
	// A timeout and a refusal share an exit code but not a kind; the schema
	// documents them apart, so the classifier must keep them apart.
	if kind, code := classify(err); kind != KindTimeout || code != 3 {
		t.Errorf("classify = (%s, %d), want (timeout, 3)", kind, code)
	}
}

// A client with no deadline (the deploy and log-stream path) has no duration to
// name, and must not invent one.
func TestTransport_TimeoutWithoutADeadlineNamesNoDuration(t *testing.T) {
	err := transportError(&url.Error{
		Op:  "Get",
		URL: "http://shinyhub.example/api/apps",
		Err: fakeTimeoutErr{},
	}, 0)

	want := "the ShinyHub server at http://shinyhub.example did not answer in time"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

// A connection dropped mid-request reads as a bare "EOF" from Go, which tells
// the operator nothing at all.
func TestTransport_ClosedBeforeAnswering(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, aerr := ln.Accept()
		if aerr == nil {
			conn.Close()
		}
	}()

	gotErr := get(t, httpClient, "http://"+ln.Addr().String()+"/api/apps/metrics")

	want := fmt.Sprintf("the connection to the ShinyHub server at http://%s closed before it answered", ln.Addr())
	if gotErr.Error() != want {
		t.Errorf("message = %q, want %q", gotErr.Error(), want)
	}
	assertNoGoNoise(t, gotErr.Error())
}

// A self-signed server is the shape an operator meets when they put ShinyHub
// behind their own TLS. The exact certificate complaint varies by platform
// verifier, so only the stable part is pinned here; transportAdvice pins the
// wording per shape.
func TestTransport_UntrustedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	// The rejected handshake is the point of the test, not a fault to report on
	// the test's own output.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer srv.Close()

	err := get(t, httpClient, srv.URL+"/api/apps/metrics")

	if !strings.HasPrefix(err.Error(), "cannot verify the ShinyHub server at "+srv.URL) {
		t.Errorf("message = %q, want it to open by naming the server it could not verify", err.Error())
	}
	assertNoGoNoise(t, err.Error())
	if kind, code := classify(err); kind != KindNetwork || code != 3 {
		t.Errorf("classify = (%s, %d), want (network, 3)", kind, code)
	}
}

// One case per transport shape the CLI can meet, each built from the error type
// Go really returns. A shape nothing here recognises keeps its own cause rather
// than being given an invented one.
func TestTransportAdvice_WordingPerShape(t *testing.T) {
	const server = "https://shinyhub.example"
	const reqURL = server + "/api/apps/metrics"

	cases := []struct {
		name string
		err  error
		want string
		kind Kind
	}{
		{
			"host does not exist",
			&net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "shinyhub.example", IsNotFound: true}},
			"cannot reach the ShinyHub server at https://shinyhub.example (that hostname does not resolve)",
			KindNetwork,
		},
		{
			// A resolver that failed is not a hostname that does not exist, and
			// reporting the second when the first happened states a fact nobody
			// established.
			"lookup failed",
			&net.DNSError{Err: "server misbehaving", Name: "shinyhub.example"},
			"cannot reach the ShinyHub server at https://shinyhub.example (its hostname could not be looked up: server misbehaving)",
			KindNetwork,
		},
		{
			// A resolver that timed out satisfies net.Error.Timeout(), so testing
			// for a timeout first would report an unresolved name as a server that
			// answered slowly. It never answered at all: nothing was ever dialled.
			"resolver timed out",
			&net.DNSError{Err: "i/o timeout", Name: "shinyhub.example", IsTimeout: true},
			"cannot reach the ShinyHub server at https://shinyhub.example (its hostname could not be looked up: i/o timeout)",
			KindNetwork,
		},
		{
			"refused",
			&net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}},
			"cannot reach the ShinyHub server at https://shinyhub.example (connection refused)",
			KindNetwork,
		},
		{
			"unknown certificate authority",
			&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}},
			"cannot verify the ShinyHub server at https://shinyhub.example (its certificate is signed by an unknown authority)",
			KindNetwork,
		},
		{
			"certificate for another name",
			&tls.CertificateVerificationError{Err: x509.HostnameError{Certificate: &x509.Certificate{}, Host: "elsewhere.example"}},
			"cannot verify the ShinyHub server at https://shinyhub.example (its certificate is not valid for that hostname)",
			KindNetwork,
		},
		{
			"certificate rejected for some other reason",
			&tls.CertificateVerificationError{Err: errors.New("certificate has expired")},
			"cannot verify the ShinyHub server at https://shinyhub.example: certificate has expired",
			KindNetwork,
		},
		{
			"closed mid-request",
			io.EOF,
			"the connection to the ShinyHub server at https://shinyhub.example closed before it answered",
			KindNetwork,
		},
		{
			"connection reset",
			&net.OpError{Op: "read", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}},
			"the connection to the ShinyHub server at https://shinyhub.example closed before it answered",
			KindNetwork,
		},
		{
			"unrecognised shape keeps its own cause",
			errors.New("http: server closed idle connection"),
			"cannot reach the ShinyHub server at https://shinyhub.example: http: server closed idle connection",
			KindNetwork,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := transportError(&url.Error{Op: "Get", URL: reqURL, Err: tc.err}, 30*time.Second)
			if err.Error() != tc.want {
				t.Errorf("message = %q\n         want %q", err.Error(), tc.want)
			}
			assertNoGoNoise(t, err.Error())
			if hintOf(err) == "" {
				t.Error("every transport failure must carry a remedy")
			}
			// The sentence and the envelope field describe the same event or one
			// of the two readers is being told something the other is not.
			if kind, code := classify(err); kind != tc.kind || code != 3 {
				t.Errorf("classify = (%s, %d), want (%s, 3)", kind, code, tc.kind)
			}
		})
	}
}

// The remedy has to point at the setting the operator is actually using.
// Telling someone who passed --host to check their saved servers is advice
// about something that had no part in choosing this address.
func TestTransportAdvice_HintNamesWhereTheAddressCameFrom(t *testing.T) {
	refused := &url.Error{Op: "Get", URL: "http://shinyhub.example/api/apps",
		Err: &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}}

	cases := []struct {
		name string
		flag string
		env  string
		want string
	}{
		{"--host wins", "http://other.example", "http://env.example", "check the address passed to --host"},
		{"environment next", "", "http://env.example", "check the address in $SHINYHUB_HOST"},
		{"otherwise the saved address", "", "", "check the saved address with `shinyhub hosts`, or target another with --host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := hostFlagOverride
			hostFlagOverride = tc.flag
			t.Cleanup(func() { hostFlagOverride = prev })
			t.Setenv("SHINYHUB_HOST", tc.env)

			hint := hintOf(transportError(refused, 30*time.Second))
			if !strings.HasSuffix(hint, tc.want) {
				t.Errorf("hint = %q, want it to end with %q", hint, tc.want)
			}
		})
	}
}

// A response is not a transport failure, whatever its status: the server was
// reached, and httpError speaks for what it said.
func TestTransport_ResponsesPassThroughUntouched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("a server that answered must not produce a transport error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestServerBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://127.0.0.1:8099/api/apps/metrics", "http://127.0.0.1:8099"},
		{"https://shinyhub.example/api/apps?limit=20", "https://shinyhub.example"},
		// A query string can carry a credential, so the whole of it goes.
		{"https://shinyhub.example/api/data?token=shk_secret", "https://shinyhub.example"},
		// Nothing parseable to reduce: report what was attempted rather than a
		// blank that reads like a server with no address.
		{"://nonsense", "://nonsense"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := serverBase(tc.in); got != tc.want {
			t.Errorf("serverBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
