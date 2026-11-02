package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/forge"
	"github.com/xraph/keysmith"
	"github.com/xraph/keysmith/store/memory"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
)

type joinedStub struct {
	nodes []nodes.Node
	err   error
	asked int
}

func (j *joinedStub) JoinedByKey(_ context.Context, _ security.Principal, _ string) ([]nodes.Node, error) {
	j.asked++
	return j.nodes, j.err
}

type keyRouteAdmins struct{}

func (keyRouteAdmins) Allowed(_ context.Context, p security.Principal, _ security.Permission) (bool, error) {
	return p.UserID == "owner" || p.UserID == "admin", nil
}

func compromisedFixture(t *testing.T, stub *joinedStub) (forge.Router, string) {
	t.Helper()
	engine, err := keysmith.NewEngine(keysmith.WithStore(memory.New()))
	if err != nil {
		t.Fatal(err)
	}
	keys := apikeys.New(engine, "app", keyRouteAdmins{}, nil, apikeys.WithPlatformAdmins(adminsOnly{}))
	created, err := keys.Create(t.Context(), security.Principal{UserID: "owner", OrgID: "org"}, apikeys.NewKey{Name: "ci", Access: apikeys.Write})
	if err != nil {
		t.Fatal(err)
	}
	e := &Extension{BaseExtension: forge.NewBaseExtension("test", "test", ""), keys: keys, joined: func() JoinedNodes { return stub }}
	e.SetLogger(forge.NewNoopLogger())
	r := forge.NewRouter()
	if err = e.keyRoutesWith(r); err != nil {
		t.Fatal(err)
	}
	return r, created.Key.ID
}

type adminsOnly struct{}

func (adminsOnly) IsPlatformAdmin(_ context.Context, userID string) (bool, error) {
	return userID == "admin", nil
}

func revokeAs(t *testing.T, r forge.Router, user, id, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/api-keys/"+id+query, nil)
	req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{UserID: user, OrgID: "org"}))
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}

func TestRevokeCompromisedListsNodes(t *testing.T) {
	stub := &joinedStub{nodes: []nodes.Node{{ID: "node_1", Name: "gpu-1", PublicKey: []byte("secret-bytes"), KeyFingerprint: "fp"}}}
	r, id := compromisedFixture(t, stub)
	out := revokeAs(t, r, "owner", id, "?compromised=true")
	if out.Code != http.StatusOK {
		t.Fatalf("status %d: %s", out.Code, out.Body)
	}
	var got struct {
		Revoked     apikeys.APIKey `json:"revoked"`
		JoinedNodes []nodes.Node   `json:"joined_nodes"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Revoked.ID != id || got.Revoked.State != "revoked" || len(got.JoinedNodes) != 1 || got.JoinedNodes[0].ID != "node_1" {
		t.Fatalf("body: %s", out.Body)
	}
	if !strings.Contains(out.Body.String(), `"joined_nodes_withheld":false`) {
		t.Fatalf("body: %s", out.Body)
	}
	if strings.Contains(out.Body.String(), "public_key") || strings.Contains(out.Body.String(), "secret-bytes") {
		t.Fatalf("the node's key leaked: %s", out.Body)
	}
}

func TestRevokeWithoutCompromisedKeepsNoContent(t *testing.T) {
	stub := &joinedStub{}
	r, id := compromisedFixture(t, stub)
	if out := revokeAs(t, r, "owner", id, ""); out.Code != http.StatusNoContent || stub.asked != 0 {
		t.Fatalf("status %d, asked %d", out.Code, stub.asked)
	}
}

func TestRevokeCompromisedIsForTheOwnerOrAnAdmin(t *testing.T) {
	stub := &joinedStub{nodes: []nodes.Node{{ID: "node_1"}}}
	r, id := compromisedFixture(t, stub)
	out := revokeAs(t, r, "stranger", id, "?compromised=true")
	if out.Code != http.StatusNotFound || stub.asked != 0 || strings.Contains(out.Body.String(), "node_1") {
		t.Fatalf("a stranger got %d, asked %d: %s", out.Code, stub.asked, out.Body)
	}
	if out = revokeAs(t, r, "admin", id, "?compromised=true"); out.Code != http.StatusOK || stub.asked != 1 {
		t.Fatalf("an admin got %d, asked %d: %s", out.Code, stub.asked, out.Body)
	}
}

func TestRevokeCompromisedWithNoNodesSaysSo(t *testing.T) {
	r, id := compromisedFixture(t, &joinedStub{})
	out := revokeAs(t, r, "owner", id, "?compromised=true")
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"joined_nodes":[]`) {
		t.Fatalf("status %d: %s", out.Code, out.Body)
	}
}

// The key is revoked by the time the list is read, so a list that can't be
// read is withheld, never a failed revoke.
func TestRevokeCompromisedWithholdsAListItCannotRead(t *testing.T) {
	for name, err := range map[string]error{"forbidden": security.ErrForbidden, "store down": errors.New("connection refused")} {
		t.Run(name, func(t *testing.T) {
			r, id := compromisedFixture(t, &joinedStub{err: err})
			out := revokeAs(t, r, "owner", id, "?compromised=true")
			if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"joined_nodes":[]`) || !strings.Contains(out.Body.String(), `"joined_nodes_withheld":true`) ||
				!strings.Contains(out.Body.String(), `"state":"revoked"`) {
				t.Fatalf("status %d: %s", out.Code, out.Body)
			}
		})
	}
}
