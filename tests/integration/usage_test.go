//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/usage"
)

// The usage API reads spend from a connected account's own billing, says
// why an account can't report, and refuses a range it doesn't know.
func TestUsage_readsSpendAndTrafficFromConnectedClouds(t *testing.T) {
	const token = "dop_v1_0123456789abcdef"
	do := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/owners" && r.Header.Get("Authorization") == "Bearer rnd_x" {
			_, _ = w.Write([]byte(`[{"owner":{"id":"tea-acme","name":"Acme","type":"team"}}]`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v2/account":
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct-1","email":"ada@example.com","status":"active","team":{"uuid":"team-acme","name":"Acme"}}}`))
		case "/v2/customers/my/balance":
			_, _ = w.Write([]byte(`{"month_to_date_usage":"11.21"}`))
		default:
			t.Errorf("asked DigitalOcean %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer do.Close()
	cfg := config.Defaults()
	cfg.Database.URL = freshDatabase(t)
	cfg.Clouds.DigitalOceanEndpoint = do.URL
	cfg.Clouds.RenderEndpoint = do.URL
	cfg.Storage.URL = "file://" + t.TempDir()
	r := start(t, cfg)
	defer r.stop()
	c := &client{t: t, base: r.srv.URL, key: cfg.Identity.PublishableKey}

	c.must(http.StatusUnauthorized, http.MethodGet, "/api/v1/usage/spend", nil, nil)
	signedIn(t, c)
	if path := os.Getenv("LOOMPLANE_TEST_OPENAPI_OUTPUT"); path != "" {
		var contract json.RawMessage
		c.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &contract)
		if err := os.WriteFile(path, contract, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "digitalocean", "name": "DigitalOcean", "credential": token}, nil)
	c.must(http.StatusCreated, http.MethodPost, "/api/v1/connections",
		map[string]string{"cloud": "render", "name": "Render", "credential": "rnd_x"}, nil)

	var spend usage.SpendReport
	c.must(http.StatusOK, http.MethodGet, "/api/v1/usage/spend", nil, &spend)
	if len(spend.Totals) != 1 || spend.Totals[0].Currency != "USD" || spend.Totals[0].Total != 11.21 {
		t.Fatalf("totals %+v", spend.Totals)
	}
	if len(spend.Accounts) != 1 || spend.Accounts[0].Name != "DigitalOcean" {
		t.Fatalf("accounts %+v", spend.Accounts)
	}
	statuses := map[string]string{}
	for _, s := range spend.Sources {
		statuses[s.Name] = s.Status
	}
	if statuses["DigitalOcean"] != usage.Reporting || statuses["Render"] != usage.Unavailable {
		t.Fatalf("sources %+v", spend.Sources)
	}

	var traffic usage.TrafficReport
	c.must(http.StatusOK, http.MethodGet, "/api/v1/usage/traffic?range=24h", nil, &traffic)
	if traffic.Range != "24h" || len(traffic.Requests) != 48 || traffic.Total != 0 || len(traffic.Services) != 0 {
		t.Fatalf("traffic %+v", traffic)
	}
	c.must(http.StatusUnprocessableEntity, http.MethodGet, "/api/v1/usage/traffic?range=1y", nil, nil)
}
