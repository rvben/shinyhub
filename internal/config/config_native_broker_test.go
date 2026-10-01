package config_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestNativeBrokerIsOptIn(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.Native.BrokerSocket != "" {
		t.Fatal("native user isolation must remain opt-in")
	}
}
func TestNativeBrokerSocketYAMLAndEnvironment(t *testing.T) {
	path := writeIsolationCfg(t, "runtime:\n  native:\n    broker_socket: /run/shinyhub-native-broker/control.sock\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.Native.BrokerSocket != "/run/shinyhub-native-broker/control.sock" {
		t.Fatal(cfg.Runtime.Native.BrokerSocket)
	}
	t.Setenv("SHINYHUB_RUNTIME_NATIVE_BROKER_SOCKET", "/run/other/control.sock")
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.Native.BrokerSocket != "/run/other/control.sock" {
		t.Fatal("environment did not override YAML")
	}
}

func TestNativeBrokerRejectsRelativeSocket(t *testing.T) {
	path := writeIsolationCfg(t, "runtime:\n  native:\n    broker_socket: ./control.sock\n")
	if _, err := config.Load(path); err == nil {
		t.Fatal("relative broker socket was accepted")
	}
}
