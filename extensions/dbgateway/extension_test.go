package dbgateway

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/xraph/chronicle"
	"github.com/xraph/chronicle/audit"
	"github.com/xraph/chronicle/store"
	"github.com/xraph/chronicle/store/memory"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/dbgateway"
	"github.com/xraph/loomplane/internal/security"
)

// memChronicle is a Chronicle over an in-memory store.
func memChronicle(t *testing.T) (*chronicle.Chronicle, *memory.Store) {
	t.Helper()
	mem := memory.New()
	c, err := chronicle.New(chronicle.WithStore(store.NewAdapter(mem)))
	if err != nil {
		t.Fatal(err)
	}
	return c, mem
}

// auditedRouteChanges lists the route changes in mem, oldest first, as
// "action user project environment gateway route resource state".
func auditedRouteChanges(t *testing.T, mem *memory.Store) []string {
	t.Helper()
	res, err := mem.Query(t.Context(), &audit.Query{AppID: "app_1", Order: "asc"})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(res.Events))
	for _, e := range res.Events {
		if e.Resource != "db_gateway_route" {
			continue
		}
		out = append(out, strings.Join([]string{
			e.Action, e.UserID, fmt.Sprint(e.Metadata["project_id"]), fmt.Sprint(e.Metadata["environment_id"]),
			fmt.Sprint(e.Metadata["gateway_id"]), e.ResourceID, fmt.Sprint(e.Metadata["resource"]), fmt.Sprint(e.Metadata["state"]),
		}, " "))
	}
	return out
}

// The Chronicle auditor records which project, environment and gateway a
// route change belongs to, in the organization's stream, and never a key
// or a salt.
func TestAuditorRecordsTheGatewayAndProject(t *testing.T) {
	trail, mem := memChronicle(t)
	signing, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sealing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gw := &dbgateway.Gateway{
		OrgID: "org_1", ID: "dbgw_1", ProjectID: "proj_1", EnvironmentID: "env_1", Name: "main",
		Provider: "cloud-run", Region: "us-central1", Suffix: "us-central1.db.example.com",
		SigningPublicKey: signing, SealingPublicKey: sealing.PublicKey(),
	}
	salt := []byte("0123456789abcdef")
	route := &dbgateway.RouteRecord{ID: "dbgr_1", Resource: "orders", State: "paused", Salt: salt}
	a := auditor{chronicle: trail, appID: "app_1", log: forge.NewNoopLogger()}
	a.Record(t.Context(), dbgateway.AuditRoutePause, security.Principal{OrgID: "org_1", UserID: "usr_admin"}, gw, route)

	want := []string{"dbGateway.route.pause usr_admin proj_1 env_1 dbgw_1 dbgr_1 orders paused"}
	if got := auditedRouteChanges(t, mem); !slices.Equal(got, want) {
		t.Fatalf("audited %v, want %v", got, want)
	}
	res, err := mem.Query(t.Context(), &audit.Query{AppID: "app_1"})
	if err != nil {
		t.Fatal(err)
	}
	e := res.Events[0]
	if e.TenantID != "org_1" || e.Category != "database_gateways" || e.Outcome != "success" {
		t.Fatalf("event %+v", e)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"salt", "key", "suffix", string(salt), base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(signing), base64.StdEncoding.EncodeToString(sealing.PublicKey().Bytes()),
	} {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(secret)) {
			t.Fatalf("the event carries %q: %s", secret, raw)
		}
	}
}
