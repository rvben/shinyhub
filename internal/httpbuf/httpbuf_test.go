package httpbuf

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPool_GetReturnsFullSizeBuffer(t *testing.T) {
	var p Pool
	if b := p.Get(); len(b) != Size {
		t.Fatalf("Get returned %d bytes, want %d", len(b), Size)
	}
}

// A buffer handed back must come out again on the next Get. The race
// detector makes sync.Pool drop a quarter of Puts at random, so the check
// allows a few attempts before calling the pool broken.
func TestPool_ReusesReturnedBuffer(t *testing.T) {
	var p Pool
	for attempt := 0; attempt < 32; attempt++ {
		first := p.Get()
		p.Put(first)
		second := p.Get()
		if &second[0] == &first[0] {
			return
		}
	}
	t.Fatal("Get after Put never handed back the recycled buffer")
}

func TestPool_DropsUndersizedBuffers(t *testing.T) {
	var p Pool
	p.Put(make([]byte, 16))
	if b := p.Get(); len(b) != Size {
		t.Fatalf("Get after an undersized Put returned %d bytes, want %d", len(b), Size)
	}
}

func TestReverseProxy_DrawsFromSharedPool(t *testing.T) {
	rp := ReverseProxy(&url.URL{Scheme: "http", Host: "127.0.0.1:1"})
	if rp.BufferPool != Buffers {
		t.Fatalf("BufferPool = %v, want the shared pool", rp.BufferPool)
	}
}

// The pooled proxy must still deliver a body larger than one buffer intact.
func TestReverseProxy_StreamsBodyLargerThanOneBuffer(t *testing.T) {
	body := strings.Repeat("x", 3*Size+17)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer backend.Close()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(ReverseProxy(target))
	defer front.Close()

	resp, err := http.Get(front.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("proxied body differs: got %d bytes, want %d", len(got), len(body))
	}
}
