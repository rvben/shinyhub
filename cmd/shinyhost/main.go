package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/rvben/shinyhost/internal/config"
)

func main() {
	cfg, err := config.Load("shinyhost.yaml")
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("shinyhost listening on %s", addr)
	if err := http.ListenAndServe(addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})); err != nil {
		log.Fatal(err)
	}
}
