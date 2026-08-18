// serverless-probe is the disposable image used by the managed-runtime
// integration suite. It deliberately exercises plain HTTP, WebSocket upgrade,
// and long requests without depending on ShinyHub or a language runtime.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/websocket"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"service": "shinyhub-serverless-probe"})
	})
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var value string
			if err := websocket.Message.Receive(conn, &value); err != nil {
				return
			}
			if err := websocket.Message.Send(conn, value); err != nil {
				return
			}
		}
	}))
	mux.HandleFunc("/slow", func(w http.ResponseWriter, req *http.Request) {
		delay, err := time.ParseDuration(req.URL.Query().Get("duration"))
		if err != nil || delay <= 0 || delay > 65*time.Minute {
			http.Error(w, "duration must be between 0 and 65m", http.StatusBadRequest)
			return
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-req.Context().Done():
			return
		case <-timer.C:
			w.WriteHeader(http.StatusNoContent)
		}
	})

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("serverless probe listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
