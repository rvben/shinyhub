// Command generate-routes refreshes the demo edge route manifest.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/rvben/shinyhub/deploy/cloudflare-demo/internal/routemanifest"
	"os"
)

func main() {
	check := flag.Bool("check", false, "fail when the committed manifest differs from the server routes, assets, or demo fleet")
	flag.Parse()
	m, err := routemanifest.Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		panic(err)
	}
	data = append(data, '\n')
	path := "deploy/cloudflare-demo/src/route-manifest.json"
	if *check {
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(current, data) {
			fmt.Fprintln(os.Stderr, "demo edge routes are stale; run GOWORK=off go run ./deploy/cloudflare-demo/generate-routes")
			os.Exit(1)
		}
		fmt.Println("demo edge route manifest is current")
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		panic(err)
	}
	fmt.Println(path)
}
