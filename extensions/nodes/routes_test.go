package nodes

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/apikeys"
	"github.com/xraph/loomplane/internal/nodes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/nodeproto"
)

func TestListNeedsNodeRead(t *testing.T) {
	r := newRig(t, "usr_1:nodes:read")
	r.seed(t)
	out := r.do(t, http.MethodGet, "/api/v1/nodes", "", &member)
	if out.Code != http.StatusForbidden {
		t.Fatalf("without nodes:read: %d %s", out.Code, out.Body)
	}
	if out = r.do(t, http.MethodGet, "/api/v1/nodes", "", nil); out.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", out.Code)
	}

	r = newRig(t)
	r.seed(t)
	out = r.do(t, http.MethodGet, "/api/v1/nodes", "", &member)
	if out.Code != http.StatusOK {
		t.Fatalf("list: %d %s", out.Code, out.Body)
	}
	var got struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || len(got.Nodes) != 1 || got.Nodes[0]["id"] != "node_1" || got.Nodes[0]["key_fingerprint"] == "" {
		t.Fatalf("body %s (%v)", out.Body, err)
	}
	if _, leaked := got.Nodes[0]["public_key"]; leaked {
		t.Fatalf("public_key in %s", out.Body)
	}
	if out = r.do(t, http.MethodGet, "/api/v1/nodes/node_1", "", &member); out.Code != http.StatusOK {
		t.Fatalf("get: %d %s", out.Code, out.Body)
	}
	if out = r.do(t, http.MethodGet, "/api/v1/nodes/node_404", "", &member); out.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", out.Code)
	}
	other := security.Principal{UserID: "usr_9", OrgID: "org_2"}
	if out = r.do(t, http.MethodGet, "/api/v1/nodes/node_1", "", &other); out.Code != http.StatusNotFound {
		t.Fatalf("another org's node: %d", out.Code)
	}
}

func TestJoinTokenRouteReturnsCommand(t *testing.T) {
	r := newRig(t)
	body := `{"labels":{"zone":"a"},"endpoints":["probe","docker"],"edge":false,"lifetime_minutes":15}`
	out := r.do(t, http.MethodPost, "/api/v1/nodes/join-tokens", body, &member)
	if out.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", out.Code, out.Body)
	}
	var got struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
		Command   string `json:"command"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// The token rides in the environment, not argv, and the scope's
	// endpoints become --allow, since the pipe leaves join no terminal to ask on.
	want := "curl -fsSL https://loom.example.com/install/node.sh | LOOMPLANE_JOIN_TOKEN=lpj_live_abc123 sh -s -- --allow probe,docker"
	if got.Token != "lpj_live_abc123" || got.Command != want || got.ExpiresAt != "2026-11-02T12:00:00Z" {
		t.Fatalf("body %s", out.Body)
	}
	if r.minter.who != member || r.minter.life.Minutes() != 15 || r.minter.scope.Labels["zone"] != "a" ||
		len(r.minter.scope.Endpoints) != 2 || r.minter.scope.Endpoints[1] != nodeproto.EndpointDocker {
		t.Fatalf("minted with %+v for %+v over %v", r.minter.scope, r.minter.who, r.minter.life)
	}

	// A trailing slash on the public URL does not double up, and a token
	// with no endpoints in its scope gets no --allow.
	want = "curl -fsSL https://loom.example.com/install/node.sh | LOOMPLANE_JOIN_TOKEN=lpj_live_abc123 sh -s"
	r.ext.cfg.PublicURL = publicURL + "/"
	out = r.do(t, http.MethodPost, "/api/v1/nodes/join-tokens", `{}`, &member)
	if !strings.Contains(out.Body.String(), `"command":"`+want+`"`) {
		t.Fatalf("with a trailing slash: %s", out.Body)
	}
	if out = r.do(t, http.MethodPost, "/api/v1/nodes/join-tokens", `{}`, nil); out.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", out.Code)
	}
	r.minter.err = security.ErrForbidden
	if out = r.do(t, http.MethodPost, "/api/v1/nodes/join-tokens", `{}`, &member); out.Code != http.StatusForbidden {
		t.Fatalf("forbidden maker: %d", out.Code)
	}
	r.minter.err = fmt.Errorf("%w: nope", apikeys.ErrInvalid)
	if out = r.do(t, http.MethodPost, "/api/v1/nodes/join-tokens", `{}`, &member); out.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid scope: %d", out.Code)
	}
}

func joinBody(t *testing.T, overrides map[string]any) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{
		"token": "lpj_live_x", "public_key": base64.StdEncoding.EncodeToString(pub), "name": "web-1",
		"endpoints": []string{"probe"}, "node_version": "v1.8.0", "protocol_version": nodeproto.Version,
		"inventory": map[string]any{"os": "linux"},
	}
	for k, v := range overrides {
		m[k] = v
	}
	if _, set := overrides["signature"]; !set {
		token, _ := m["token"].(string) //nolint:errcheck // a test override of another type signs as ""
		name, _ := m["name"].(string)   //nolint:errcheck // as above
		m["signature"] = base64.StdEncoding.EncodeToString(nodeproto.SignJoin(priv, token, name))
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The token in the body is the only credential: no bearer, no session.
func TestJoinRouteNoSession(t *testing.T) {
	r := newRig(t)
	out := r.do(t, http.MethodPost, "/api/v1/nodes/join", joinBody(t, nil), nil)
	if out.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", out.Code, out.Body)
	}
	var got struct {
		NodeID string `json:"node_id"`
		OrgID  string `json:"org_id"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || got.OrgID != "org_1" || !strings.HasPrefix(got.NodeID, "node_") {
		t.Fatalf("body %s (%v)", out.Body, err)
	}
	if n, err := r.store.GetByID(t.Context(), got.NodeID); err != nil || n.Name != "web-1" || n.Status != nodes.StatusPending {
		t.Fatalf("stored %+v %v", n, err)
	}
}

func TestJoinRouteErrors(t *testing.T) {
	hint := "mint a new join token"
	for _, tc := range []struct {
		name      string
		err       error
		body      map[string]any
		status    int
		mustSay   string
		mustNotUp int // tokens spent
	}{
		{name: "rejected", err: apikeys.ErrRejected, status: http.StatusUnauthorized},
		{name: "unavailable", err: apikeys.ErrUnavailable, status: http.StatusServiceUnavailable, mustSay: hint},
		{name: "mint a new one", err: fmt.Errorf("%w: %w", nodes.ErrMintNewToken, errors.New("disk full")), status: http.StatusInternalServerError, mustSay: hint},
		{name: "must update", body: map[string]any{"protocol_version": 0}, status: http.StatusUpgradeRequired, mustSay: nodes.ErrUpdateNode.Error()},
		{name: "node newer", body: map[string]any{"protocol_version": nodeproto.Version + 1}, status: http.StatusUpgradeRequired, mustSay: nodes.ErrNodeNewer.Error()},
		{name: "bad key", body: map[string]any{"public_key": "AAAA"}, status: http.StatusUnprocessableEntity},
		{name: "unsigned", body: map[string]any{"signature": ""}, status: http.StatusUnprocessableEntity, mustSay: "signed"},
		{name: "signature not base64", body: map[string]any{"signature": "***"}, status: http.StatusUnprocessableEntity, mustSay: "signature"},
		{name: "signature for another key", body: map[string]any{"signature": base64.StdEncoding.EncodeToString(make([]byte, 64))}, status: http.StatusUnprocessableEntity, mustSay: "signed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			r.tokens.err = tc.err
			out := r.do(t, http.MethodPost, "/api/v1/nodes/join", joinBody(t, tc.body), nil)
			if out.Code != tc.status || !strings.Contains(out.Body.String(), tc.mustSay) {
				t.Fatalf("status %d: %s", out.Code, out.Body)
			}
			if tc.body != nil && r.tokens.spent != 0 {
				t.Fatalf("a refused join spent its token")
			}
		})
	}
	r := newRig(t)
	if out := r.do(t, http.MethodPost, "/api/v1/nodes/join", `not json`, nil); out.Code != http.StatusBadRequest {
		t.Fatalf("not json: %d", out.Code)
	}
	if out := r.do(t, http.MethodPost, "/api/v1/nodes/join", joinBody(t, map[string]any{"public_key": "***"}), nil); out.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key not base64: %d", out.Code)
	}
	huge := joinBody(t, map[string]any{"name": strings.Repeat("a", 2<<20)})
	if out := r.do(t, http.MethodPost, "/api/v1/nodes/join", huge, nil); out.Code != http.StatusRequestEntityTooLarge || r.tokens.spent != 0 {
		t.Fatalf("oversized: %d", out.Code)
	}
}

// A name a live node holds is refused before the token is spent, and the
// operator is told which name and what to do.
func TestJoinRouteTakenNameKeepsTheToken(t *testing.T) {
	r := newRig(t)
	r.seed(t)
	out := r.do(t, http.MethodPost, "/api/v1/nodes/join", joinBody(t, nil), nil)
	var msg struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &msg); err != nil || out.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", out.Code, out.Body)
	}
	for _, want := range []string{`"web-1"`, "--name", "not used"} {
		if !strings.Contains(msg.Error, want) {
			t.Fatalf("message %q lacks %q", msg.Error, want)
		}
	}
	if r.tokens.spent != 0 {
		t.Fatal("a join with a taken name spent its token")
	}
}

func TestPatchWidenIs422(t *testing.T) {
	r := newRig(t)
	r.seed(t, nodeproto.EndpointProbe)
	out := r.do(t, http.MethodPatch, "/api/v1/nodes/node_1", `{"endpoints":["probe","docker"]}`, &member)
	var msg struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &msg); err != nil || out.Code != http.StatusUnprocessableEntity || msg.Error != nodes.ErrWidenRemotely.Error() {
		t.Fatalf("widen: %d %s", out.Code, out.Body)
	}
	out = r.do(t, http.MethodPatch, "/api/v1/nodes/node_1", `{"name":"web-2","endpoints":[]}`, &member)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"name":"web-2"`) || !strings.Contains(out.Body.String(), `"endpoints":[]`) {
		t.Fatalf("narrow and rename: %d %s", out.Code, out.Body)
	}
	if out = r.do(t, http.MethodPatch, "/api/v1/nodes/node_1", `{"name":"Not A Label"}`, &member); out.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad name: %d %s", out.Code, out.Body)
	}
	if out = r.do(t, http.MethodPatch, "/api/v1/nodes/node_1", `{}`, nil); out.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", out.Code)
	}
}

func TestRevokeRoute(t *testing.T) {
	r := newRig(t)
	r.seed(t)
	out := r.do(t, http.MethodPost, "/api/v1/nodes/node_1/revoke", "", &member)
	if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revoke: %d %s", out.Code, out.Body)
	}
	if out = r.do(t, http.MethodPost, "/api/v1/nodes/node_404/revoke", "", &member); out.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", out.Code)
	}
	r = newRig(t, "usr_1:nodes:manage")
	r.seed(t)
	if out = r.do(t, http.MethodPost, "/api/v1/nodes/node_1/revoke", "", &member); out.Code != http.StatusForbidden {
		t.Fatalf("without nodes:manage: %d", out.Code)
	}
}

func TestJoinTokenListAndCancelRoutes(t *testing.T) {
	r := newRig(t)
	exp := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	r.minter.pending = []apikeys.PendingNodeJoinToken{{ID: "key_1", Hint: "c123", MintedBy: "usr_1", MintedByKeyID: "key_9",
		Endpoints: []nodeproto.Endpoint{nodeproto.EndpointProbe}, CreatedAt: exp.Add(-time.Hour), ExpiresAt: exp}}
	// /join-tokens is its own route, not a node called "join-tokens".
	out := r.do(t, http.MethodGet, "/api/v1/nodes/join-tokens", "", &member)
	if out.Code != http.StatusOK {
		t.Fatalf("list: %d %s", out.Code, out.Body)
	}
	var got struct {
		Tokens []map[string]any `json:"tokens"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || len(got.Tokens) != 1 {
		t.Fatalf("body %s (%v)", out.Body, err)
	}
	if got.Tokens[0]["minted_by_key_id"] != "key_9" || got.Tokens[0]["expires_at"] != "2026-11-02T12:00:00Z" {
		t.Fatalf("token %v", got.Tokens[0])
	}
	if strings.Contains(out.Body.String(), "lpj_") || got.Tokens[0]["secret"] != nil || got.Tokens[0]["token"] != nil {
		t.Fatalf("a secret in %s", out.Body)
	}
	r.minter.pending = nil
	if out = r.do(t, http.MethodGet, "/api/v1/nodes/join-tokens", "", &member); out.Code != http.StatusOK || !strings.Contains(out.Body.String(), `"tokens":[]`) {
		t.Fatalf("empty list: %d %s", out.Code, out.Body)
	}

	if out = r.do(t, http.MethodDelete, "/api/v1/nodes/join-tokens/key_1", "", &member); out.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", out.Code, out.Body)
	}
	if !slices.Equal(r.minter.cancelled, []string{"key_1"}) || r.minter.who != member {
		t.Fatalf("cancelled %v as %+v", r.minter.cancelled, r.minter.who)
	}
	for _, tc := range []struct {
		err    error
		status int
	}{
		{apikeys.ErrNotFound, http.StatusNotFound},
		{security.ErrForbidden, http.StatusForbidden},
		{apikeys.ErrUnavailable, http.StatusServiceUnavailable},
	} {
		r.minter.err = tc.err
		if out = r.do(t, http.MethodDelete, "/api/v1/nodes/join-tokens/key_1", "", &member); out.Code != tc.status {
			t.Errorf("cancel with %v: %d", tc.err, out.Code)
		}
		if out = r.do(t, http.MethodGet, "/api/v1/nodes/join-tokens", "", &member); tc.err != apikeys.ErrNotFound && out.Code != tc.status {
			t.Errorf("list with %v: %d", tc.err, out.Code)
		}
	}
	r.minter.err = nil
	if out = r.do(t, http.MethodGet, "/api/v1/nodes/join-tokens", "", nil); out.Code != http.StatusUnauthorized {
		t.Fatalf("list with no session: %d", out.Code)
	}
	if out = r.do(t, http.MethodDelete, "/api/v1/nodes/join-tokens/key_1", "", nil); out.Code != http.StatusUnauthorized {
		t.Fatalf("cancel with no session: %d", out.Code)
	}
}
