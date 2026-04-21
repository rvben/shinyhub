//go:build ignore

// Generator for bundle-rules.json — the JSON literal the JS zipper consumes.
// Run via:  go generate ./internal/bundle/...
package main

import (
	"encoding/json"
	"log"
	"os"

	"github.com/rvben/shinyhub/internal/bundle"
)

func main() {
	out, err := json.MarshalIndent(bundle.DefaultRules(), "", "  ")
	if err != nil {
		log.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile("../ui/static/bundle-rules.json", append(out, '\n'), 0o644); err != nil {
		log.Fatalf("write: %v", err)
	}
	log.Printf("wrote ../ui/static/bundle-rules.json (%d bytes)", len(out))
}
