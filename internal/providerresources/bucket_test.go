package providerresources

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/security"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

func bucketFixture(t *testing.T) (*Service, *testConnections, *testReader) {
	t.Helper()
	s, c, r, _ := fixture(t)
	c.rows["one"] = &connections.Connection{ID: "one", OrgID: "org", Cloud: connections.AWS, Account: "123456789012", Generation: 1, Status: connections.StatusReady, Details: map[string]string{"region": "us-east-1"}}
	r.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "account/123456789012/regions/us-east-1/managed-resources", Complete: true, Resources: []provider.ResourceObservation{{ID: "arn:aws:s3:::assets-bucket", Name: "assets-bucket", Kind: "object-storage", Region: "us-east-1"}}}, nil
	}
	return s, c, r
}

func TestS3LogicalBucketCanBeVerifiedWithoutInventedIncarnation(t *testing.T) {
	s, _, _ := bucketFixture(t)
	if _, err := s.Sync(t.Context(), principal, "one"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(t.Context(), principal)
	if err != nil || len(list.Resources) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	got, err := s.Verified(t.Context(), principal, list.Resources[0].ID)
	if err != nil {
		t.Fatalf("account/name bucket attachment blocked: %v", err)
	}
	if got.Incarnation != "" {
		t.Fatal("S3 acquired an invented physical incarnation")
	}
}

func bucketApplicationAccess() *catalogconnect.BucketAccess {
	return &catalogconnect.BucketAccess{Provider: "aws", Bucket: "assets-bucket", Region: "us-east-1", ExpectedOwner: "123456789012", AccessKeyID: "AKIAWORKLOADKEY123456", SecretAccessKey: "WORKLOAD-SECRET-123456"}
}
func TestBucketConnectPinsOnlyExplicitWorkloadCredentials(t *testing.T) {
	s, _, _ := bucketFixture(t)
	rows := &endpointRows{rows: map[string]*catalogconnect.Endpoint{}}
	secrets := endpointSecrets{}
	s.deps.Endpoints = catalogconnect.NewService(rows, secrets, &testAuth{})
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	id := list.Resources[0].ID
	got, err := s.Connect(t.Context(), principal, id, ConnectInput{BucketAccess: bucketApplicationAccess()})
	if err != nil {
		t.Fatal(err)
	}
	if got.IdentityScope != "account-name" || got.BucketOwner != "123456789012" || got.EndpointID == "" || got.Incarnation != "" {
		t.Fatalf("logical contract lost: %+v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "WORKLOAD-SECRET") || strings.Contains(string(raw), "TOPSECRET") {
		t.Fatal("secret returned in inventory")
	}
	record, e, access, err := s.Resolve(t.Context(), principal, id, got.Revision)
	if err != nil || record.ID != id || e.Engine != catalogconnect.EngineS3 || e.Database != "assets-bucket" || e.Host != "s3.us-east-1.amazonaws.com" || !strings.Contains(access, "WORKLOAD-SECRET") || strings.Contains(access, "TOPSECRET") {
		t.Fatalf("explicit access: %+v %+v %v", record, e, err)
	}
	oldEndpoint, oldRevision := got.EndpointID, got.Revision
	next := bucketApplicationAccess()
	next.SecretAccessKey = "ROTATED-SECRET-123456"
	got, err = s.Connect(t.Context(), principal, id, ConnectInput{BucketAccess: next})
	if err != nil || got.EndpointID == oldEndpoint || len(secrets) != 2 {
		t.Fatalf("rotation overwrote approved credential: %+v %v", got, err)
	}
	if _, _, _, err = s.Resolve(t.Context(), principal, id, oldRevision); !errors.Is(err, ErrChanged) {
		t.Fatalf("old review survived rotation: %v", err)
	}
}
func TestBucketCredentialsRejectSubstitutionBeforeVault(t *testing.T) {
	for _, kind := range []string{"provider", "bucket", "owner", "region", "both", "dsn"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _ := bucketFixture(t)
			secrets := endpointSecrets{}
			s.deps.Endpoints = catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, secrets, &testAuth{})
			_, _ = s.Sync(t.Context(), principal, "one")
			list, _ := s.List(t.Context(), principal)
			in := ConnectInput{BucketAccess: bucketApplicationAccess()}
			switch kind {
			case "provider":
				in.BucketAccess.Provider = "gcp"
			case "bucket":
				in.BucketAccess.Bucket = "another-bucket"
			case "owner":
				in.BucketAccess.ExpectedOwner = "999999999999"
			case "region":
				in.BucketAccess.Region = "us-west-2"
			case "both":
				in.DSN = "s3://assets-bucket"
			case "dsn":
				in.BucketAccess = nil
				in.DSN = "s3://assets-bucket"
			}
			if _, err := s.Connect(t.Context(), principal, list.Resources[0].ID, in); !errors.Is(err, ErrInvalid) || len(secrets) != 0 {
				t.Fatalf("substitution reached Vault: %v", err)
			}
		})
	}
}
func TestBucketScopedGrantAndPartialInventoryCannotBypassVerification(t *testing.T) {
	s, c, reader := bucketFixture(t)
	secrets := endpointSecrets{}
	s.deps.Endpoints = catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, secrets, &testAuth{})
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	got, err := s.Connect(t.Context(), principal, list.Resources[0].ID, ConnectInput{BucketAccess: bucketApplicationAccess()})
	if err != nil {
		t.Fatal(err)
	}
	bindings := []ResourceBinding{{ID: "attached-bucket", ProjectID: "project", EnvironmentID: "production", Provider: got.Provider, ConnectionID: got.ConnectionID, AccountIdentity: got.AccountIdentity, ExternalID: got.ExternalID, InventoryResourceID: got.ID, Revision: got.Revision, EndpointID: got.EndpointID, EndpointRevision: got.EndpointRevision}}
	s.deps.Usage = func(context.Context, string) ([]ResourceBinding, error) { return bindings, nil }
	s.deps.Authorizer = projectOnlyAuth{}
	developer := security.Principal{OrgID: "org", UserID: "developer"}
	if _, _, _, err = s.ResolveProject(t.Context(), developer, "project", "production", got.ID, got.Revision); err != nil {
		t.Fatal(err)
	}
	before := c.revealed
	if _, _, _, err = s.ResolveProject(t.Context(), developer, "project", "staging", got.ID, got.Revision); !errors.Is(err, security.ErrForbidden) || c.revealed != before {
		t.Fatalf("cross-env access: %v", err)
	}
	original := reader.fn
	reader.fn = func(conn *connections.Connection) (provider.ResourceInventory, error) {
		inv, e := original(conn)
		inv.Complete = false
		return inv, e
	}
	if _, _, _, err = s.ResolveProject(t.Context(), developer, "project", "production", got.ID, got.Revision); !errors.Is(err, ErrUnverified) {
		t.Fatalf("partial inventory authorized attachment: %v", err)
	}
}

func TestBucketLogicalIdentityIsRestrictedToObservedAccountAndName(t *testing.T) {
	s, _, _ := bucketFixture(t)
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	original := list.Resources[0]
	for _, broken := range []string{"account", "scope", "native-id", "kind", "provider", "region", "name"} {
		t.Run(broken, func(t *testing.T) {
			r := *original
			switch broken {
			case "account":
				r.AccountIdentity = "arbitrary-account"
			case "scope":
				r.Scope = "unverified"
			case "native-id":
				r.ExternalID = "arn:aws:s3:::other-bucket"
			case "kind":
				r.Kind = "postgres"
			case "provider":
				r.Provider = "azure"
			case "region":
				r.Region = "us-west-2"
			case "name":
				r.Name = "another-bucket"
			}
			if AttachableIdentity(&r) {
				t.Fatalf("logical identity bypassed %s", broken)
			}
		})
	}
}
func TestGoogleBucketIdentitySupportsProviderNamesAndRequiresPhysicalCreation(t *testing.T) {
	name := "assets_backup." + strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + ".example"
	r := &ProviderResource{Provider: "gcp", Kind: "object-storage", Name: name, ExternalID: "projects/bucket-project/buckets/" + name, Region: "us", Scope: "projects/bucket-project/all-regions/managed-resources", Incarnation: "2026-01-01T00:00:00Z", AccountIdentity: connections.AccountIdentity(connections.GCP, "", map[string]string{"project_id": "bucket-project"})}
	if !AttachableIdentity(r) {
		t.Fatal("valid Google bucket rejected by S3 naming rules")
	}
	r.Incarnation = ""
	if AttachableIdentity(r) {
		t.Fatal("GCS gained the S3 name-only exception")
	}
}

func TestGoogleBucketConnectUsesExplicitKeyAndBlocksPhysicalReplacement(t *testing.T) {
	s, c, reader, _ := fixture(t)
	c.rows["one"] = &connections.Connection{ID: "one", OrgID: "org", Cloud: connections.GCP, Generation: 1, Status: connections.StatusReady, Details: map[string]string{"project_id": "bucket-project"}}
	created := "2026-01-01T00:00:00Z"
	reader.fn = func(*connections.Connection) (provider.ResourceInventory, error) {
		return provider.ResourceInventory{Scope: "projects/bucket-project/all-regions/managed-resources", Complete: true, Resources: []provider.ResourceObservation{{ID: "projects/bucket-project/buckets/assets_backup.example", Name: "assets_backup.example", Kind: "object-storage", Region: "us", Incarnation: created}}}, nil
	}
	secrets := endpointSecrets{}
	s.deps.Endpoints = catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, secrets, &testAuth{})
	_, _ = s.Sync(t.Context(), principal, "one")
	list, _ := s.List(t.Context(), principal)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyJSON, err := json.Marshal(map[string]string{"type": "service_account", "project_id": "credentials-project", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})), "client_email": "workload@credentials-project.iam.gserviceaccount.com", "token_uri": "https://oauth2.googleapis.com/token"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Connect(t.Context(), principal, list.Resources[0].ID, ConnectInput{BucketAccess: &catalogconnect.BucketAccess{ServiceAccountJSON: string(keyJSON)}})
	if err != nil {
		t.Fatal(err)
	}
	if got.IdentityScope != "physical" || got.BucketOwner != "" || got.CredentialMethods[0] != "bucket_access" {
		t.Fatalf("incorrect GCS identity/access: %+v", got)
	}
	_, endpoint, raw, err := s.Resolve(t.Context(), principal, got.ID, got.Revision)
	if err != nil || endpoint.Engine != catalogconnect.EngineGCS || endpoint.Host != "storage.googleapis.com" || endpoint.Database != "assets_backup.example" {
		t.Fatalf("Google contract: %+v %v", endpoint, err)
	}
	access, err := catalogconnect.ParseBucketAccess(raw)
	if err != nil || access.Provider != "gcp" || access.Bucket != "assets_backup.example" || access.Region != "us" || access.ServiceAccountJSON != string(keyJSON) {
		t.Fatalf("verified selectors not normalized: %v", err)
	}
	created = "2026-02-01T00:00:00Z"
	if _, _, _, err = s.Resolve(t.Context(), principal, got.ID, got.Revision); err == nil {
		t.Fatal("recreated Google bucket retained old physical grant")
	}
}
