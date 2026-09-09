package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	workerapi "github.com/rvben/shinyhub/internal/worker/api"
)

func TestClientAuthorizeElastic(t *testing.T) {
	authority := workerapi.ElasticAuthority{ReservationID: "reservation", NodeID: "worker", Instance: "controller", Epoch: 1, RequestDigest: "digest", Action: "ack"}
	for _, status := range []int{204, 200, 401, 409, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/workers/elastic/authorize" {
					t.Errorf("request %s %s", r.Method, r.URL)
				}
				if r.Header.Get("User-Agent") != "shinyhub-worker" {
					t.Errorf("non-generic User-Agent %q", r.Header.Get("User-Agent"))
				}
				var got workerapi.ElasticAuthority
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				if got != authority {
					t.Errorf("authority=%+v", got)
				}
				w.WriteHeader(status)
				if status != 204 {
					_, _ = w.Write([]byte("private diagnostic"))
				}
			}))
			defer server.Close()
			client := &Client{serverURL: server.URL, httpc: server.Client()}
			err := client.AuthorizeElastic(context.Background(), authority)
			if (err == nil) != (status == 204) {
				t.Fatalf("status %d error %v", status, err)
			}
			if err != nil && strings.Contains(err.Error(), "private diagnostic") {
				t.Fatal("server details exposed")
			}
		})
	}
}
