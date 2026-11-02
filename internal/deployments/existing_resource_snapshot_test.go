package deployments

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/providerresources"
	"github.com/xraph/loomplane/internal/security"
)

func TestExistingResourceSnapshotRequiresExactApprovedAccess(t *testing.T) {
	for _, engine := range []string{catalogconnect.EnginePostgres, catalogconnect.EngineS3} {
		for _, scenario := range []string{"unchanged", "stale", "missing", "manual-override", "revoked", "metadata-changed", "endpoint-changed", "malformed-snapshot", "empty-optional-value"} {
			t.Run(engine+"/"+scenario, func(t *testing.T) {
				account, providerName, providerCloud, kind, externalID, incarnation, region, scope, name := "account", "digitalocean", "digitalocean", "postgres", "native", "physical", "nyc3", "account", "db"
				raw := "postgres://app:APPROVED-SECRET@db.example/app?sslmode=verify-full"
				key, field, want := "DATABASE_URL", "", raw
				if engine == catalogconnect.EngineS3 {
					account = connections.AccountIdentity(connections.AWS, "123456789012", map[string]string{"region": "account-inventory"})
					providerName, providerCloud, kind, externalID, incarnation, region, scope, name = "s3", "aws", "object-storage", "arn:aws:s3:::assets-bucket", "", "us-east-1", "account/123456789012/regions/us-east-1/managed-resources", "assets-bucket"
					var err error
					raw, err = catalogconnect.EncodeBucketAccess(catalogconnect.BucketAccess{Provider: "aws", Bucket: name, Region: region, ExpectedOwner: "123456789012", AccessKeyID: "AKIAWORKLOADKEY123456", SecretAccessKey: "APPROVED-WORKLOAD-SECRET"})
					if err != nil {
						t.Fatal(err)
					}
					key, field, want = "AWS_SECRET_ACCESS_KEY", "output:AWS_SECRET_ACCESS_KEY", "APPROVED-WORKLOAD-SECRET"
					if scenario == "empty-optional-value" {
						key, field, want = "AWS_SESSION_TOKEN", "output:AWS_SESSION_TOKEN", ""
					}
				}
				pin := &planning.ExistingResourceConfig{ResourceID: "inventory", Revision: 1, EndpointID: "endpoint", EndpointRevision: 1, AccountIdentity: account, ExternalID: externalID, Incarnation: incarnation}
				cfg := &planning.ResourceConfig{Provider: providerName, Kind: kind, AccountIdentity: account, ExistingResource: pin}
				config, err := json.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				record := &providerresources.ProviderResource{ID: "inventory", OrgID: "org", Provider: providerCloud, Kind: kind, Name: name, Region: region, Scope: scope, AccountIdentity: account, ExternalID: externalID, Incarnation: incarnation, Revision: 1, EndpointID: "endpoint", EndpointRevision: 1}
				endpoint := &catalogconnect.Endpoint{ID: "endpoint", OrgID: "org", Revision: 1, Engine: engine}
				resolver := &retryResourceResolver{existingAccess: existingAccess{record: record, endpoint: endpoint}, raw: raw}
				snapshot := appliedVariables{Values: map[string]string{}, ResourceValues: map[string]string{key: want}}
				switch scenario {
				case "stale":
					snapshot.ResourceValues[key] = "OLD-SECRET"
				case "missing":
					delete(snapshot.ResourceValues, key)
				case "manual-override":
					snapshot.Values[key] = "manual-project-value"
					delete(snapshot.ResourceValues, key)
				case "revoked":
					resolver.err = security.ErrForbidden
				case "metadata-changed":
					record.Revision++
				case "endpoint-changed":
					endpoint.ID = "different-endpoint"
				}
				captured, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "malformed-snapshot" {
					captured = []byte("{invalid")
				}
				secrets := snapshotSecrets{"snapshot": captured}
				resource := planning.Action{Name: "db", Subject: planning.ResourceSubject, Provider: providerName, ConnectionID: "resource-connection", Resource: cfg, Op: planning.Keep}
				job := &Job{Deployment: &Deployment{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}, Plan: &planning.Plan{CreatedBy: "user", Actions: []planning.Action{resource}}, Action: planning.Action{Name: "api", Service: &planning.ServiceConfig{AppliedInputs: &planning.AppliedInputs{Variables: "snapshot"}, Env: []planning.EnvLink{{Key: key, From: "db", Subject: planning.ResourceSubject, Field: field}}}}}
				bindings := map[string]*planning.Binding{"db": {Name: "db", Subject: planning.ResourceSubject, Provider: providerName, ConnectionID: "resource-connection", Config: config}}
				runner := NewRunner(RunnerDeps{Resources: secrets, ExistingResources: resolver})
				err = runner.verifyExistingSnapshotAccess(t.Context(), job, bindings)
				good := scenario == "unchanged" || scenario == "manual-override" || scenario == "empty-optional-value"
				if good && err != nil {
					t.Fatalf("exact/explicit access blocked: %v", err)
				}
				if !good && err == nil {
					t.Fatal("stale or unverified generated snapshot reached delivery")
				}
				if err != nil && (strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), raw)) {
					t.Fatal("snapshot refusal exposed credential")
				}
				if string(secrets["snapshot"]) != string(captured) {
					t.Fatal("immutable snapshot silently refreshed")
				}
			})
		}
	}
}
