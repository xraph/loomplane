package connections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/clouds/gcp"
)

// fakeIAM answers Google's token exchange and testIamPermissions,
// granting every permission but those it's told to hold back.
func fakeIAM(t *testing.T, withheld ...string) (*httptest.Server, *[][]string) {
	t.Helper()
	var asked [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Write([]byte(`{"access_token":"token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path != "/v3/projects/shop:testIamPermissions" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			return
		}
		var in struct {
			Permissions []string `json:"permissions"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		asked = append(asked, in.Permissions)
		var held []string
		for _, p := range in.Permissions {
			if !slices.Contains(withheld, p) {
				held = append(held, p)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"permissions": held})
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

// gcpKeyAt is a service account key that exchanges its token at srv.
func gcpKeyAt(t *testing.T, srv *httptest.Server) []byte {
	t.Helper()
	var key map[string]string
	if err := json.Unmarshal(gcpKey(t), &key); err != nil {
		t.Fatal(err)
	}
	key["token_uri"] = srv.URL + "/token"
	data, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A service account without what only a canary needs is complete: every
// deploy still works through it, and the connection says what a canary
// would lack.
func TestGCPCheckKeepsCanaryOnlyPermissionsOutOfMissing(t *testing.T) {
	srv, asked := fakeIAM(t, "run.revisions.delete", "run.revisions.get")
	c, err := GCPChecker{Client: gcp.New(srv.URL)}.Check(t.Context(), gcpKeyAt(t, srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Missing) != 0 || c.Details[DetailCanaryMissing] != "run.revisions.delete,run.revisions.get" {
		t.Fatalf("missing %v, details %v", c.Missing, c.Details)
	}
	if len(*asked) != 1 || !slices.Contains((*asked)[0], "run.revisions.delete") || !slices.Contains((*asked)[0], "run.services.update") {
		t.Fatalf("asked %v", *asked)
	}
	conn := &Connection{}
	apply(conn, c, conn.UpdatedAt)
	if conn.Status != StatusReady {
		t.Fatalf("status %s", conn.Status)
	}
}

func TestGCPCheckStillRequiresWhatEveryDeployNeeds(t *testing.T) {
	srv, _ := fakeIAM(t, "run.services.update", "run.revisions.get")
	c, err := GCPChecker{Client: gcp.New(srv.URL)}.Check(t.Context(), gcpKeyAt(t, srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Missing, []string{"run.services.update"}) || c.Details[DetailCanaryMissing] != "run.revisions.get" {
		t.Fatalf("missing %v, details %v", c.Missing, c.Details)
	}
	conn := &Connection{}
	apply(conn, c, conn.UpdatedAt)
	if conn.Status != StatusIncomplete {
		t.Fatalf("status %s", conn.Status)
	}
}

func TestGCPCheckWithEverythingRecordsNoCanaryGap(t *testing.T) {
	srv, _ := fakeIAM(t)
	c, err := GCPChecker{Client: gcp.New(srv.URL)}.Check(t.Context(), gcpKeyAt(t, srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Details[DetailCanaryMissing]; ok || len(c.Missing) != 0 {
		t.Fatalf("missing %v, details %v", c.Missing, c.Details)
	}
}
