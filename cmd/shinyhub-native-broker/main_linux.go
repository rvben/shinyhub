//go:build linux

// This deliberately separate binary is the only privileged part of the native
// user-isolation backend. Never run the ShinyHub control plane as root.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rvben/shinyhub/internal/nativebroker"
)

func main() {
	var err error
	if len(os.Args) == 3 && os.Args[1] == "bootstrap" {
		err = nativebroker.Bootstrap(os.Args[2])
	} else if len(os.Args) == 3 && os.Args[1] == "serve" {
		var server *nativebroker.Server
		server, err = nativebroker.NewServer(os.Args[2])
		if err == nil {
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()
			err = server.Serve(ctx)
		}
	} else {
		err = fmt.Errorf("usage: shinyhub-native-broker serve /etc/shinyhub/native-broker.json")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
}
