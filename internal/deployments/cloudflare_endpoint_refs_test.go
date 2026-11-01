package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/cloudflare"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

type cfEndpointAuthorizer struct {
	deny, revoked      bool
	calls, memberships int
	actor              security.Principal
}

func (a *cfEndpointAuthorizer) Require(_ context.Context, p security.Principal, permission security.Permission, _ string) error {
	a.calls++
	a.actor = p
	if permission != security.SecretManage {
		return errors.New("wrong permission")
	}
	if a.deny {
		return errors.New("private access denial")
	}
	return nil
}
func (a *cfEndpointAuthorizer) RequireCurrentMembership(context.Context, security.Principal) error {
	a.memberships++
	if a.revoked {
		return security.ErrNotMember
	}
	return nil
}

type cfEndpointResolver struct {
	row    *catalogconnect.Endpoint
	dsn    string
	calls  int
	change bool
}

func (e *cfEndpointResolver) Get(context.Context, string, string) (*catalogconnect.Endpoint, error) {
	row := *e.row
	return &row, nil
}

func (e *cfEndpointResolver) Resolve(context.Context, string, string) (*catalogconnect.Endpoint, string, error) {
	e.calls++
	row := *e.row
	if e.change && e.calls > 1 {
		row.Revision++
	}
	return &row, e.dsn, nil
}
func cfEndpointJob(t *testing.T) *Job {
	job := cfWorkerJob(t)
	job.Plan.OrgID = "org"
	job.Plan.CreatedBy = "creator"
	job.Plan.DecidedBy = "approver"
	job.Action.Service = nil
	job.Action.Resource = &planning.ResourceConfig{Kind: "cloudflare-hyperdrive", Settings: map[string]string{"origin_endpoint": "origin", "ca_certificate_id": cfVersion}, EndpointPins: map[string]planning.EndpointPin{"origin_endpoint": {ID: "origin", Revision: 7, Engine: "postgres", Host: "db.example.com", Database: "app"}}}
	job.Keep = func(context.Context, string, []byte) error { return nil }
	return job
}
func cfOriginEndpoint() *cfEndpointResolver {
	return &cfEndpointResolver{row: &catalogconnect.Endpoint{ID: "origin", OrgID: "org", Revision: 7, Engine: catalogconnect.EnginePostgres, Host: "db.example.com", Port: 5432, Database: "app", TLS: catalogconnect.TLSVerifyFull}, dsn: "postgres://app:private-password@db.example.com:5432/app?sslmode=verify-full"}
}

func TestCloudflareEndpointUseRequiresCurrentActorAndExactReviewedPin(t *testing.T) {
	for _, scenario := range []string{"allowed", "denied", "revoked", "missing-pin", "revision-changed", "host-changed", "database-changed", "weak-tls", "dsn-host-changed"} {
		t.Run(scenario, func(t *testing.T) {
			job := cfEndpointJob(t)
			auth := &cfEndpointAuthorizer{deny: scenario == "denied", revoked: scenario == "revoked"}
			endpoints := cfOriginEndpoint()
			switch scenario {
			case "missing-pin":
				job.Action.Resource.EndpointPins = nil
			case "revision-changed":
				endpoints.row.Revision++
			case "host-changed":
				endpoints.row.Host = "other.example.com"
			case "database-changed":
				endpoints.row.Database = "other"
			case "weak-tls":
				endpoints.row.TLS = catalogconnect.TLSRequire
			case "dsn-host-changed":
				endpoints.dsn = "postgres://app:private-password@other.example.com:5432/app?sslmode=verify-full"
			}
			exec := &Cloudflare{Authorizer: auth, Endpoints: endpoints}
			origin, err := exec.cloudflareHyperdriveOrigin(context.Background(), job, "origin")
			if scenario == "allowed" {
				if err != nil || origin.Host != "db.example.com" || auth.actor.UserID != "approver" || auth.memberships == 0 || auth.calls == 0 {
					t.Fatalf("valid origin %#v %v authorization %#v", origin, err, auth)
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "private-password") {
					t.Fatalf("unsafe origin accepted/error leaked: %#v %v", origin, err)
				}
			}
			if (scenario == "denied" || scenario == "revoked" || scenario == "missing-pin" || scenario == "revision-changed" || scenario == "host-changed" || scenario == "database-changed" || scenario == "weak-tls") && endpoints.calls != 0 {
				t.Fatalf("endpoint secret read before pin/permission gate: %d", endpoints.calls)
			}
		})
	}
}

func TestCloudflareHyperdriveRechecksEndpointImmediatelyBeforeProviderPost(t *testing.T) {
	job := cfEndpointJob(t)
	endpoints := cfOriginEndpoint()
	endpoints.change = true
	posts := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
			t.Error("provisioned using a changed endpoint revision")
		}
		fmt.Fprint(w, `{"success":true,"result":[]}`)
	}))
	defer s.Close()
	_, err := (&Cloudflare{Client: cloudflare.New(s.URL, false), Authorizer: &cfEndpointAuthorizer{}, Endpoints: endpoints}).Apply(context.Background(), job)
	if err == nil || posts != 0 || endpoints.calls < 2 {
		t.Fatalf("endpoint not rechecked: calls=%d posts=%d err=%v", endpoints.calls, posts, err)
	}
}

func TestCloudflareManagedApplicationOutputsUseReviewedCredentialMaterial(t *testing.T) {
	job := cfEndpointJob(t)
	job.Action.Resource = &planning.ResourceConfig{Kind: "cloudflare-kv", Settings: map[string]string{"application_endpoint": "application"}, EndpointPins: map[string]planning.EndpointPin{"application_endpoint": {ID: "application", Revision: 7, Engine: "cloudflare-kv", Host: "api.cloudflare.com", Database: cfResourceID}}}
	cfg, _ := json.Marshal(job.Action.Resource)
	job.Binding = &planning.Binding{ExternalID: cfResourceID, PhysicalID: cfResourceID, Config: cfg}
	raw, err := catalogconnect.EncodeAPIAccess(catalogconnect.APIAccess{Provider: "cloudflare", Kind: "cloudflare-kv", AccountID: cfAccount, ResourceID: cfResourceID, Token: "application-private-token"})
	if err != nil {
		t.Fatal(err)
	}
	endpoints := &cfEndpointResolver{row: &catalogconnect.Endpoint{ID: "application", OrgID: "org", Revision: 7, Engine: "cloudflare-kv", Host: "api.cloudflare.com", Port: 443, Database: cfResourceID, TLS: catalogconnect.TLSVerifyFull}, dsn: raw}
	kept := map[string]string{}
	job.Keep = func(_ context.Context, key string, value []byte) error { kept[key] = string(value); return nil }
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"success":true,"result":{"id":%q,"title":"cache"}}`, cfResourceID)
	}))
	defer s.Close()
	out, err := (&Cloudflare{Client: cloudflare.New(s.URL, false), Authorizer: &cfEndpointAuthorizer{}, Endpoints: endpoints}).Apply(context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}
	if kept["output:CLOUDFLARE_API_TOKEN"] != "application-private-token" || kept["output:CLOUDFLARE_KV_NAMESPACE_ID"] != cfResourceID {
		t.Fatalf("application outputs unavailable: %#v", kept)
	}
	if out.URL != "" || kept["output:CLOUDFLARE_API_TOKEN"] == string(job.Credential) {
		t.Fatal("control-plane credential or private endpoint escaped")
	}
}
