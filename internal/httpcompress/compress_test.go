package httpcompress

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// payload is text large enough to compress and varied enough that a truncated
// or reordered decode cannot match it by accident.
func payload(n int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < n; i++ {
		b.WriteString("function f")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("(){return ")
		b.WriteString(strconv.Itoa(i * 7))
		b.WriteString(";}\n")
	}
	return b.Bytes()[:n]
}

// rawClient does not negotiate or decode compression itself, so tests see the
// bytes on the wire.
var rawClient = &http.Client{Transport: &http.Transport{DisableCompression: true}}

func get(t *testing.T, srv *httptest.Server, method, acceptEncoding string, extra http.Header) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	for k, vs := range extra {
		req.Header[k] = vs
	}
	resp, err := rawClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip body is corrupt: %v", err)
	}
	return out
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(Handler(h))
	t.Cleanup(srv.Close)
	return srv
}

func TestCompressesEligibleResponses(t *testing.T) {
	body := payload(20000)
	for _, tc := range []struct {
		name     string
		declared bool
	}{{"declared length", true}, {"unknown length", false}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/javascript")
				if tc.declared {
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				}
				// Several writes, so the pending buffer and the streaming path
				// both carry bytes.
				for chunk := body; len(chunk) > 0; {
					n := min(700, len(chunk))
					_, _ = w.Write(chunk[:n])
					chunk = chunk[n:]
				}
			})
			resp, wire := get(t, srv, http.MethodGet, "gzip, deflate, br", nil)
			if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", got)
			}
			if got := resp.Header.Get("Vary"); got != "Accept-Encoding" {
				t.Errorf("Vary = %q, want Accept-Encoding", got)
			}
			if cl := resp.Header.Get("Content-Length"); cl == strconv.Itoa(len(body)) {
				t.Errorf("Content-Length still declares the identity size %s", cl)
			}
			if len(wire) >= len(body)/2 {
				t.Errorf("wire size %d is not meaningfully below %d", len(wire), len(body))
			}
			if got := gunzip(t, wire); !bytes.Equal(got, body) {
				t.Fatalf("decoded body differs from the original (%d vs %d bytes)", len(got), len(body))
			}
		})
	}
}

func TestNegotiatesAcceptEncoding(t *testing.T) {
	body := payload(4000)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		_, _ = w.Write(body)
	})
	for _, tc := range []struct {
		accept string
		want   bool
	}{
		{"", false},
		{"identity", false},
		{"br", false},
		{"gzip", true},
		{"GZIP", true},
		{"x-gzip", true},
		{"gzip;q=0", false},
		{"gzip; q=0.0", false},
		{"deflate, gzip;q=0.5", true},
		{"*", true},
		{"*;q=0", false},
		{"gzip;q=0, *", false},
		{"*;q=0, gzip", true},
		{"gzip;q=bogus", false},
	} {
		t.Run(tc.accept, func(t *testing.T) {
			resp, wire := get(t, srv, http.MethodGet, tc.accept, nil)
			got := resp.Header.Get("Content-Encoding") == "gzip"
			if got != tc.want {
				t.Fatalf("compressed = %v, want %v", got, tc.want)
			}
			if !got && !bytes.Equal(wire, body) {
				t.Fatal("identity body was altered")
			}
			// The response depends on Accept-Encoding whether or not this
			// client got gzip, so a shared cache must key on it either way.
			if v := resp.Header.Get("Vary"); v != "Accept-Encoding" {
				t.Errorf("Vary = %q, want Accept-Encoding", v)
			}
		})
	}
}

func TestPassesThroughIneligibleResponses(t *testing.T) {
	body := payload(5000)
	for _, tc := range []struct {
		name   string
		status int
		header http.Header
	}{
		{"already encoded", 200, http.Header{"Content-Type": {"text/html"}, "Content-Encoding": {"br"}}},
		{"event stream", 200, http.Header{"Content-Type": {"text/event-stream"}}},
		{"image", 200, http.Header{"Content-Type": {"image/png"}}},
		{"woff2", 200, http.Header{"Content-Type": {"font/woff2"}}},
		{"octet stream", 200, http.Header{"Content-Type": {"application/octet-stream"}}},
		{"no-transform", 200, http.Header{"Content-Type": {"text/html"}, "Cache-Control": {"public, no-transform"}}},
		{"partial content", 206, http.Header{"Content-Type": {"text/plain"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
				for k, vs := range tc.header {
					w.Header()[k] = vs
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write(body)
			})
			resp, wire := get(t, srv, http.MethodGet, "gzip", nil)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			if got, want := resp.Header.Get("Content-Encoding"), tc.header.Get("Content-Encoding"); got != want {
				t.Fatalf("Content-Encoding = %q, want %q", got, want)
			}
			if !bytes.Equal(wire, body) {
				t.Fatal("body was altered")
			}
			if v := resp.Header.Get("Vary"); v != "" {
				t.Errorf("Vary = %q on a response that never varies", v)
			}
		})
	}
}

func TestBodilessStatusesPassThrough(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("ETag", `"v1"`)
				w.WriteHeader(status)
			})
			resp, wire := get(t, srv, http.MethodGet, "gzip", nil)
			if resp.StatusCode != status || len(wire) != 0 || resp.Header.Get("Content-Encoding") != "" {
				t.Fatalf("got %d, %d bytes, encoding %q", resp.StatusCode, len(wire), resp.Header.Get("Content-Encoding"))
			}
			// A 304 must carry the same validator the 200 did.
			if got := resp.Header.Get("ETag"); got != `"v1"` {
				t.Errorf("ETag = %q, want unchanged", got)
			}
		})
	}
}

func TestPreservesMultipleContentEncodings(t *testing.T) {
	body := payload(20000)
	var encoded bytes.Buffer
	zw := gzip.NewWriter(&encoded)
	if _, err := zw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() < MinSize {
		t.Fatal("fixture must be large enough to trigger compression")
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Encoding"] = []string{"identity", "gzip"}
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write(encoded.Bytes())
	})
	for _, wrapped := range []bool{false, true} {
		t.Run("wrapped="+strconv.FormatBool(wrapped), func(t *testing.T) {
			var handler http.Handler = h
			if wrapped {
				handler = Handler(h)
			}
			srv := httptest.NewServer(handler)
			defer srv.Close()
			resp, wire := get(t, srv, http.MethodGet, "gzip", nil)
			if got := strings.Join(resp.Header.Values("Content-Encoding"), ","); got != "identity,gzip" {
				t.Errorf("Content-Encoding = %q, want identity,gzip", got)
			}
			if !bytes.Equal(wire, encoded.Bytes()) {
				t.Error("already encoded response body was changed")
			}
			if !bytes.Equal(gunzip(t, wire), body) {
				t.Error("decoding the declared encoding did not recover the original body")
			}
		})
	}
}

func TestCommitsHeadersBeforeBufferedBody(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, size := range []int{8, 2 * MinSize} {
			for _, wrapped := range []bool{false, true} {
				name := "explicit=" + strconv.FormatBool(explicit) + "/size=" + strconv.Itoa(size) + "/wrapped=" + strconv.FormatBool(wrapped)
				t.Run(name, func(t *testing.T) {
					body := append([]byte("first"), payload(size)...)
					h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/plain")
						w.Header().Set("X-Early", "hint")
						w.WriteHeader(http.StatusEarlyHints)
						w.Header().Set("X-Early", "original")
						w.Header().Set("Trailer", "X-End")
						if explicit {
							w.WriteHeader(http.StatusCreated)
						} else {
							_, _ = w.Write(body[:5])
						}
						w.Header()["X-Early"][0] = "changed"
						w.Header().Set("X-Late", "must not be sent")
						w.Header().Set("Content-Type", "application/octet-stream")
						if explicit {
							_, _ = w.Write(body[:5])
						}
						trailerValues := []string{"pending trailer"}
						w.Header()["X-End"] = trailerValues
						_, _ = w.Write(body[5:])
						trailerValues[0] = "declared trailer"
						w.Header().Set(http.TrailerPrefix+"X-Dynamic", "late trailer")
					})
					var handler http.Handler = h
					if wrapped {
						handler = Handler(h)
					}
					srv := httptest.NewServer(handler)
					defer srv.Close()
					resp, wire := get(t, srv, http.MethodGet, "gzip", nil)
					wantStatus := http.StatusOK
					if explicit {
						wantStatus = http.StatusCreated
					}
					if resp.StatusCode != wantStatus {
						t.Errorf("status = %d, want %d", resp.StatusCode, wantStatus)
					}
					if resp.Header.Get("X-Early") != "original" || resp.Header.Get("X-Late") != "" || resp.Header.Get("Content-Type") != "text/plain" {
						t.Errorf("committed headers changed: %v", resp.Header)
					}
					if wrapped && size >= MinSize {
						if resp.Header.Get("Content-Encoding") != "gzip" {
							t.Error("late Content-Type changed the compression decision")
						} else {
							wire = gunzip(t, wire)
						}
					}
					if !bytes.Equal(wire, body) {
						t.Error("response body changed")
					}
					if resp.Trailer.Get("X-End") != "declared trailer" || resp.Trailer.Get("X-Dynamic") != "late trailer" {
						t.Errorf("trailers changed: %v", resp.Trailer)
					}
					if resp.Header.Get("X-End") != "" || resp.Header.Get("X-Dynamic") != "" {
						t.Error("trailer values appeared in response headers")
					}
				})
			}
		}
	}
}

func TestSmallBodiesAreNotCompressed(t *testing.T) {
	body := payload(MinSize - 1)
	for _, declared := range []bool{true, false} {
		t.Run("declared="+strconv.FormatBool(declared), func(t *testing.T) {
			srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if declared {
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				}
				_, _ = w.Write(body[:10])
				_, _ = w.Write(body[10:])
			})
			resp, wire := get(t, srv, http.MethodGet, "gzip", nil)
			if resp.Header.Get("Content-Encoding") != "" || !bytes.Equal(wire, body) {
				t.Fatalf("small body was compressed or altered: encoding %q", resp.Header.Get("Content-Encoding"))
			}
		})
	}
	// Exactly MinSize is the first size that is compressed.
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload(MinSize))
	})
	if resp, _ := get(t, srv, http.MethodGet, "gzip", nil); resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("a MinSize body was not compressed")
	}
}

func TestEmptyResponses(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"nothing written": func(w http.ResponseWriter, r *http.Request) {},
		"header only": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(201)
		},
		"empty write call": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			resp, wire := get(t, serve(t, h), http.MethodGet, "gzip", nil)
			if len(wire) != 0 || resp.Header.Get("Content-Encoding") != "" {
				t.Fatalf("empty response became %d bytes with encoding %q", len(wire), resp.Header.Get("Content-Encoding"))
			}
		})
	}
}

func TestHeadRangeAndUpgradeStayIdentity(t *testing.T) {
	body := payload(5000)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "app.js", time.Unix(0, 0), bytes.NewReader(body))
	})

	resp, _ := get(t, srv, http.MethodHead, "gzip", nil)
	if resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("HEAD: encoding %q, length %q", resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Length"))
	}

	resp, wire := get(t, srv, http.MethodGet, "gzip", http.Header{"Range": {"bytes=100-199"}})
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(wire, body[100:200]) || resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Range: status %d, encoding %q, body match %v", resp.StatusCode, resp.Header.Get("Content-Encoding"), bytes.Equal(wire, body[100:200]))
	}

	// A WebSocket upgrade must reach the handler on the raw writer so the
	// connection can be hijacked.
	hijacked := make(chan bool, 1)
	up := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, isRaw := w.(http.Hijacker)
		_, isWrapper := w.(*writer)
		hijacked <- isRaw && !isWrapper
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		_ = buf.Flush()
		_ = conn.Close()
	})
	resp, _ = get(t, up, http.MethodGet, "gzip", http.Header{"Connection": {"keep-alive, Upgrade"}, "Upgrade": {"websocket"}})
	if resp.StatusCode != http.StatusSwitchingProtocols || !<-hijacked {
		t.Fatalf("upgrade: status %d, raw writer delivered %v", resp.StatusCode, false)
	}
}

func TestWeakensStrongETag(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{`"abc"`, `W/"abc"`}, {`W/"abc"`, `W/"abc"`}} {
		srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("ETag", tc.in)
			_, _ = w.Write(payload(3000))
		})
		resp, _ := get(t, srv, http.MethodGet, "gzip", nil)
		if got := resp.Header.Get("ETag"); got != tc.want {
			t.Errorf("ETag %s became %q, want %q", tc.in, got, tc.want)
		}
		resp, _ = get(t, srv, http.MethodGet, "", nil)
		if got := resp.Header.Get("ETag"); got != tc.in {
			t.Errorf("identity ETag %s became %q", tc.in, got)
		}
	}
}

func TestSniffsUntypedBody(t *testing.T) {
	body := append([]byte("<!DOCTYPE html><html><body>"), payload(4000)...)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) })
	resp, wire := get(t, srv, http.MethodGet, "gzip", nil)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want sniffed text/html", ct)
	}
	if resp.Header.Get("Content-Encoding") != "gzip" || !bytes.Equal(gunzip(t, wire), body) {
		t.Fatal("sniffed HTML was not compressed intact")
	}
}

// An empty write carries nothing to sniff. Sniffing it anyway would fix the
// type as text/plain before the real body arrives, which net/http does not do.
func TestEmptyWriteDoesNotFixTheSniffedType(t *testing.T) {
	body := append([]byte("<!DOCTYPE html><html><body>"), payload(4000)...)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(nil)
		_, _ = w.Write(body)
	})
	resp, wire := get(t, srv, http.MethodGet, "gzip", nil)
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html sniffed from the real body", ct)
	}
	if !bytes.Equal(gunzip(t, wire), body) {
		t.Fatal("body did not survive the round trip")
	}
}

// A flush must put the bytes written so far in front of the client, decodable,
// while the handler is still running: streaming handlers depend on it.
func TestFlushDeliversDecodableBytesMidResponse(t *testing.T) {
	release := make(chan struct{})
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write([]byte("first\n"))
		http.NewResponseController(w).Flush()
		<-release
		_, _ = w.Write(append([]byte("second\n"), payload(3000)...))
	})
	defer close(release)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := rawClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip (the flush precedes MinSize)", resp.Header.Get("Content-Encoding"))
	}
	got := make(chan string, 1)
	go func() {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		line, err := bufio.NewReader(zr).ReadString('\n')
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		got <- line
	}()
	select {
	case line := <-got:
		if line != "first\n" {
			t.Fatalf("first flushed line = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flushed bytes did not reach the client while the handler was still running")
	}
}

// Deadline changes made through http.ResponseController must reach the
// connection, which is how the server lifts write deadlines for long streams.
func TestResponseControllerReachesConnection(t *testing.T) {
	errs := make(chan error, 1)
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		errs <- http.NewResponseController(w).SetWriteDeadline(time.Time{})
	})
	get(t, srv, http.MethodGet, "gzip", nil)
	if err := <-errs; err != nil {
		t.Fatalf("SetWriteDeadline through the wrapper: %v", err)
	}
}

// The production path for application traffic: httputil.ReverseProxy relaying
// a backend that does not compress (httpuv, uvicorn), and one that does.
func TestReverseProxiedBackends(t *testing.T) {
	body := payload(60000)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("ETag", `"dep-v1"`)
		switch r.URL.Path {
		case "/sized":
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
		case "/chunked":
			for chunk := body; len(chunk) > 0; {
				n := min(4096, len(chunk))
				_, _ = w.Write(chunk[:n])
				http.NewResponseController(w).Flush()
				chunk = chunk[n:]
			}
		case "/self-gzipped":
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			_, _ = zw.Write(body)
			_ = zw.Close()
		}
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Transport = &http.Transport{DisableCompression: true}
	front := httptest.NewServer(Handler(rp))
	defer front.Close()

	for _, path := range []string{"/sized", "/chunked", "/self-gzipped"} {
		t.Run(path, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, front.URL+path, nil)
			req.Header.Set("Accept-Encoding", "gzip, br")
			resp, err := rawClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			wire, _ := io.ReadAll(resp.Body)
			if resp.Header.Get("Content-Encoding") != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", resp.Header.Get("Content-Encoding"))
			}
			// Exactly one layer: a double-encoded body would decode to gzip.
			if got := gunzip(t, wire); !bytes.Equal(got, body) {
				t.Fatalf("decoded body differs (%d vs %d bytes)", len(got), len(body))
			}
		})
	}
}
