package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rvben/shinyhub/internal/protocol"
)

// TestServerInfoMatchesVersionedProtocolContract makes an API compatibility
// decision explicit. Removing a field or changing its JSON type requires a new
// protocol version and a corresponding contract fixture. Additive fields stay
// compatible and therefore do not force a protocol bump.
func TestServerInfoMatchesVersionedProtocolContract(t *testing.T) {
	contractPath := filepath.Join("..", "protocol", "testdata",
		fmt.Sprintf("server-info-v%d.json", protocol.CurrentVersion))
	wantBytes, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("protocol %d has no server-info contract fixture at %s: %v",
			protocol.CurrentVersion, contractPath, err)
	}

	srv, _ := newTestServer(t)
	srv.SetVersion("v0.0.0-contract-test")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/server-info", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}

	var want, got any
	if err := json.Unmarshal(wantBytes, &want); err != nil {
		t.Fatalf("decode contract fixture: %v", err)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode server response: %v", err)
	}
	assertJSONContract(t, "$", want, got)

	wantProtocol := want.(map[string]any)["protocol_version"]
	if wantProtocol != float64(protocol.CurrentVersion) {
		t.Fatalf("fixture protocol_version = %v, want %d to match CurrentVersion",
			wantProtocol, protocol.CurrentVersion)
	}
	gotProtocol := got.(map[string]any)["protocol_version"]
	if gotProtocol != wantProtocol {
		t.Fatalf("response protocol_version = %v, contract fixture declares %v", gotProtocol, wantProtocol)
	}
}

func assertJSONContract(t *testing.T, path string, want, got any) {
	t.Helper()
	wantType := reflect.TypeOf(want)
	gotType := reflect.TypeOf(got)
	if wantType != gotType {
		t.Fatalf("%s changed JSON type from %v to %v; bump protocol.CurrentVersion and add a new fixture if this is intentional",
			path, wantType, gotType)
	}

	switch expected := want.(type) {
	case map[string]any:
		actual := got.(map[string]any)
		for key, child := range expected {
			value, ok := actual[key]
			if !ok {
				t.Fatalf("%s.%s was removed; bump protocol.CurrentVersion and add a new fixture if this is intentional", path, key)
			}
			assertJSONContract(t, path+"."+key, child, value)
		}
	}
}
