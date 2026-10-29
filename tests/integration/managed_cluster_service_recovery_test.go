//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/vault/crypto"
	"github.com/xraph/vault/secret"
	vaultpg "github.com/xraph/vault/store/postgres"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

var errEnrollmentProcessLost = errors.New("test process stopped")

type enrollmentServiceAuth struct{}

func (enrollmentServiceAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}
func (enrollmentServiceAuth) RequireCurrentMembership(context.Context, security.Principal) error {
	return nil
}

type enrollmentKindProvider struct {
	observation provider.ClusterObservation
	access      provider.BootstrapAccess
}

func (p *enrollmentKindProvider) GetCluster(context.Context, provider.AccountAccess, provider.ClusterRef) (provider.ClusterObservation, error) {
	return p.observation, nil
}
func (p *enrollmentKindProvider) ListClusters(context.Context, provider.AccountAccess, string) (provider.ClusterPage, error) {
	return provider.ClusterPage{Items: []provider.ClusterObservation{p.observation}}, nil
}
func (*enrollmentKindProvider) Options(context.Context, provider.AccountAccess, string) (provider.ClusterOptions, error) {
	return provider.ClusterOptions{}, nil
}
func (p *enrollmentKindProvider) BootstrapCluster(context.Context, provider.AccountAccess, provider.ClusterRef, time.Duration) (provider.BootstrapAccess, error) {
	return p.access, nil
}

type enrollmentCrashStore struct {
	*cloudinfra.PgStore
	stage string
	stale *cloudinfra.ClusterOperation
}

func (s *enrollmentCrashStore) SaveClusterCheckpoint(ctx context.Context, o *cloudinfra.ClusterOperation, expected int64) error {
	crash := o.Enrollment != nil && (s.stage == "namespace" && o.Enrollment.State.NamespaceUID != "" || s.stage == "rbac" && o.Enrollment.State.Objects["Role"] != "")
	if crash {
		copyOp := *o
		s.stale = &copyOp
		s.stage = ""
		panic(errEnrollmentProcessLost)
	}
	return s.PgStore.SaveClusterCheckpoint(ctx, o, expected)
}
func (s *enrollmentCrashStore) PublishClusterEnrollment(ctx context.Context, o *cloudinfra.ClusterOperation, a *cloudinfra.RuntimeAttachment, c *connections.Connection) error {
	if err := s.PgStore.PublishClusterEnrollment(ctx, o, a, c); err != nil {
		return err
	}
	if s.stage == "publication" {
		s.stage = ""
		panic(errEnrollmentProcessLost)
	}
	return nil
}

type enrollmentVaultFault struct {
	t *testing.T
	connections.Secrets
	db      *pgdriver.PgDB
	stage   string
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (v *enrollmentVaultFault) Put(ctx context.Context, org, key string, raw []byte) error {
	if v.stage == "delayed-write" {
		v.once.Do(func() { close(v.reached); <-v.release })
	}
	if err := v.Secrets.Put(ctx, org, key, raw); err != nil {
		return err
	}
	if v.stage == "vault" {
		v.stage = ""
		panic(errEnrollmentProcessLost)
	}
	return nil
}
func (v *enrollmentVaultFault) Delete(ctx context.Context, org, key string) error {
	var active bool
	err := v.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_runtime_attachments WHERE org_id=$1 AND body->>'vault_key'=$2) OR EXISTS(SELECT 1 FROM loomplane_cluster_credential_refresh WHERE org_id=$1 AND (vault_key=$2 OR recovery_key=$2))`, org, key).Scan(&active)
	if err != nil {
		return err
	}
	if active {
		v.t.Error("cleanup attempted an active or in-flight credential")
		return errors.New("cleanup attempted an active or in-flight credential")
	}
	return v.Secrets.Delete(ctx, org, key)
}

type enrollmentServiceFixture struct {
	t             *testing.T
	sql           *pgx.Conn
	pg            *pgdriver.PgDB
	store         *cloudinfra.PgStore
	clouds        *cloudprofiles.PgStore
	provider      *enrollmentKindProvider
	bootstrap     *kubernetes.Client
	principal     security.Principal
	op            *cloudinfra.ClusterOperation
	originalInput []byte
}

func newEnrollmentServiceFixture(t *testing.T) *enrollmentServiceFixture {
	t.Helper()
	path := os.Getenv("LOOMPLANE_TEST_MANAGED_KUBECONFIG")
	if path == "" {
		t.Skip("set disposable kind kubeconfig")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := kubernetes.ParseConfig(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := kubernetes.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := clientcmd.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	selected := embedded.Contexts[embedded.CurrentContext]
	cluster := embedded.Clusters[selected.Cluster]
	auth := embedded.AuthInfos[selected.AuthInfo]
	ca := sha256.Sum256(cluster.CertificateAuthorityData)
	observation := provider.ClusterObservation{Ref: provider.ClusterRef{ID: "kind", AccountID: "account", Region: "local", Incarnation: "kind-incarnation"}, Endpoint: cluster.Server, CAHash: hex.EncodeToString(ca[:]), State: "running", ObservedAt: time.Now().UTC()}
	db := managedClusterDB(t)
	store, clouds := managedClusterStore(t, db)
	pg := openPg(t, db.Config().ConnString())
	if _, err = db.Exec(t.Context(), `UPDATE loomplane_cloud_connections SET cloud='digitalocean',details='{"account_uuid":"account"}' WHERE id='conn'`); err != nil {
		t.Fatal(err)
	}
	f := &enrollmentServiceFixture{t: t, sql: db, pg: pg, store: store, clouds: clouds, bootstrap: bootstrap, principal: security.Principal{OrgID: "org", UserID: "manager"}, provider: &enrollmentKindProvider{observation: observation, access: provider.BootstrapAccess{Endpoint: cluster.Server, CAData: cluster.CertificateAuthorityData, Token: auth.Token, ClientCertificate: auth.ClientCertificateData, ClientKey: auth.ClientKeyData, ExpiresAt: time.Now().Add(time.Hour)}}}
	vault := f.vault()
	if err = vault.Put(t.Context(), "org", "conn", []byte("test-only-provider-account")); err != nil {
		t.Fatal(err)
	}
	resource := &cloudinfra.ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", ConnectionID: "conn", Kind: "cluster", Provider: "digitalocean", AccountID: "account", ExternalID: observation.Ref.ID, Incarnation: observation.Ref.Incarnation, Ownership: "external", Status: "running", Revision: 1, SchemaVersion: 1, Observation: &observation}
	if _, err = store.ClaimManagedResource(t.Context(), resource, 0); err != nil {
		t.Fatal(err)
	}
	namespace := "service-" + uuid.NewString()[:8]
	input := cloudinfra.ClusterInput{Kind: "adopt-external", Provider: "digitalocean", ConnectionID: "conn", AccountID: "account", ConnectionGeneration: 1, CloudRevision: 1, ResourceID: "cluster", ResourceRevision: 1, Ref: observation.Ref, Attachment: &cloudinfra.AttachmentInput{ProjectID: "proj", Namespace: namespace, ServiceAccountName: "runtime", TargetSlug: "worker", CloudRevision: 1, ResourceRevision: 1, GrantRevision: 1}}
	svc, _ := f.service(store, vault)
	op, err := svc.PreviewCluster(t.Context(), f.principal, "cloud", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ApplyCluster(t.Context(), f.principal, "cloud", op.ID, cloudinfra.ApplyInput{Review: op.Digest}); err != nil {
		t.Fatal(err)
	}
	f.op = op
	f.originalInput, err = json.Marshal(op.Input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		saved, getErr := store.GetClusterOperation(context.Background(), "org", "cloud", op.ID)
		if getErr == nil && saved.Enrollment != nil {
			_ = kubernetes.RetireRuntimeRBAC(context.Background(), bootstrap.Kube, kubernetes.EnrollmentRequest{AttachmentID: saved.Enrollment.AttachmentID, Namespace: namespace, ServiceAccountName: "runtime"}, saved.Enrollment.State)
		}
		_ = bootstrap.Kube.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
	})
	return f
}
func (f *enrollmentServiceFixture) vault() *enrollmentVaultFault {
	f.t.Helper()
	db, err := grove.Open(f.pg)
	if err != nil {
		f.t.Fatal(err)
	}
	storage := vaultpg.New(db)
	if err = storage.Migrate(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	encryptor, err := crypto.NewEncryptor(bytes.Repeat([]byte{37}, 32))
	if err != nil {
		f.t.Fatal(err)
	}
	return &enrollmentVaultFault{Secrets: connections.NewVaultSecrets(secret.NewService(storage, encryptor, secret.WithAppID(connections.VaultAppID))), db: f.pg, t: f.t}
}
func (f *enrollmentServiceFixture) service(store cloudinfra.ClusterStore, vault connections.Secrets) (*cloudinfra.Service, *connections.Service) {
	f.t.Helper()
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterCluster("digitalocean", f.provider, provider.ClusterDiscovery, provider.ClusterBootstrapAccess); err != nil {
		f.t.Fatal(err)
	}
	credentials := connections.NewService(connections.NewPgStore(f.pg), vault, nil, enrollmentServiceAuth{}, nil, nil)
	svc := cloudinfra.NewService(cloudinfra.Deps{Clusters: store, Clouds: f.clouds, Connections: credentials, RuntimeConnections: connections.NewPgStore(f.pg), Secrets: vault, Authz: enrollmentServiceAuth{}, Adapters: adapters, ManagedClusters: config.ManagedClustersConfig{Enabled: true}})
	credentials.ManagedCredentials = svc
	return svc, credentials
}
func enrollmentMustCrash(t *testing.T, run func()) {
	t.Helper()
	defer func() {
		recovered, ok := recover().(error)
		if !ok || !errors.Is(recovered, errEnrollmentProcessLost) {
			t.Fatalf("wanted injected process loss, got %v", recovered)
		}
	}()
	run()
	t.Fatal("process-loss hook was not reached")
}
func (f *enrollmentServiceFixture) expireOperation() {
	f.t.Helper()
	_, err := f.sql.Exec(f.t.Context(), `UPDATE loomplane_cluster_operations SET body=jsonb_set(body,'{lease_expires_at}',to_jsonb($2::text)) WHERE id=$1`, f.op.ID, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano))
	if err != nil {
		f.t.Fatal(err)
	}
}
func (f *enrollmentServiceFixture) attachment() *cloudinfra.RuntimeAttachment {
	f.t.Helper()
	op, err := f.store.GetClusterOperation(f.t.Context(), "org", "cloud", f.op.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if op.Status != "succeeded" || op.Enrollment == nil {
		f.t.Fatalf("operation did not publish: %s %s", op.Status, op.Error)
	}
	input, err := json.Marshal(op.Input)
	if err != nil || !bytes.Equal(input, f.originalInput) {
		f.t.Fatal("reviewed input changed", err)
	}
	a, err := f.store.GetRuntimeAttachment(f.t.Context(), "org", op.Enrollment.AttachmentID)
	if err != nil {
		f.t.Fatal(err)
	}
	var count int
	if err = f.sql.QueryRow(f.t.Context(), `SELECT count(*) FROM loomplane_cloud_targets WHERE id=$1 AND connection_id=$2 AND managed_resource_id='cluster'`, op.Enrollment.TargetID, a.RuntimeConnectionID).Scan(&count); err != nil || count != 1 {
		f.t.Fatal("target publication", count, err)
	}
	return a
}
func (f *enrollmentServiceFixture) handout(credentials *connections.Service, a *cloudinfra.RuntimeAttachment) []byte {
	f.t.Helper()
	c, raw, err := credentials.Credential(f.t.Context(), "org", a.RuntimeConnectionID)
	if err != nil {
		f.t.Fatal(err)
	}
	if c.Generation != a.TrustRevision {
		f.t.Fatal("renewal changed reviewed generation")
	}
	cfg, err := kubernetes.ParseConfig(raw, c.Details)
	if err != nil {
		f.t.Fatal(err)
	}
	runtimeClient, err := kubernetes.New(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	if err = kubernetes.VerifyRuntimeIdentity(f.t.Context(), runtimeClient.Kube, a.Namespace, a.NamespaceUID, a.ServiceAccountName, a.ServiceAccountUID); err != nil {
		f.t.Fatal(err)
	}
	if err = runtimeClient.CheckRuntimeBoundaries(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	return raw
}

func TestManagedClusterEnrollmentServiceRestart(t *testing.T) {
	for _, stage := range []string{"namespace", "rbac", "vault", "publication"} {
		t.Run(stage, func(t *testing.T) {
			f := newEnrollmentServiceFixture(t)
			crashStore := &enrollmentCrashStore{PgStore: f.store, stage: stage}
			vault := f.vault()
			if stage == "vault" {
				vault.stage = stage
			}
			svc, _ := f.service(crashStore, vault)
			enrollmentMustCrash(t, func() {
				if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
					t.Fatal(err)
				}
			})
			ns, err := f.bootstrap.Kube.CoreV1().Namespaces().Get(t.Context(), f.op.Input.Attachment.Namespace, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			before, readErr := f.store.GetClusterOperation(t.Context(), "org", "cloud", f.op.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			previousUIDs := map[string]string{}
			if objects, listErr := f.bootstrap.Kube.CoreV1().ServiceAccounts(ns.Name).List(t.Context(), metav1.ListOptions{LabelSelector: "loomplane.io/attachment=" + before.Enrollment.AttachmentID}); listErr == nil {
				for _, object := range objects.Items {
					previousUIDs["ServiceAccount"] = string(object.UID)
				}
			} else {
				t.Fatal(listErr)
			}
			if objects, listErr := f.bootstrap.Kube.RbacV1().RoleBindings(ns.Name).List(t.Context(), metav1.ListOptions{LabelSelector: "loomplane.io/attachment=" + before.Enrollment.AttachmentID}); listErr == nil {
				for _, object := range objects.Items {
					previousUIDs["RoleBinding"] = string(object.UID)
				}
			} else {
				t.Fatal(listErr)
			}
			if objects, listErr := f.bootstrap.Kube.RbacV1().ClusterRoles().List(t.Context(), metav1.ListOptions{LabelSelector: "loomplane.io/attachment=" + before.Enrollment.AttachmentID}); listErr == nil {
				for _, object := range objects.Items {
					previousUIDs["ClusterRole"] = string(object.UID)
				}
			} else {
				t.Fatal(listErr)
			}
			if objects, listErr := f.bootstrap.Kube.RbacV1().ClusterRoleBindings().List(t.Context(), metav1.ListOptions{LabelSelector: "loomplane.io/attachment=" + before.Enrollment.AttachmentID}); listErr == nil {
				for _, object := range objects.Items {
					previousUIDs["ClusterRoleBinding"] = string(object.UID)
				}
			} else {
				t.Fatal(listErr)
			}
			existingRole, roleErr := f.bootstrap.Kube.RbacV1().Roles(ns.Name).Get(t.Context(), "runtime", metav1.GetOptions{})
			f.expireOperation()
			reopened, credentials := f.service(cloudinfra.NewPgStore(f.pg), f.vault())
			if _, err = reopened.EnrollClusterTarget(t.Context(), f.principal, "cloud", "cluster", *f.op.Input.Attachment); err != nil {
				t.Fatal(err)
			}
			a := f.attachment()
			if before.Enrollment.VaultKey != "" && a.VaultKey != before.Enrollment.VaultKey {
				t.Fatal("restart replaced the persisted runtime credential")
			}
			for _, receipt := range a.RBACObjects {
				if old := previousUIDs[receipt.Kind]; old != "" && old != receipt.UID {
					t.Fatal("restart changed runtime UID", receipt.Kind)
				}
			}
			if a.NamespaceUID != string(ns.UID) {
				t.Fatal("restart replaced namespace")
			}
			if roleErr == nil {
				current, getErr := f.bootstrap.Kube.RbacV1().Roles(ns.Name).Get(t.Context(), "runtime", metav1.GetOptions{})
				if getErr != nil || current.UID != existingRole.UID {
					t.Fatal("restart replaced role", getErr)
				}
			}
			if crashStore.stale != nil {
				if err = f.store.SaveClusterCheckpoint(t.Context(), crashStore.stale, crashStore.stale.Revision-1); !errors.Is(err, cloudinfra.ErrConflict) {
					t.Fatal("former worker checkpoint survived restart", err)
				}
			}
			f.handout(credentials, a)
			roles, err := f.bootstrap.Kube.RbacV1().ClusterRoles().List(t.Context(), metav1.ListOptions{LabelSelector: "loomplane.io/attachment=" + a.ID})
			if err != nil || len(roles.Items) != 1 {
				t.Fatal("duplicate runtime cluster roles", err)
			}
		})
	}
}

func (f *enrollmentServiceFixture) forceRenewal(a *cloudinfra.RuntimeAttachment) {
	f.t.Helper()
	vault := f.vault()
	payload, err := vault.Reveal(f.t.Context(), "org", a.VaultKey)
	if err != nil {
		f.t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(payload, &envelope); err != nil {
		f.t.Fatal(err)
	}
	// Advance persisted expiry into the renewal window without waiting fifty minutes.
	expiry := time.Now().UTC().Add(5 * time.Minute)
	envelope["expires_at"], err = json.Marshal(expiry)
	if err != nil {
		f.t.Fatal(err)
	}
	payload, err = json.Marshal(envelope)
	if err != nil {
		f.t.Fatal(err)
	}
	if err = vault.Put(f.t.Context(), "org", a.VaultKey, payload); err != nil {
		f.t.Fatal(err)
	}
	expected := a.Revision
	a.Revision++
	a.CredentialExpiry = expiry
	if err = f.store.SaveRuntimeAttachment(f.t.Context(), a, expected); err != nil {
		f.t.Fatal(err)
	}
}
func (f *enrollmentServiceFixture) expireRefresh() {
	f.t.Helper()
	if _, err := f.sql.Exec(f.t.Context(), `UPDATE loomplane_cluster_credential_refresh SET lease_until=clock_timestamp()-interval '1 second'; UPDATE loomplane_cluster_credential_attempts SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
		f.t.Fatal(err)
	}
}
func TestManagedClusterEnrollmentServiceRenewalRecovery(t *testing.T) {
	for _, stage := range []string{"orphan-write", "delayed-write"} {
		t.Run(stage, func(t *testing.T) {
			f := newEnrollmentServiceFixture(t)
			svc, _ := f.service(f.store, f.vault())
			if err := svc.ReconcileClusterOperation(t.Context(), "org", f.op.ID, nil); err != nil {
				t.Fatal(err)
			}
			a := f.attachment()
			f.forceRenewal(a)
			faultVault := f.vault()
			faultVault.stage = "vault"
			_, oldCredentials := f.service(&enrollmentCrashStore{PgStore: f.store}, faultVault)
			var oldResult chan error
			if stage == "orphan-write" {
				enrollmentMustCrash(t, func() { _, _, _ = oldCredentials.Credential(t.Context(), "org", a.RuntimeConnectionID) })
			} else {
				faultVault.stage = "delayed-write"
				faultVault.reached = make(chan struct{})
				faultVault.release = make(chan struct{})
				oldResult = make(chan error, 1)
				go func() {
					_, raw, err := oldCredentials.Credential(t.Context(), "org", a.RuntimeConnectionID)
					if len(raw) != 0 {
						oldResult <- errors.New("stale worker returned a credential")
						return
					}
					oldResult <- err
				}()
				select {
				case <-faultVault.reached:
				case <-time.After(15 * time.Second):
					t.Fatal("refresh did not reach delayed write")
				}
			}
			var abandonedKey string
			if err := f.sql.QueryRow(t.Context(), `SELECT vault_key FROM loomplane_cluster_credential_refresh WHERE attachment_id=$1`, a.ID).Scan(&abandonedKey); err != nil {
				t.Fatal(err)
			}
			var orphanPayload []byte
			if stage == "orphan-write" {
				var err error
				orphanPayload, err = f.vault().Reveal(t.Context(), "org", abandonedKey)
				if err != nil {
					t.Fatal(err)
				}
			}
			f.expireRefresh()
			_, freshCredentials := f.service(cloudinfra.NewPgStore(f.pg), f.vault())
			activeRaw := f.handout(freshCredentials, a)
			current := f.attachment()
			if current.VaultKey == abandonedKey || current.CredentialVersion != a.CredentialVersion+1 || current.TrustRevision != a.TrustRevision {
				t.Fatal("takeover lost credential identity")
			}
			if stage == "orphan-write" {
				var recovered struct {
					Kubeconfig []byte `json:"kubeconfig"`
				}
				if err := json.Unmarshal(orphanPayload, &recovered); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(recovered.Kubeconfig, activeRaw) {
					t.Fatal("takeover did not recover the written runtime token")
				}
			} else {
				close(faultVault.release)
				select {
				case err := <-oldResult:
					if !errors.Is(err, cloudinfra.ErrConflict) {
						t.Fatal("late worker did not fail its lease fence", err)
					}
				case <-time.After(15 * time.Second):
					t.Fatal("late worker did not finish")
				}
				if _, err := f.vault().Reveal(t.Context(), "org", abandonedKey); err != nil {
					t.Fatal("late write was not exercised", err)
				}
			}
			f.handout(freshCredentials, current)
			if _, err := f.vault().Reveal(t.Context(), "org", abandonedKey); !errors.Is(err, connections.ErrNotFound) {
				t.Fatal("orphan material survived service cleanup", err)
			}
			activePayload, err := f.vault().Reveal(t.Context(), "org", current.VaultKey)
			if err != nil {
				t.Fatal("active material was deleted", err)
			}
			if len(activePayload) == 0 {
				t.Fatal("active Vault payload missing")
			}
			if !reflect.DeepEqual(f.attachment().RBACObjects, current.RBACObjects) {
				t.Fatal("renewal changed runtime ownership")
			}
		})
	}
}
