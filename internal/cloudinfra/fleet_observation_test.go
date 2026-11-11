package cloudinfra

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/fleets"
	"github.com/xraph/loomplane/pkg/fleet"
	"github.com/xraph/loomplane/pkg/provider"
)

type fleetObservationRecords struct {
	instance fleet.Instance
	fleet    *fleet.Fleet
}

func (r *fleetObservationRecords) GetInstance(_ context.Context, org, id string) (*fleet.Instance, error) {
	if org != "org" || id != r.instance.ID {
		return nil, ErrNotFound
	}
	cp := r.instance
	return &cp, nil
}
func (r *fleetObservationRecords) GetFleet(_ context.Context, org, id string) (*fleet.Fleet, error) {
	if r.fleet != nil && org == r.fleet.OrgID && id == r.fleet.ID {
		cp := *r.fleet
		return &cp, nil
	}
	if org != "org" || id != "fleet" {
		return nil, ErrNotFound
	}
	return &fleet.Fleet{ID: id, OrgID: org, ProjectID: "project"}, nil
}

type fleetObservationSecrets struct {
	connections.Secrets
	reads, writes int
}

func (s *fleetObservationSecrets) Reveal(context.Context, string, string) ([]byte, error) {
	s.reads++
	return nil, errors.New("unexpected secret read")
}
func (s *fleetObservationSecrets) Put(context.Context, string, string, []byte) error {
	s.writes++
	return errors.New("unexpected secret write")
}
func (s *fleetObservationSecrets) Delete(context.Context, string, string) error {
	s.writes++
	return errors.New("unexpected secret deletion")
}
func fleetObservationTargetFixture(t *testing.T) (*FleetObservationReader, *namespaceStatusStore, *fleetObservationRecords, *fleetObservationSecrets) {
	t.Helper()
	svc, current, store, _, _ := namespaceStatusFixture(t)
	op, namespace := instanceNamespaceIdentity("org", current.instance.ID, current.instance.OwnershipEpoch)
	store.record.OperationID = op
	store.record.Namespace = namespace
	store.operation.ID = op
	store.operation.Input.Attachment.Namespace = namespace
	store.attachment.OperationID = op
	store.attachment.Namespace = namespace
	store.attachment.ServiceAccountName = "runtime"
	store.operation.Input.Attachment.ServiceAccountName = "runtime"
	store.attachment.EndpointFingerprint = strings.Repeat("a", 64)
	store.attachment.TrustRevision = 1
	store.attachment.CredentialVersion = 1
	store.attachment.CredentialExpiry = time.Now().Add(time.Hour)
	store.attachment.VaultKey = "synthetic-vault-reference"
	records := &fleetObservationRecords{instance: current.instance}
	secrets := &fleetObservationSecrets{}
	svc.Secrets = secrets
	reader := NewFleetObservationReader(svc, records)
	reader.namespaces = &fleetObservationNamespaceFixture{record: &store.record}
	return reader, store, records, secrets
}
func TestFleetObservationTargetUsesPersistedScope(t *testing.T) {
	reader, store, _, secrets := fleetObservationTargetFixture(t)
	target, err := reader.Target(t.Context(), "org", "fleet", "instance")
	if err != nil {
		t.Fatal(err)
	}
	if target.Generation != 2 || target.OwnershipEpoch != 1 || target.AttachmentRevision != 7 || target.ConnectionID != store.attachment.RuntimeConnectionID || target.NamespaceUID != store.attachment.NamespaceUID {
		t.Fatalf("wrong pins: %+v", target)
	}
	raw, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic-vault-reference") || secrets.reads != 0 || secrets.writes != 0 {
		t.Fatal("target exposed or opened credentials")
	}
}
func TestFleetObservationTargetRejectsDrift(t *testing.T) {
	for _, which := range []string{"epoch", "generation", "environment", "namespace-uid", "service-account", "empty-service-account", "attachment-owner", "connection", "failed", "retired"} {
		t.Run(which, func(t *testing.T) {
			reader, store, records, secrets := fleetObservationTargetFixture(t)
			switch which {
			case "epoch":
				records.instance.OwnershipEpoch++
			case "generation":
				records.instance.DesiredGeneration++
			case "environment":
				records.instance.EnvironmentID = "other"
			case "namespace-uid":
				store.attachment.NamespaceUID = "other"
			case "service-account":
				store.attachment.ServiceAccountUID = "other"
			case "empty-service-account":
				store.attachment.ServiceAccountUID = ""
				store.operation.Enrollment.State.ServiceAccountUID = ""
			case "attachment-owner":
				store.attachment.ProjectID = "other"
			case "connection":
				store.attachment.RuntimeConnectionID = "other"
			case "failed":
				store.operation.Status = "failed"
			case "retired":
				now := time.Now()
				store.attachment.DeletedAt = &now
			}
			if _, err := reader.Target(t.Context(), "org", "fleet", "instance"); err == nil {
				t.Fatal("stale observation target accepted")
			}
			if secrets.reads != 0 || secrets.writes != 0 {
				t.Fatal("invalid target accessed vault")
			}
		})
	}
}
func TestFleetObservationOpenRejectsForgedTargetBeforeSecret(t *testing.T) {
	reader, _, _, secrets := fleetObservationTargetFixture(t)
	target, err := reader.Target(t.Context(), "org", "fleet", "instance")
	if err != nil {
		t.Fatal(err)
	}
	target.NamespaceUID = "replacement"
	if _, err = reader.Open(t.Context(), target); err == nil {
		t.Fatal("forged observation target opened")
	}
	if secrets.reads != 0 || secrets.writes != 0 {
		t.Fatal("forged target reached vault")
	}
}

type fleetObservationOpenStore struct {
	*namespaceStatusStore
	resource ManagedResource
	checks   int
}

func (s *fleetObservationOpenStore) CheckInstanceNamespaceRecord(_ context.Context, r InstanceNamespaceRecord) error {
	s.checks++
	if !reflect.DeepEqual(r, s.record) {
		return ErrConflict
	}
	return nil
}
func (s *fleetObservationOpenStore) GetManagedResource(_ context.Context, org, id string) (*ManagedResource, error) {
	if org != s.resource.OrgID || id != s.resource.ID {
		return nil, ErrNotFound
	}
	v := s.resource
	return &v, nil
}

type fleetObservationAuthority struct {
	InstanceNamespaceAuthority
	calls int
	err   error
}

func (a *fleetObservationAuthority) RevalidateAuthority(context.Context, fleets.OperationAuthority) error {
	a.calls++
	return a.err
}

type fleetObservationQualification struct {
	InstanceNamespaceQualifier
	calls int
	err   error
}

func (q *fleetObservationQualification) RevalidateInstanceNamespaceQualification(context.Context, InstanceNamespaceRecord, *ClusterOperation) error {
	q.calls++
	return q.err
}

type fleetObservationConnections struct {
	connections.Store
	items map[string]connections.Connection
}

func (c *fleetObservationConnections) GetConnection(_ context.Context, org, id string) (*connections.Connection, error) {
	v, ok := c.items[id]
	if !ok || v.OrgID != org {
		return nil, connections.ErrNotFound
	}
	return &v, nil
}
func TestFleetObservationOpenUsesExistingCredentialWithoutWrites(t *testing.T) {
	for _, scenario := range []string{"success", "authority-denied", "qualification-denied", "generation-changes-during-read", "namespace-replaced", "platform-owned", "resource-revision", "missing-ca", "post-read-disabled", "post-read-details", "post-read-deleted"} {
		t.Run(scenario, func(t *testing.T) {
			reader, base, records, _ := fleetObservationTargetFixture(t)
			requests, writes := 0, 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				if r.Method != http.MethodGet {
					writes++
					w.WriteHeader(403)
					return
				}
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("missing runtime credential")
					w.WriteHeader(401)
					return
				}
				if scenario == "generation-changes-during-read" {
					records.instance.DesiredGeneration++
				}
				uid := base.attachment.NamespaceUID
				if scenario == "namespace-replaced" {
					uid = "replacement"
				}
				var value any
				switch r.URL.Path {
				case "/api/v1/namespaces/" + base.attachment.Namespace:
					value = map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]string{"name": base.attachment.Namespace, "uid": uid}}
				case "/api/v1/namespaces/" + base.attachment.Namespace + "/serviceaccounts/runtime":
					value = map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]string{"name": "runtime", "namespace": base.attachment.Namespace, "uid": base.attachment.ServiceAccountUID}}
				default:
					t.Errorf("unexpected read %s", r.URL.Path)
					w.WriteHeader(403)
					return
				}
				if err := json.NewEncoder(w).Encode(value); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			digest := sha256.Sum256(cert)
			ca := hex.EncodeToString(digest[:])
			a := &base.attachment
			a.TrustFingerprint = ca
			a.EndpointFingerprint = endpointFingerprint(server.URL)
			op := &base.operation
			op.Input.Kind = InstanceNamespaceKind
			op.Input.CloudRevision = 1
			op.Input.ResourceRevision = 1
			op.Input.ConnectionID = "infra"
			op.Input.ConnectionGeneration = 1
			op.Input.Provider = "digitalocean"
			op.Input.AccountID = "account"
			op.Input.Attachment.GrantRevision = 1
			op.Input.Attachment.TrustFingerprint = ca
			ref := provider.ClusterRef{ID: "cluster-external", Incarnation: "incarnation"}
			op.Input.Ref = ref
			store := &fleetObservationOpenStore{namespaceStatusStore: base, resource: ManagedResource{ID: "cluster", OrgID: "org", CloudID: "cloud", ConnectionID: "infra", Provider: "digitalocean", AccountID: "account", ExternalID: ref.ID, Incarnation: ref.Incarnation, Status: "running", Revision: 1, Observation: &provider.ClusterObservation{Ref: ref, Endpoint: server.URL, CAHash: ca, State: "running"}}}
			authority := &fleetObservationAuthority{}
			qualification := &fleetObservationQualification{}
			if scenario == "authority-denied" {
				authority.err = ErrConflict
			}
			if scenario == "qualification-denied" {
				qualification.err = ErrConflict
			}
			svc := reader.service
			svc.Clusters = store
			svc.ManagedClusters.Enabled = true
			svc.Clouds = fleetObservationClouds{platformOwned: scenario == "platform-owned"}
			if scenario == "resource-revision" {
				store.resource.Revision++
			}
			if scenario == "missing-ca" {
				store.resource.Observation.CAHash = ""
			}
			svc.InstanceAuthority = authority
			svc.InstanceIsolation = qualification
			svc.RuntimeConnections = &fleetObservationConnections{items: map[string]connections.Connection{
				"infra":               {ID: "infra", OrgID: "org", Cloud: connections.DigitalOcean, Status: connections.StatusReady, Generation: 1, Details: map[string]string{"account_uuid": "account"}},
				a.RuntimeConnectionID: {ID: a.RuntimeConnectionID, OrgID: "org", Cloud: connections.Kubernetes, Method: connections.MethodManagedKubernetes, Status: connections.StatusReady, Generation: a.TrustRevision, Details: map[string]string{"managed_attachment_id": a.ID, "managed_project_id": a.ProjectID, "managed_resource_id": a.ClusterID, "managed_instance_id": records.instance.ID, "managed_environment_id": records.instance.EnvironmentID, "namespace": a.Namespace, "namespace_uid": a.NamespaceUID, "cluster_ca": ca, "endpoint": server.URL}},
			}}
			raw, err := kubernetes.RuntimeKubeconfig(server.URL, cert, a.Namespace, "fixture-token")
			if err != nil {
				t.Fatal(err)
			}
			secrets := &fleetObservationOpenSecrets{observationSecrets: &observationSecrets{bootstrapSecrets: bootstrapSecrets{}}}
			svc.Secrets = secrets
			if err = svc.putManagedCredential(t.Context(), a.OrgID, a.VaultKey, raw, a.CredentialExpiry); err != nil {
				t.Fatal(err)
			}
			secrets.puts = 0
			target, err := reader.Target(t.Context(), "org", "fleet", "instance")
			if err != nil {
				t.Fatal(err)
			}
			client, err := reader.Open(t.Context(), target)
			if strings.HasPrefix(scenario, "post-read-") {
				if err != nil || client == nil {
					t.Fatalf("initial read: %v", err)
				}
				connections := svc.RuntimeConnections.(*fleetObservationConnections)
				current := connections.items[target.ConnectionID]
				switch scenario {
				case "post-read-disabled":
					current.Status = "disabled"
					connections.items[target.ConnectionID] = current
				case "post-read-details":
					current.Details["managed_instance_id"] = "replacement"
					connections.items[target.ConnectionID] = current
				case "post-read-deleted":
					delete(connections.items, target.ConnectionID)
				}
				if err = reader.Revalidate(t.Context(), target); err == nil {
					t.Fatal("post-read runtime connection change accepted")
				}
				return
			}

			if scenario == "success" {
				if err != nil || client == nil {
					t.Fatalf("open: %v", err)
				}
				if requests != 2 || authority.calls != 2 || qualification.calls != 2 || store.checks != 2 {
					t.Fatalf("checks: reads=%d authority=%d qualification=%d scope=%d", requests, authority.calls, qualification.calls, store.checks)
				}
			} else if err == nil || client != nil {
				t.Fatal("invalid authority or identity opened")
			}
			if (scenario == "authority-denied" || scenario == "qualification-denied" || scenario == "platform-owned" || scenario == "resource-revision" || scenario == "missing-ca") && (requests != 0 || secrets.reads != 0) {
				t.Fatal("denied request reached cluster")
			}
			if writes != 0 || secrets.puts != 0 || secrets.deletes != 0 {
				t.Fatalf("observation wrote state: network=%d puts=%d deletes=%d", writes, secrets.puts, secrets.deletes)
			}
		})
	}
}

type fleetObservationClouds struct {
	clusterClouds
	platformOwned bool
}

func (c fleetObservationClouds) GetProfile(ctx context.Context, org, project, id string) (*cloudprofiles.Profile, error) {
	p, err := c.clusterClouds.GetProfile(ctx, org, project, id)
	p.PlatformOwned = c.platformOwned
	return p, err
}

type fleetObservationOpenSecrets struct {
	*observationSecrets
	reads int
}

func (s *fleetObservationOpenSecrets) Reveal(ctx context.Context, org, key string) ([]byte, error) {
	s.reads++
	return s.observationSecrets.Reveal(ctx, org, key)
}

func TestFleetObservationMissingNamespaceInstallationDenied(t *testing.T) {
	reader, _, _, secrets := fleetObservationTargetFixture(t)
	reader = NewFleetObservationReader(reader.service, reader.records)
	if _, err := reader.Target(t.Context(), "org", "fleet", "instance"); err == nil {
		t.Fatal("observation target accepted without installed namespace authority")
	}
	if secrets.reads != 0 || secrets.writes != 0 {
		t.Fatal("uninstalled observer accessed credentials")
	}
}

// Synthetic HTTP tests inject only the namespace evidence boundary; production
// construction always binds the installed namespace transaction adapter.
type fleetObservationNamespaceFixture struct {
	lifecycle InstanceNamespaceTransactions
	record    *InstanceNamespaceRecord
}

func (f *fleetObservationNamespaceFixture) beginNamespaceCall(ctx context.Context) (context.Context, func(), error) {
	return f.lifecycle.beginNamespaceCall(ctx)
}
func (f *fleetObservationNamespaceFixture) readOriginalReservation(_ context.Context, org, operation string) (namespaceAction, bool, error) {
	if org != f.record.OrgID || operation != f.record.OperationID {
		return namespaceAction{}, false, ErrConflict
	}
	return namespaceAction{AttemptID: f.record.CreationAttemptID, Record: *f.record}, true, nil
}

func TestFleetObservationNamespaceShutdownOwnsSample(t *testing.T) {
	reader, _, _, _ := fleetObservationTargetFixture(t)
	adapter := reader.namespaces.(*fleetObservationNamespaceFixture)
	call, finish, err := reader.BeginObservation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	deadline, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err = adapter.lifecycle.Shutdown(deadline); err == nil {
		t.Fatal("shutdown drained while sample owns namespace adapter")
	}
	select {
	case <-call.Done():
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel sample")
	}
	if adapter.lifecycle.Drained() {
		t.Fatal("sample ownership lost")
	}
	if _, _, err = reader.BeginObservation(t.Context()); err == nil {
		t.Fatal("new observation admitted during shutdown")
	}
	finish()
	if err = adapter.lifecycle.Shutdown(t.Context()); err != nil || !adapter.lifecycle.Drained() {
		t.Fatalf("sample did not drain: %v", err)
	}
}

func TestFleetObservationPostgresInstallationEvidence(t *testing.T) {
	f := newNamespacePGFixture(t)
	reader, base, records, secrets := fleetObservationTargetFixture(t)
	record := f.action.Record
	base.record = record
	records.instance = fleet.Instance{ID: record.Pins.InstanceID, FleetID: record.Pins.FleetID, EnvironmentID: record.Pins.EnvironmentID, DesiredGeneration: record.CurrentGeneration, OwnershipEpoch: record.Pins.OwnershipEpoch}
	records.fleet = &fleet.Fleet{ID: record.Pins.FleetID, OrgID: record.OrgID, ProjectID: record.ProjectID}
	base.operation.ID = record.OperationID
	base.operation.Input.Attachment.Instance = &base.record.Pins
	base.operation.Input.Attachment.ProjectID = record.ProjectID
	base.operation.Input.Attachment.Namespace = record.Namespace
	base.attachment.ProjectID = record.ProjectID
	base.attachment.OperationID = record.OperationID
	base.attachment.Namespace = record.Namespace
	target, err := reader.Target(t.Context(), record.OrgID, record.Pins.FleetID, record.Pins.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	reader.namespaces = f.transactions
	assertDenied := func(t *testing.T) {
		t.Helper()
		if _, targetErr := reader.Target(t.Context(), record.OrgID, record.Pins.FleetID, record.Pins.InstanceID); targetErr == nil {
			t.Fatal("invalid namespace installation returned target")
		}
		if _, openErr := reader.Open(t.Context(), target); openErr == nil {
			t.Fatal("invalid namespace installation opened credentials")
		}
		if validationErr := reader.Revalidate(t.Context(), target); validationErr == nil {
			t.Fatal("invalid namespace installation passed final validation")
		}
		if secrets.reads != 0 || secrets.writes != 0 {
			t.Fatal("denied installation accessed Vault")
		}
	}
	t.Run("missing-original-reservation", assertDenied)
	saved, err := f.transactions.run(t.Context(), f.action)
	if err != nil {
		t.Fatal(err)
	}
	base.record = saved.Record
	if _, err = reader.Target(t.Context(), record.OrgID, record.Pins.FleetID, record.Pins.InstanceID); err != nil {
		t.Fatalf("installed original reservation rejected: %v", err)
	}
	same := namespaceOtherInstallation(t, f, "app", "app")
	reader.namespaces = same
	if _, sameErr := reader.Target(t.Context(), record.OrgID, record.Pins.FleetID, record.Pins.InstanceID); sameErr != nil {
		t.Fatalf("matching second installation rejected: %v", sameErr)
	}
	other := namespaceOtherInstallation(t, f, "other-app", "app")
	t.Run("wrong-installed-app", func(t *testing.T) { reader.namespaces = other; assertDenied(t) })
	t.Run("wrong-saved-app", func(t *testing.T) {
		reader.namespaces = namespaceOtherInstallation(t, f, "other-app", "other-app")
		assertDenied(t)
	})
	t.Run("wrong-creation-attempt", func(t *testing.T) {
		reader.namespaces = f.transactions
		old := base.record.CreationAttemptID
		base.record.CreationAttemptID = "foreign"
		defer func() { base.record.CreationAttemptID = old }()
		assertDenied(t)
	})
	t.Run("stopped", func(t *testing.T) {
		reader.namespaces = f.transactions
		if err := f.transactions.Shutdown(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertDenied(t)
	})
}
