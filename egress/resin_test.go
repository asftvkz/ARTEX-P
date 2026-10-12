package egress

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResinProbePaginatesAndReleaseIsIdempotent(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/api/v1/system/info" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"auth_version":"V1"}`)
			return
		}
		if r.URL.Path == "/api/v1/platforms" {
			if r.URL.Query().Get("offset") == "0" {
				items := make([]string, 0, 100)
				for i := 0; i < 100; i++ {
					items = append(items, fmt.Sprintf(`{"id":"other-%d","name":"Other"}`, i))
				}
				fmt.Fprintf(w, `{"total":101,"items":[%s]}`, strings.Join(items, ","))
				return
			}
			fmt.Fprint(w, `{"total":101,"items":[{"id":"target-id","name":"Default"}]}`)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/platforms/target-id/leases/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	cfg := ResinConfig{AdminURL: server.URL, AdminToken: "test-token", Platform: "Default"}
	if err := cfg.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Release(context.Background(), "task-1"); err != nil {
		t.Fatal(err)
	}
	if len(requests) < 5 {
		t.Fatalf("expected health, info, two platform pages, delete; got %v", requests)
	}
}

func TestResinRejectsUnsafeAdminURL(t *testing.T) {
	if err := (ResinConfig{AdminURL: "http://user:pass@example/"}).Probe(context.Background()); err == nil {
		t.Fatal("expected URL rejection")
	}
}
