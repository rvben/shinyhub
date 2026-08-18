package cli

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"time"
)

// apiClient is how every command talks to a ShinyHub server. Its only addition
// to *http.Client is what happens when a request never reaches one.
//
// Go reports that as its own transport plumbing: `Get
// "http://host:8099/api/apps/metrics": dial tcp 127.0.0.1:8099: connect:
// connection refused`. That names an internal endpoint the operator never
// chose, repeats the address twice, and says nothing about what to do next. Do
// replaces it with a sentence about the server, and puts the remedy in the
// hint the error renderer prints under it.
type apiClient struct{ *http.Client }

// Do sends req, translating a failure to reach the server. A response is
// returned untouched, including an error status: a server that answered 500 is
// reachable, and httpError already speaks for it.
func (c *apiClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, transportError(err, c.Client.Timeout)
	}
	return resp, nil
}

// transportError rewrites a request that never produced a response. timeout is
// the client's own deadline, named in the message when one expires.
//
// The original error is kept as the cause rather than folded into the message,
// so classify still reads the kind from the real transport shape and callers
// matching on net.Error or *url.Error still see what they match on.
func transportError(err error, timeout time.Duration) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		// Not a transport failure (a redirect policy rejection, a request body
		// that could not be rewound). Nothing here knows better than the error.
		return err
	}
	msg, hint, kind := transportAdvice(err, serverBase(ue.URL), timeout)
	return &ExitCodeError{Code: 3, Kind: kind,
		Err: &hintedMsgError{msg: msg, hint: hint, cause: err}}
}

// transportAdvice states what went wrong reaching server, what to do about it,
// and which kind the envelope records. Each case is a different problem with a
// different fix; the last one keeps the underlying cause verbatim rather than
// inventing a diagnosis for a shape nothing here recognises.
//
// The kind is decided here, alongside the wording, so the sentence a person
// reads and the field a script branches on can never describe different events.
func transportAdvice(err error, server string, timeout time.Duration) (msg, hint string, kind Kind) {
	// DNS is tested before the timeout below because a resolver that times out
	// still failed to resolve, and saying so is more use than reporting it as a
	// slow server. Whether the name is absent or the lookup merely failed are
	// different facts, so they get different sentences.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return fmt.Sprintf("cannot reach the ShinyHub server at %s (that hostname does not resolve)", server),
				targetHint(), KindNetwork
		}
		return fmt.Sprintf("cannot reach the ShinyHub server at %s (its hostname could not be looked up: %s)",
			server, dnsErr.Err), targetHint(), KindNetwork
	}

	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		within := "in time"
		if timeout > 0 {
			within = "within " + timeout.String()
		}
		// The one kind that is not network: the server was reached and simply
		// never answered, so a retry is worth something.
		return fmt.Sprintf("the ShinyHub server at %s did not answer %s", server, within),
			"the server may be overloaded, or the address may not be reachable; check the server's logs",
			KindTimeout
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Sprintf("cannot reach the ShinyHub server at %s (connection refused)", server),
			"start the server if it is down; otherwise " + targetHint(), KindNetwork
	}

	var unknownCA x509.UnknownAuthorityError
	if errors.As(err, &unknownCA) {
		return fmt.Sprintf("cannot verify the ShinyHub server at %s (its certificate is signed by an unknown authority)", server),
			"install the issuing CA certificate on this machine, or front the server with a publicly trusted one",
			KindNetwork
	}
	var wrongName x509.HostnameError
	if errors.As(err, &wrongName) {
		return fmt.Sprintf("cannot verify the ShinyHub server at %s (its certificate is not valid for that hostname)", server),
			"use the hostname the certificate was issued for", KindNetwork
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return fmt.Sprintf("cannot verify the ShinyHub server at %s: %s", server, certErr.Err),
			"check the server's TLS certificate", KindNetwork
	}

	if connectionClosedBeforeAnswering(err) {
		return fmt.Sprintf("the connection to the ShinyHub server at %s closed before it answered", server),
			"the server, or a proxy in front of it, dropped the connection; check the server's logs",
			KindNetwork
	}

	// Unrecognised shape: report the cause the transport gave, minus the
	// url.Error wrapper whose op and full URL are what made it unreadable.
	var ue *url.Error
	cause := err
	if errors.As(err, &ue) && ue.Err != nil {
		cause = ue.Err
	}
	return fmt.Sprintf("cannot reach the ShinyHub server at %s: %s", server, cause), targetHint(), KindNetwork
}

// connectionClosedBeforeAnswering recognises the equivalent shapes net/http
// can return when the peer accepts a connection and closes it without a
// response. errServerClosedIdle is an unexported net/http sentinel, so its
// stable standard-library message is the only available discriminator.
func connectionClosedBeforeAnswering(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	for err != nil {
		if err.Error() == "http: server closed idle connection" {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// targetHint names the thing the operator can change to point somewhere else,
// which depends on where this invocation's address came from. Telling someone
// who passed --host to check their saved servers is advice about a setting they
// are not using.
func targetHint() string {
	switch {
	case hostFlagOverride != "":
		return "check the address passed to --host"
	case os.Getenv("SHINYHUB_HOST") != "":
		return "check the address in $SHINYHUB_HOST"
	default:
		return "check the saved address with `shinyhub hosts`, or target another with --host"
	}
}

// serverBase reduces a request URL to the server it was sent to. The path names
// an internal endpoint the operator neither chose nor can act on, and a query
// string can carry a credential, so neither belongs in an error message. A URL
// that will not parse is returned unchanged: a failure message is the wrong
// place to substitute a plausible-looking blank.
func serverBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	return u.Scheme + "://" + u.Host
}
