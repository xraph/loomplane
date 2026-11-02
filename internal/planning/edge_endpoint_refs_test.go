package planning

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
)

type edgePinAuthorizer struct {
	denied, revoked bool
	actor           security.Principal
	permissions     []security.Permission
}

func (a *edgePinAuthorizer) Require(_ context.Context, p security.Principal, perm security.Permission, _ string) error {
	a.actor = p
	a.permissions = append(a.permissions, perm)
	if a.denied {
		return errors.New("private denied detail")
	}
	return nil
}
func (a *edgePinAuthorizer) RequireCurrentMembership(context.Context, security.Principal) error {
	if a.revoked {
		return security.ErrNotMember
	}
	return nil
}

type edgePinEndpoints struct {
	rows  map[string]*catalogconnect.Endpoint
	reads []string
}

func (e *edgePinEndpoints) Endpoint(_ context.Context, _ string, id string) (*catalogconnect.Endpoint, error) {
	e.reads = append(e.reads, id)
	if row := e.rows[id]; row != nil {
		return row, nil
	}
	return nil, catalogconnect.ErrNotFound
}
func (*edgePinEndpoints) Check(context.Context, string, string, recipes.Requirement, catalogconnect.DialFunc) (*catalogconnect.Report, error) {
	return nil, errors.New("endpoint pinning must read metadata only")
}

func TestResourceEndpointPinsAuthorizeActorAndPinEnvironmentMetadata(t *testing.T) {
	auth := &edgePinAuthorizer{}
	endpoints := &edgePinEndpoints{rows: map[string]*catalogconnect.Endpoint{"production-origin": {ID: "production-origin", OrgID: "org", Revision: 7, Engine: catalogconnect.EnginePostgres, Host: "db.example.com", Database: "app", Username: "private-username", TLS: catalogconnect.TLSVerifyFull}}}
	service := &Service{authz: auth, endpoints: endpoints}
	m := &manifest.Manifest{Resources: map[string]manifest.Resource{"pool": {Kind: "cloudflare-hyperdrive", Settings: map[string]string{"origin_endpoint": "dev-origin"}, Environments: map[string]manifest.ResourceSettings{"production": {Settings: map[string]string{"origin_endpoint": "production-origin"}}}}}}
	got := service.resourceEndpointPins(context.Background(), &Plan{OrgID: "org", CreatedBy: "creator", DecidedBy: "approver", Environment: "production"}, m)
	pin, ok := got["production-origin"]
	if !ok || pin.ID != "production-origin" || pin.Revision != 7 || pin.Engine != "postgres" || pin.Host != "db.example.com" || pin.Database != "app" {
		t.Fatalf("wrong pin %#v", got)
	}
	if len(endpoints.reads) != 1 || endpoints.reads[0] != "production-origin" || auth.actor.UserID != "approver" || auth.actor.OrgID != "org" || len(auth.permissions) != 1 || auth.permissions[0] != security.SecretManage {
		t.Fatalf("wrong authorization or metadata read: %#v %#v", auth, endpoints)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "private-username") {
		t.Fatal("endpoint credential metadata entered the plan")
	}
}

func TestResourceEndpointPinsExcludeUnauthorizedRevokedAndUnverifiedOrigins(t *testing.T) {
	m := &manifest.Manifest{Resources: map[string]manifest.Resource{"pool": {Kind: "cloudflare-hyperdrive", Settings: map[string]string{"origin_endpoint": "origin"}}}}
	plan := &Plan{OrgID: "org", CreatedBy: "creator", Environment: "production"}
	for _, scenario := range []string{"denied", "revoked", "missing", "wrong-org", "weak-tls", "wrong-engine", "no-actor"} {
		t.Run(scenario, func(t *testing.T) {
			row := &catalogconnect.Endpoint{ID: "origin", OrgID: "org", Revision: 1, Engine: catalogconnect.EnginePostgres, Host: "db.example.com", TLS: catalogconnect.TLSVerifyFull}
			auth := &edgePinAuthorizer{denied: scenario == "denied", revoked: scenario == "revoked"}
			endpoint := &edgePinEndpoints{rows: map[string]*catalogconnect.Endpoint{"origin": row}}
			copyPlan := *plan
			switch scenario {
			case "missing":
				delete(endpoint.rows, "origin")
			case "wrong-org":
				row.OrgID = "other"
			case "weak-tls":
				row.TLS = catalogconnect.TLSRequire
			case "wrong-engine":
				row.Engine = catalogconnect.EngineRedis
			case "no-actor":
				copyPlan.CreatedBy = ""
			}
			got := (&Service{authz: auth, endpoints: endpoint}).resourceEndpointPins(context.Background(), &copyPlan, m)
			if len(got) != 0 {
				t.Fatalf("unsafe origin pinned: %#v", got)
			}
			if (scenario == "denied" || scenario == "revoked" || scenario == "no-actor") && len(endpoint.reads) != 0 {
				t.Fatalf("metadata read before authorization: %v", endpoint.reads)
			}
		})
	}
}
