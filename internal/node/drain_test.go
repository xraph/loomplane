package node

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xraph/loomplane/pkg/client"
)

// drainAPI is a Loomplane whose node web-1 runs a service and a database
// without backups on its server.
func drainAPI(t *testing.T, applied *atomic.Value) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []client.Node{{ID: "node_a", Name: "web-1", Status: "online"}}})
	})
	mux.HandleFunc("POST /api/v1/nodes/{id}/drain", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(client.DrainReview{ServerID: "srv_1", Review: "digest-1", Reservations: 1,
			Usage: &client.ServerUsage{Workloads: []client.ServerWorkload{{ProjectID: "prj_1", EnvironmentID: "env_1", Service: "api", Phase: "serving"}}}})
	})
	mux.HandleFunc("GET /api/v1/nodes/{id}/workloads", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"workloads": []client.NodeWorkload{
			{Service: "api", Kind: "service", Environment: "production"},
			{Service: "db", Kind: "database", Environment: "production"},
		}})
	})
	mux.HandleFunc("POST /api/v1/nodes/{id}/drain/apply", func(w http.ResponseWriter, req *http.Request) {
		var in client.InfrastructureApplyInput
		_ = json.NewDecoder(req.Body).Decode(&in)
		applied.Store(req.PathValue("id") + ":" + in.Review)
		_ = json.NewEncoder(w).Encode(client.Node{ID: "node_a", Name: "web-1", Status: "draining"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDrainCommandShowsTheReviewThenDrains(t *testing.T) {
	var applied atomic.Value
	srv := drainAPI(t, &applied)
	r := newCmdRig(t)
	r.opts.Login = func() (string, string, error) { return srv.URL, secretKey, nil }

	// No terminal and no --yes: nothing is drained.
	if err := r.run("node", "drain", "web-1"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("drain without a terminal: %v", err)
	}
	if applied.Load() != nil {
		t.Fatal("drained without asking")
	}
	out := r.out.String()
	for _, want := range []string{"srv_1", "1 reservation", "api", "serving", "The database db (production) has no backups. If this machine is lost, its data is lost."} {
		if !strings.Contains(out, want) {
			t.Fatalf("review lacks %q:\n%s", want, out)
		}
	}

	// Anything but yes keeps it.
	r.out.Reset()
	r.opts.isTerminal = func() bool { return true }
	r.opts.in = strings.NewReader("n\n")
	if err := r.run("node", "drain", "web-1"); err != nil || applied.Load() != nil || !strings.Contains(r.out.String(), "Not drained") {
		t.Fatalf("answered no: %v %v\n%s", err, applied.Load(), r.out)
	}

	r.opts.in = strings.NewReader("y\n")
	if err := r.run("node", "drain", "web-1"); err != nil || applied.Load() != "node_a:digest-1" {
		t.Fatalf("answered yes: %v %v\n%s", err, applied.Load(), r.out)
	}
	if !strings.Contains(r.out.String(), "web-1 is draining") {
		t.Fatalf("output:\n%s", r.out)
	}
}
