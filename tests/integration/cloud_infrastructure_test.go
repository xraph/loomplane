//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type infraSecrets struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *infraSecrets) Put(_ context.Context, org, id string, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[org+"/"+id] = append([]byte{}, raw...)
	return nil
}
func (s *infraSecrets) Reveal(_ context.Context, org, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[org+"/"+id]
	if !ok {
		return nil, connections.ErrNotFound
	}
	return append([]byte{}, v...), nil
}
func (s *infraSecrets) Delete(_ context.Context, org, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, org+"/"+id)
	return nil
}

type infraAuth struct{ denied bool }

func (a *infraAuth) Require(_ context.Context, p security.Principal, permission security.Permission, resource string) error {
	if a.denied || p.OrgID != "org" || p.UserID != "manager" || permission != security.OrganizationCloudManage || resource != "org" {
		return security.ErrForbidden
	}
	return nil
}

type infraWorkflow struct {
	calls int
	fail  bool
}

func (w *infraWorkflow) StartInfrastructure(context.Context, string, string, int) error {
	w.calls++
	if w.fail {
		return errors.New("queue unavailable")
	}
	return nil
}

type infraRuntime struct {
	reject bool
	calls  int
	vacant bool
}

func (r *infraRuntime) Inspect(_ context.Context, _ []byte, settings map[string]string, vacant bool) (cloudinfra.RuntimeHealth, error) {
	r.calls++
	r.vacant = vacant
	if r.reject || settings["host_key"] == "" {
		return cloudinfra.RuntimeHealth{}, errors.New("pinned runtime unavailable")
	}
	return cloudinfra.RuntimeHealth{DockerID: "docker-real", Architecture: "amd64", CPUs: 2, MemoryBytes: 2 << 30, CheckedAt: time.Now().UTC()}, nil
}

type infraProvider struct {
	remote                    provider.InfrastructureServer
	creates, resizes, deletes int
	lost                      bool
	pending                   bool
	seenReconcile             bool
	beforeCreate              func(provider.ServerSpec)
	actionKind                string
}

func (f *infraProvider) ListServers(context.Context, provider.InfrastructureCredential) ([]provider.InfrastructureServer, error) {
	return []provider.InfrastructureServer{}, nil
}
func (f *infraProvider) GetServer(_ context.Context, c provider.InfrastructureCredential, id string) (provider.InfrastructureServer, error) {
	if id != f.remote.ID || f.remote.ID == "" {
		return provider.InfrastructureServer{}, provider.ErrInfrastructureNotFound
	}
	if f.remote.AccountID != c.AccountID {
		return provider.InfrastructureServer{}, provider.ErrInfrastructureIdentity
	}
	return f.remote, nil
}
func (f *infraProvider) CreateServer(_ context.Context, c provider.InfrastructureCredential, s provider.ServerSpec) (provider.ServerResult, error) {
	if f.beforeCreate != nil {
		f.beforeCreate(s)
	}
	f.seenReconcile = s.ReconcileOnly
	if s.ReconcileOnly {
		if f.remote.ID == "" {
			return provider.ServerResult{}, provider.ErrInfrastructureAmbiguous
		}
		return provider.ServerResult{Server: f.remote, Reconciled: true, Actions: []provider.InfrastructureAction{{ID: "7", ResourceID: f.remote.ID, Kind: "create", Status: "completed"}}}, nil
	}
	f.creates++
	f.remote = provider.InfrastructureServer{ID: "42", AccountID: c.AccountID, Name: s.Name, Region: s.Region, Size: s.Size, PublicIPv4: []string{"127.0.0.1"}}
	f.actionKind = "create"
	if f.lost {
		return provider.ServerResult{}, provider.ErrInfrastructureAmbiguous
	}
	return provider.ServerResult{Server: f.remote, Actions: []provider.InfrastructureAction{{ID: "7", ResourceID: "42", Kind: "create", Status: "in-progress"}}}, nil
}
func (f *infraProvider) ResizeServer(_ context.Context, _ provider.InfrastructureCredential, id, size string) (provider.InfrastructureAction, error) {
	f.resizes++
	f.remote.Size = size
	f.actionKind = "resize"
	return provider.InfrastructureAction{ID: "8", ResourceID: id, Kind: "resize", Status: "in-progress"}, nil
}
func (f *infraProvider) DeleteServer(context.Context, provider.InfrastructureCredential, string) error {
	f.deletes++
	f.remote.ID = ""
	return nil
}
func (f *infraProvider) GetServerAction(_ context.Context, _ provider.InfrastructureCredential, id, action string) (provider.InfrastructureAction, error) {
	status := "completed"
	if f.pending {
		status = "in-progress"
	}
	return provider.InfrastructureAction{ID: action, ResourceID: id, Kind: f.actionKind, Status: status}, nil
}

type infraFixture struct {
	sql         *pgx.Conn
	svc         *cloudinfra.Service
	store       *cloudinfra.PgStore
	connections *connections.PgStore
	secrets     *infraSecrets
	auth        *infraAuth
	workflow    *infraWorkflow
	runtime     *infraRuntime
	provider    *infraProvider
	p           security.Principal
}

func newInfraFixture(t *testing.T) *infraFixture {
	t.Helper()
	clouds, sql, db := cloudSyncDatabase(t)
	store := cloudinfra.NewPgStore(db)
	cs := connections.NewPgStore(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := cs.CreateConnection(t.Context(), &connections.Connection{ID: "infra-account", OrgID: "org", Cloud: "digitalocean", Name: "Infrastructure account", Account: "Account", Details: map[string]string{"account_uuid": "account"}, Status: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	secrets := &infraSecrets{values: map[string][]byte{}}
	if err := secrets.Put(t.Context(), "org", "infra-account", []byte("dop_v1_fixture")); err != nil {
		t.Fatal(err)
	}
	auth := &infraAuth{}
	workflow := &infraWorkflow{}
	runtime := &infraRuntime{}
	adapter := &infraProvider{}
	adapters := cloudinfra.NewAdapters()
	if err := adapters.RegisterServer("digitalocean", adapter); err != nil {
		t.Fatal(err)
	}
	credentials := connections.NewService(cs, secrets, nil, auth, nil, nil)
	svc := cloudinfra.NewService(cloudinfra.Deps{Store: store, Clouds: clouds, Connections: credentials, RuntimeConnections: cs, Secrets: secrets, Authz: auth, Adapters: adapters, Workflow: workflow, Runtime: runtime, EnrollmentURL: "http://127.0.0.1", AllowLocalEnrollment: true})
	return &infraFixture{sql: sql, svc: svc, store: store, connections: cs, secrets: secrets, auth: auth, workflow: workflow, runtime: runtime, provider: adapter, p: security.Principal{OrgID: "org", UserID: "manager"}}
}
func (f *infraFixture) create(t *testing.T) *cloudinfra.Operation {
	t.Helper()
	o, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "host", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
func (f *infraFixture) reconcile(t *testing.T, o *cloudinfra.Operation) *cloudinfra.Operation {
	t.Helper()
	err := f.svc.ReconcileInfrastructure(t.Context(), "org", o.ID, nil)
	if err != nil && !errors.Is(err, cloudinfra.ErrPending) {
		t.Fatal(err)
	}
	got, err := f.store.OperationByID(t.Context(), "org", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCloudInfrastructurePersistsAttemptBeforeCreateAndRecoversLostResponse(t *testing.T) {
	f := newInfraFixture(t)
	f.workflow.fail = true
	o := f.create(t)
	if err := f.svc.RecoverInfrastructure(t.Context()); err == nil {
		t.Fatal("expected unavailable queue")
	}
	f.workflow.fail = false
	if err := f.svc.RecoverInfrastructure(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.workflow.calls < 3 {
		t.Fatal("accepted operation was not recovered")
	}
	f.provider.beforeCreate = func(spec provider.ServerSpec) {
		saved, err := f.store.OperationByID(t.Context(), "org", o.ID)
		if err != nil || !saved.Attempted {
			t.Fatal("provider call preceded durable attempted marker")
		}
		if !spec.ReconcileOnly && (spec.UserData == "" || strings.Contains(spec.UserData, "PRIVATE KEY")) {
			t.Fatal("bootstrap user data missing or private key leaked")
		}
	}
	f.provider.lost = true
	o = f.reconcile(t, o)
	if o.Status != "failed" || !o.Attempted || f.provider.creates != 1 {
		t.Fatalf("lost response: %+v", o)
	}
	// A new service instance resumes only the persisted intent.
	restarted := cloudinfra.NewService(f.svc.Deps)
	f.svc = restarted
	if _, err := f.svc.RetryOperation(t.Context(), f.p, "cloud", o.ID); err != nil {
		t.Fatal(err)
	}
	o = f.reconcile(t, o)
	if o.ResourceID != "42" || !f.provider.seenReconcile || f.provider.creates != 1 || o.Status != "waiting" {
		t.Fatalf("recovery duplicated or marked provider-ready: %+v", o)
	}
	raw, _ := json.Marshal(o)
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "dop_v1_fixture") {
		t.Fatal("secret in public operation")
	}
	if _, err := f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review}); err != nil {
		t.Fatal(err)
	}
	if f.provider.creates != 1 {
		t.Fatal("idempotent acceptance created again")
	}
}

func TestCloudInfrastructureReviewGuardsAndRevocation(t *testing.T) {
	f := newInfraFixture(t)
	denied := f.p
	denied.UserID = "project-manager"
	if _, err := f.svc.ListServers(t.Context(), denied, "cloud"); !errors.Is(err, security.ErrForbidden) {
		t.Fatal(err)
	}
	o := f.create(t)
	f.auth.denied = true
	o = f.reconcile(t, o)
	if o.Status != "failed" || f.provider.creates != 0 {
		t.Fatal("revoked permission permitted create")
	}
	f.auth.denied = false
	connection, err := f.connections.GetConnection(t.Context(), "org", "infra-account")
	if err != nil {
		t.Fatal(err)
	}
	connection.UpdatedAt = connection.UpdatedAt.Add(time.Second)
	connection.Details["account_uuid"] = "replacement"
	if err = f.connections.UpdateConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.RetryOperation(t.Context(), f.p, "cloud", o.ID); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("changed connection: %v", err)
	}
	if _, err = f.svc.GetOperation(t.Context(), security.Principal{OrgID: "other", UserID: "manager"}, "cloud", o.ID); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("cross organization operation exposed")
	}
}

func TestCloudInfrastructureDatabaseIntentAndCloudDeletionGuards(t *testing.T) {
	f := newInfraFixture(t)
	o := f.create(t)
	if err := f.connections.DeleteConnection(t.Context(), "org", "infra-account"); !errors.Is(err, connections.ErrInUse) {
		t.Fatalf("pending account deleted: %v", err)
	}
	o.Input.Size = "changed"
	if err := f.store.SaveOperation(t.Context(), o); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("intent mutable: %v", err)
	}
	o, err := f.store.OperationByID(t.Context(), "org", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	o.Attempted = true
	if err = f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	o.Attempted = false
	if err = f.store.SaveOperation(t.Context(), o); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("attempt reset: %v", err)
	}
	// The Cloud foreign key preserves operations even before a provider ID exists.
	clouds := f.svc.Clouds.(interface {
		DeleteProfile(context.Context, string, string, string) error
	})
	if err = clouds.DeleteProfile(t.Context(), "org", "", "cloud"); err == nil {
		t.Fatal("Cloud deletion orphaned pending infrastructure")
	}
}

func TestCloudInfrastructureResizeAndManagedDeletion(t *testing.T) {
	f := newInfraFixture(t)
	o := f.create(t)
	o = f.reconcile(t, o)
	// Enrollment's real transport is covered by the SSH/Docker test below. This
	// fixture isolates lifecycle checkpoint and exact-action behavior.
	o.HostKey = "fixture-host-key"
	o.Enrolled = true
	if err := f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	o = f.reconcile(t, o)
	if o.Status != "succeeded" {
		t.Fatalf("create: %+v", o)
	}
	server, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "resize", ServerID: server.ID, CloudRevision: 1, ServerRevision: server.Revision, Size: "large"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Review}); err != nil {
		t.Fatal(err)
	}
	f.provider.pending = true
	preview = f.reconcile(t, preview)
	if preview.Status != "waiting" || len(preview.Actions) != 1 || preview.Actions[0].ID != "8" {
		t.Fatalf("action checkpoint: %+v", preview)
	}
	f.provider.pending = false
	preview = f.reconcile(t, preview)
	if preview.Status != "succeeded" || f.provider.resizes != 1 {
		t.Fatalf("resize: %+v", preview)
	}
	server, err = f.store.GetServer(t.Context(), "org", "cloud", server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if server.Size != "large" || server.Health.MemoryBytes == 0 {
		t.Fatal("runtime capacity was not verified")
	}
	preview, err = f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "delete", ServerID: server.ID, CloudRevision: 1, ServerRevision: server.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Review}); err != nil {
		t.Fatal(err)
	}
	preview = f.reconcile(t, preview)
	if preview.Status != "waiting" {
		t.Fatal("provider accepted delete marked complete")
	}
	preview = f.reconcile(t, preview)
	if preview.Status != "succeeded" || f.provider.deletes != 1 {
		t.Fatalf("delete: %+v", preview)
	}
	if _, err = f.connections.GetConnection(t.Context(), "org", server.RuntimeConnectionID); !errors.Is(err, connections.ErrNotFound) {
		t.Fatal("managed runtime credential connection remained")
	}
	// Recover a process death after cleanup but before its terminal checkpoint.
	preview.Status = "waiting"
	if err = f.store.SaveOperation(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	preview = f.reconcile(t, preview)
	if preview.Status != "succeeded" || f.provider.deletes != 1 {
		t.Fatal("cleanup replay failed after runtime connection removal")
	}
}

func TestCloudInfrastructureAdoptionNeverDeletesProvider(t *testing.T) {
	f := newInfraFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	conn := &connections.Connection{ID: "adopted", OrgID: "org", Cloud: "server", Name: "Pinned host", Account: "docker-real", Status: "ready", Details: map[string]string{"host_key": "fixture-key"}, CreatedAt: now, UpdatedAt: now}
	if err := f.connections.CreateConnection(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if err := f.secrets.Put(t.Context(), "org", conn.ID, []byte("fixture-private-key")); err != nil {
		t.Fatal(err)
	}
	in := cloudinfra.ServerInput{Kind: "adopt", Name: "Existing", ConnectionID: conn.ID, CloudRevision: 1}
	if _, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", in); !errors.Is(err, cloudinfra.ErrInvalid) {
		t.Fatal("implicit adoption permitted")
	}
	in.ConfirmAdoption = true
	o, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review}); err != nil {
		t.Fatal(err)
	}
	o = f.reconcile(t, o)
	if o.Status != "succeeded" || !f.runtime.vacant {
		t.Fatal("adoption skipped vacancy verification")
	}
	server, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	in = cloudinfra.ServerInput{Kind: "delete", ServerID: server.ID, CloudRevision: 1, ServerRevision: server.Revision}
	if _, err = f.svc.PreviewServer(t.Context(), f.p, "cloud", in); !errors.Is(err, cloudinfra.ErrInvalid) {
		t.Fatal("adoption gained provider delete authority")
	}
	in.Kind = "disconnect"
	f.runtime.reject = true
	o, err = f.svc.PreviewServer(t.Context(), f.p, "cloud", in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", o.ID, cloudinfra.ApplyInput{Review: o.Review}); err != nil {
		t.Fatal(err)
	}
	o = f.reconcile(t, o)
	if o.Status != "succeeded" || f.provider.deletes != 0 {
		t.Fatal("disconnect deleted provider")
	}
	if _, err = f.connections.GetConnection(t.Context(), "org", conn.ID); err != nil {
		t.Fatal("adopted connection deleted")
	}
}

func Example_infrastructureReview() {
	fmt.Println("Preview the server change, then apply its saved review.") // Output: Preview the server change, then apply its saved review.
}

func TestCloudInfrastructureCloudDeletionAfterCleanedHistory(t *testing.T) {
	f := newInfraFixture(t)
	o, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "Unused preview", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	clouds := f.svc.Clouds.(interface {
		DeleteProfile(context.Context, string, string, string) error
	})
	if err = clouds.DeleteProfile(t.Context(), "org", "", "cloud"); err != nil {
		t.Fatalf("unused preview prevented Cloud deletion: %v", err)
	}
	if _, err = f.store.OperationByID(t.Context(), "org", o.ID); err != nil {
		t.Fatal("preview receipt was cascaded away")
	}
}

func TestCloudInfrastructureCancelledCreateCanBeExplicitlyDeleted(t *testing.T) {
	f := newInfraFixture(t)
	o := f.create(t)
	o = f.reconcile(t, o)
	if _, err := f.svc.CancelOperation(t.Context(), f.p, "cloud", o.ID); err != nil {
		t.Fatal(err)
	}
	server, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "delete", ServerID: server.ID, CloudRevision: 1, ServerRevision: server.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ApplyServerOperation(t.Context(), f.p, "cloud", preview.ID, cloudinfra.ApplyInput{Review: preview.Review}); err != nil {
		t.Fatal(err)
	}
	preview = f.reconcile(t, preview)
	preview = f.reconcile(t, preview)
	if preview.Status != "succeeded" {
		t.Fatalf("cleanup: %+v", preview)
	}
	clouds := f.svc.Clouds.(interface {
		DeleteProfile(context.Context, string, string, string) error
	})
	if err = clouds.DeleteProfile(t.Context(), "org", "", "cloud"); err != nil {
		t.Fatalf("cleaned history blocked deletion: %v", err)
	}
	if _, err = f.store.OperationByID(t.Context(), "org", o.ID); err != nil {
		t.Fatal("accepted receipt deleted")
	}
}

func TestCloudInfrastructureOrganizationAPI(t *testing.T) {
	_, running, client, _, _ := cloudAPISetup(t)
	defer running.stop()
	var cloud struct {
		ID string `json:"id"`
	}
	client.must(http.StatusCreated, http.MethodPost, "/api/v1/clouds", map[string]string{"name": "Capacity"}, &cloud)
	base := "/api/v1/clouds/" + cloud.ID
	var servers struct {
		Servers []cloudinfra.Server `json:"servers"`
	}
	client.must(http.StatusOK, http.MethodGet, base+"/servers", nil, &servers)
	if servers.Servers == nil || len(servers.Servers) != 0 {
		t.Fatal("empty inventory contract")
	}
	var operations struct {
		Operations []cloudinfra.Operation `json:"operations"`
	}
	client.must(http.StatusOK, http.MethodGet, base+"/infrastructure-operations", nil, &operations)
	client.must(http.StatusNotFound, http.MethodPost, base+"/servers/preview", cloudinfra.ServerInput{Kind: "create", Name: "host", ConnectionID: "missing", CloudRevision: 1}, nil)
	client.must(http.StatusNotFound, http.MethodGet, base+"/infrastructure-operations/missing", nil, nil)
	var document map[string]any
	client.must(http.StatusOK, http.MethodGet, "/openapi.json", nil, &document)
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/api/v1/clouds/{cloudId}/servers", "/api/v1/clouds/{cloudId}/server-options/{connectionId}", "/api/v1/clouds/{cloudId}/servers/preview", "/api/v1/clouds/{cloudId}/infrastructure-operations/{operationId}/apply"} {
		if paths[path] == nil {
			t.Errorf("missing Forge metadata: %s", path)
		}
	}
	token := client.token
	client.token = ""
	client.must(http.StatusUnauthorized, http.MethodGet, base+"/servers", nil, nil)
	client.token = token
}

func TestCloudInfrastructureCloudDeletionSerializesNewWork(t *testing.T) {
	for _, kind := range []string{"preview", "accept", "server"} {
		t.Run(kind, func(t *testing.T) {
			f := newInfraFixture(t)
			o, err := f.svc.PreviewServer(t.Context(), f.p, "cloud", cloudinfra.ServerInput{Kind: "create", Name: "Review", ConnectionID: "infra-account", CloudRevision: 1, Region: "region", Size: "small", Image: "ubuntu"})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.sql.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			if _, err = tx.Exec(t.Context(), `DELETE FROM loomplane_cloud_profiles WHERE org_id='org' AND id='cloud'`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			switch kind {
			case "preview":
				copyOp := *o
				copyOp.ID = "concurrent-preview"
				err = f.store.CreatePreview(ctx, &copyOp)
			case "accept":
				o.Status = "pending"
				o.Attempt = 1
				err = f.store.Accept(ctx, o)
			case "server":
				err = f.store.SaveServer(ctx, &cloudinfra.Server{ID: "concurrent-server", OrgID: "org", CloudID: "cloud", ConnectionID: "infra-account", Provider: "digitalocean", AccountID: "account", Ownership: "managed", Revision: 1, Status: "provisioning"})
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s did not serialize on deleting Cloud: %v", kind, err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.OperationByID(t.Context(), "other", o.ID); !errors.Is(err, cloudinfra.ErrNotFound) {
				t.Fatal("retained receipt crossed organization boundary")
			}
			if f.provider.creates != 0 {
				t.Fatal("Cloud deletion race started provider work")
			}
		})
	}
}

func TestCloudInfrastructureRetirementSerializesWorkloadReferences(t *testing.T) {
	f := newInfraFixture(t)
	o := f.create(t)
	o = f.reconcile(t, o)
	o.HostKey = "fixture-key"
	o.Enrolled = true
	if err := f.store.SaveOperation(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	o = f.reconcile(t, o)
	v, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.sql.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org',$1,'plan','concurrent')`, v.RuntimeConnectionID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err = f.store.RetireServer(ctx, v); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retirement ignored concurrent placement: %v", err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = f.store.RetireServer(t.Context(), v); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("retired used server: %v", err)
	}
	if _, err = f.sql.Exec(t.Context(), `DELETE FROM loomplane_connection_uses WHERE kind='plan' AND owner_id='concurrent'`); err != nil {
		t.Fatal(err)
	}
	if err = f.store.RetireServer(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(t.Context(), `INSERT INTO loomplane_connection_uses(org_id,connection_id,kind,owner_id) VALUES('org',$1,'plan','late')`, v.RuntimeConnectionID); err == nil {
		t.Fatal("late placement entered retiring server")
	}
}

func TestCloudInfrastructureStaleWorkersCannotResurrectRecords(t *testing.T) {
	f := newInfraFixture(t)
	o := f.create(t)
	stale := *o
	o = f.reconcile(t, o)
	stale.Status = "succeeded"
	if err := f.store.SaveOperation(t.Context(), &stale); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("stale operation checkpoint accepted: %v", err)
	}
	v, err := f.store.GetServer(t.Context(), "org", "cloud", o.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	staleServer := *v
	v.Status = "retiring"
	if err = f.store.SaveServer(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	staleServer.Status = "ready"
	if err = f.store.SaveServer(t.Context(), &staleServer); !errors.Is(err, cloudinfra.ErrConflict) {
		t.Fatalf("stale worker restored readiness: %v", err)
	}
}
