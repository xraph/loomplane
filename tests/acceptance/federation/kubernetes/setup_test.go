package kubernetes_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	agentv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/agent/v1"
	entryv1 "github.com/spiffe/spire-api-sdk/proto/spire/api/server/entry/v1"
	spiretypes "github.com/spiffe/spire-api-sdk/proto/spire/api/types"
	"github.com/xraph/grove/drivers/pgdriver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/xraph/loomplane/database/migrations"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	fed "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/ops"
	"github.com/xraph/loomplane/internal/runtimeapi"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

const adminAddress = "unix:///run/spire/admin/server.sock"
const workloadAddress = "unix:///run/spire/workload/agent.sock"

func openIdentity(ctx context.Context, t *testing.T, own *corev1.Pod, nodeUID string) *workloadidentity.Admin {
	t.Helper()
	var admin *workloadidentity.Admin
	wait(ctx, t, "SPIRE admin and PSAT node", func() error {
		opened, err := workloadidentity.OpenAdmin(ctx, adminAddress, "loomplane.test", "fixture")
		if err != nil {
			return err
		}
		admin = opened
		return nil
	})
	t.Cleanup(func() { must(t, admin.Close()) })
	parent := &spiretypes.SPIFFEID{TrustDomain: "loomplane.test", Path: "/spire/agent/k8s_psat/qualification/" + nodeUID}
	wait(ctx, t, "PSAT node attestation", func() error { _, err := admin.GetAgent(ctx, &agentv1.GetAgentRequest{Id: parent}); return err })
	// This fixture sidecar has local administrator access. Production agent
	// credentials never receive the admin socket or the control registration.
	conn, err := grpc.NewClient(adminAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	must(t, err)
	defer func() { must(t, conn.Close()) }()
	entry := &spiretypes.Entry{Id: uuid.NewString(), ParentId: parent, SpiffeId: &spiretypes.SPIFFEID{TrustDomain: "loomplane.test", Path: "/loomplane/control/org/fixture"}, X509SvidTtl: 60, Selectors: []*spiretypes.Selector{
		{Type: "k8s", Value: "ns:spire"}, {Type: "k8s", Value: "sa:spire-server"}, {Type: "k8s", Value: "pod-uid:" + string(own.UID)}, {Type: "k8s", Value: "container-name:fixture"},
	}}
	result, err := entryv1.NewEntryClient(conn).BatchCreateEntry(ctx, &entryv1.BatchCreateEntryRequest{Entries: []*spiretypes.Entry{entry}})
	must(t, err)
	if len(result.Results) != 1 || result.Results[0].Status.GetCode() != int32(codes.OK) {
		t.Fatal("control registration failed")
	}
	return admin
}

type fixtureCredentials struct {
	connection connections.Connection
	raw        []byte
}

func (c *fixtureCredentials) GetConnection(_ context.Context, org, id string) (*connections.Connection, error) {
	if org != c.connection.OrgID || id != c.connection.ID {
		return nil, connections.ErrNotFound
	}
	connection := c.connection
	return &connection, nil
}
func (c *fixtureCredentials) Credential(ctx context.Context, org, id string) (*connections.Connection, []byte, error) {
	connection, err := c.GetConnection(ctx, org, id)
	return connection, append([]byte(nil), c.raw...), err
}
func database(ctx context.Context, t *testing.T, client kubernetes.Interface) (*pgdriver.PgDB, *fed.PgStore, *fixtureCredentials, *domain.Destination) {
	t.Helper()
	password := os.Getenv("POSTGRES_PASSWORD")
	if password == "" {
		t.Fatal("fixture database password absent")
	}
	dsn := "postgres://fixture:" + password + "@127.0.0.1:5432/fixture?sslmode=disable"
	db := pgdriver.New()
	must(t, db.Open(ctx, dsn))
	t.Cleanup(func() { must(t, db.Close()) })
	wait(ctx, t, "PostgreSQL", func() error { var one int; return db.QueryRow(ctx, `SELECT 1`).Scan(&one) })
	must(t, ops.Migrate(ctx, dsn, migrations.All()))
	for _, query := range []string{
		`INSERT INTO loomplane_projects(id,org_id,name,slug,status,created_by,created_at,updated_at) VALUES('project','fixture','fixture','fixture','ready','operator',now(),now())`,
		`INSERT INTO loomplane_environments(id,org_id,project_id,name,kind,created_at,updated_at) VALUES('test','fixture','project','fixture','production',now(),now())`,
		`INSERT INTO loomplane_cloud_connections(id,org_id,cloud,name,account,status,created_by,created_at,updated_at) VALUES('connection','fixture','kubernetes','fixture','fixture','ready','operator',now(),now())`,
	} {
		_, err := db.Exec(ctx, query)
		must(t, err)
	}
	ca, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	must(t, err)
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	must(t, err)
	ns, err := client.CoreV1().Namespaces().Get(ctx, "apps", metav1.GetOptions{})
	must(t, err)
	hash := sha256.Sum256(ca)
	fingerprint := hex.EncodeToString(hash[:])
	raw, err := clientcmd.Write(clientapi.Config{CurrentContext: "fixture", Clusters: map[string]*clientapi.Cluster{"fixture": {Server: "https://kubernetes.default.svc", CertificateAuthorityData: ca}}, AuthInfos: map[string]*clientapi.AuthInfo{"fixture": {Token: string(token)}}, Contexts: map[string]*clientapi.Context{"fixture": {Cluster: "fixture", AuthInfo: "fixture", Namespace: "apps"}}})
	must(t, err)
	now := time.Now().UTC()
	credential := &fixtureCredentials{raw: raw, connection: connections.Connection{ID: "connection", OrgID: "fixture", Cloud: connections.Kubernetes, Status: connections.StatusReady, CheckedAt: &now, Details: map[string]string{"namespace": "apps", "namespace_uid": string(ns.UID), "cluster_ca": fingerprint}}}
	physical, err := fed.ConnectionIdentity(&credential.connection, now)
	must(t, err)
	destination := &domain.Destination{ID: "destination", Scope: domain.Scope{OrgID: "fixture", ProjectID: "project", EnvironmentID: "test"}, Name: "fixture", ConnectionID: "connection", Ownership: domain.Imported, Identity: physical, Generation: 1, CreatedAt: now, UpdatedAt: now,
		Capabilities: domain.CapabilitySet{Revision: "local-kind-fixture", IdentityRevision: physical.Revision(), CheckedAt: now, ExpiresAt: now.Add(5 * time.Minute), Results: map[domain.Capability]domain.CapabilityResult{domain.WorkloadAttestation: {State: domain.Supported}}}}
	// This capability applies only inside the disposable qualification database.
	store := fed.NewPgStore(db)
	must(t, store.Create(ctx, destination))
	return db, store, credential, destination
}
func startRuntime(ctx context.Context, t *testing.T, db *pgdriver.PgDB) *runtimeapi.Runtime {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	must(t, err)
	now := time.Now().UTC()
	body, err := json.Marshal(map[string]any{"version": 1, "keys": []any{map[string]any{"org_id": "fixture", "project_id": "project", "environment_id": "test", "private_key": base64.StdEncoding.EncodeToString(key), "not_before": now.Add(-time.Minute), "not_after": now.Add(10 * time.Minute)}}})
	must(t, err)
	path := filepath.Join(t.TempDir(), "keys.json")
	must(t, os.WriteFile(path, body, 0o600))
	runtime, err := runtimeapi.NewRuntime(config.FederationRuntimeConfig{Enabled: true, ListenAddress: "0.0.0.0:9443", OrganizationID: "fixture", TrustDomain: "loomplane.test", WorkloadAPI: workloadAddress, SigningKeysFile: path}, db, noWake{})
	must(t, err)
	must(t, runtime.Start(ctx))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		must(t, runtime.Stop(cleanup))
	})
	must(t, runtime.Health(ctx))
	fmt.Println("PASS control identity through PSAT and CSI")
	return runtime
}
