// The mixed-load fixture has no external runtime dependencies. Its purpose is
// to expose gateway costs, not to simulate Shiny's rendering engine.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

func main() {
	cgroup := flag.String("cgroup", "/sys/fs/cgroup", "target cgroup v2 directory")
	observeAddr := flag.String("observe-addr", "0.0.0.0:9091", "metrics and resources listener")
	metricsURL := flag.String("metrics-url", "http://127.0.0.1:9090", "server metrics URL")
	mode := flag.String("mode", "serve", "serve, metrics, or fetch")
	port := flag.Int("port", 8000, "listen port")
	delay := flag.Duration("startup", 100*time.Millisecond, "deterministic startup delay")
	target := flag.String("url", "http://127.0.0.1:9090/debug/pprof/profile?seconds=10", "local profile URL")
	output := flag.String("out", "/state/profile.pprof", "profile output")
	flag.Parse()
	if *mode == "metrics" {
		target, err := url.Parse(*metricsURL)
		if err != nil || target.Scheme != "http" || target.Host == "" {
			log.Fatal("metrics-url must be an absolute HTTP URL")
		}
		mux := http.NewServeMux()
		mux.Handle("/metrics", httputil.NewSingleHostReverseProxy(target))
		mux.HandleFunc("/resources", func(w http.ResponseWriter, r *http.Request) {
			data := targetResources(func(path string) ([]byte, error) {
				if strings.HasPrefix(path, "/sys/fs/cgroup/") {
					path = strings.TrimRight(*cgroup, "/") + strings.TrimPrefix(path, "/sys/fs/cgroup")
				}
				return os.ReadFile(path)
			})
			w.Header().Set("Content-Type", "application/json")
			if data.validate() != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			_ = json.NewEncoder(w).Encode(data)
		})
		log.Fatal(http.ListenAndServe(*observeAddr, mux))
		return
	}
	if *mode == "fetch" {
		client := &http.Client{Timeout: 45 * time.Second}
		resp, err := client.Get(*target)
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			log.Fatalf("profile status %d", resp.StatusCode)
		}
		f, err := os.Create(*output)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		if _, err = io.Copy(f, resp.Body); err != nil {
			log.Fatal(err)
		}
		return
	}

	version := "0"
	if data, err := os.ReadFile("version.txt"); err == nil {
		version = strings.TrimSpace(string(data))
	} else if !os.IsNotExist(err) {
		log.Fatal(err)
	}
	time.Sleep(*delay)
	page := `<!doctype html><html><head><title>Mixed load fixture</title><meta charset="utf-8"><link rel="stylesheet" href="style.css"></head><body><h1 id="mixed-fixture">Mixed load fixture</h1>` + strings.Repeat("<p>Representative dashboard content.</p>", 850) + `<script src="app.js"></script></body></html>`
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mime, body := "text/html; charset=utf-8", page
		switch r.URL.Path {
		case "/":
		case "/version":
			mime, body = "text/plain", version
		case "/app.js":
			mime, body = "application/javascript", "/* mixed-js */\n"+strings.Repeat("// fixture asset padding\n", 2700)
		case "/style.css":
			mime, body = "text/css", "/* mixed-css */\n"+strings.Repeat("p { color: #333; }\n", 450)
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		fmt.Fprint(w, body)
	})
	mux.Handle("/websocket/", websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(c *websocket.Conn) {
		defer c.Close()
		if err := websocket.Message.Send(c, "ready"); err != nil {
			return
		}
		for {
			var message string
			if err := websocket.Message.Receive(c, &message); err != nil {
				return
			}
			if err := websocket.Message.Send(c, message); err != nil {
				return
			}
		}
	}})
	server := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", *port), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(server.ListenAndServe())
}
