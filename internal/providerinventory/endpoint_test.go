package providerinventory

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestVerifyEndpointSupportsOnlyInstalledRenderAndPinsOwner(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/postgres/pg-1/connection-info" {
			w.Write([]byte(`{"externalConnectionString":"postgres://app:SECRET@db.test/main"}`))
			return
		}
		if r.URL.Path != "/postgres/pg-1" || r.Method != "GET" {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		w.Write([]byte(`{"id":"pg-1","createdAt":"created","owner":{"id":"tea-1"}}`))
	}))
	defer srv.Close()
	c := &Clients{Render: render.New(srv.URL)}
	if !c.SupportsEndpointVerification("render") || c.SupportsEndpointVerification("aws") || (&Clients{}).SupportsEndpointVerification("render") {
		t.Error("unavailable capability advertised")
	}
	selected := provider.ResourceObservation{ID: "pg-1", Incarnation: "created", Kind: "postgres"}
	for _, owner := range []string{"tea-1", "tea-2"} {
		got, err := c.VerifyEndpoint(t.Context(), &connections.Connection{Cloud: "render", Details: map[string]string{"owner_id": owner}}, []byte("token"), selected, "db.test", 5432)
		if owner == "tea-1" {
			if err != nil || got.Host != "db.test" || calls != 3 {
				t.Fatalf("%+v %v calls=%d", got, err, calls)
			}
		} else if err == nil || got.Host != "" || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%+v %v", got, err)
		}
	}
}
