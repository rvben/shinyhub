// internal/worker/client_register_test.go
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

func TestRegister_503MapsToUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := Register(context.Background(), srv.URL, workerapi.RegisterRequest{Token: "t"}, nil)
	if !errors.Is(err, ErrControlPlaneUnavailable) {
		t.Fatalf("err = %v, want ErrControlPlaneUnavailable", err)
	}
}

func TestRegister_401IsNotUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := Register(context.Background(), srv.URL, workerapi.RegisterRequest{Token: "t"}, nil)
	if err == nil {
		t.Fatal("expected an error on 401")
	}
	if errors.Is(err, ErrControlPlaneUnavailable) {
		t.Fatal("401 must not map to ErrControlPlaneUnavailable (it is permanent)")
	}
}

func TestRegister_200Decodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(workerapi.RegisterResponse{NodeID: "n", CertPEM: "c", CABundle: "ca"})
	}))
	defer srv.Close()

	resp, err := Register(context.Background(), srv.URL, workerapi.RegisterRequest{Token: "t"}, nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if resp.NodeID != "n" {
		t.Fatalf("node id = %q, want n", resp.NodeID)
	}
}
