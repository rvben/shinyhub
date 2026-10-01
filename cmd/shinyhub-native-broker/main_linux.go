//go:build linux

// This deliberately separate binary is the only privileged part of the native
// user-isolation backend. Never run the ShinyHub control plane as root.
package main

import (
	"context"
	"encoding/json"
	"flag"
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
	} else if len(os.Args) > 1 && os.Args[1] == "provision" {
		flags := flag.NewFlagSet("provision", flag.ContinueOnError)
		policy := flags.String("policy", "/etc/shinyhub/native-broker.json", "Root-owned desired broker policy")
		apply := flags.Bool("apply", false, "Apply the plan while controller, apps and broker are stopped")
		err = flags.Parse(os.Args[2:])
		if err == flag.ErrHelp {
			return
		}
		if err == nil && flags.NArg() != 0 {
			err = fmt.Errorf("provision accepts flags only")
		}
		if err == nil {
			var plan nativebroker.ProvisionPlan
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer cancel()
			plan, err = nativebroker.Provision(ctx, *policy, *apply)
			if err == nil {
				err = json.NewEncoder(os.Stdout).Encode(plan)
			}
		}
	} else {
		err = fmt.Errorf("usage: shinyhub-native-broker serve /etc/shinyhub/native-broker.json | provision --policy /etc/shinyhub/native-broker.json [--apply]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
}
