//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	federationstore "github.com/xraph/loomplane/internal/federation"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

type groupObserver struct {
	secretUID    string
	namespaceUID string
	expired      bool
	renewed      bool
}

func (o *groupObserver) Observe(_ context.Context, a *cloudinfra.RuntimeAttachment, name string) (planning.MemberPrerequisiteObservation, error) {
	now := time.Now().UTC()
	uid := "secret-" + a.ID
	if o.secretUID != "" {
		uid = o.secretUID
	}
	ns := a.NamespaceUID
	if o.namespaceUID != "" {
		ns = o.namespaceUID
	}
	expiry := now.Add(30 * 24 * time.Hour)
	if o.expired {
		expiry = now.Add(time.Hour)
	}
	if o.renewed {
		expiry = now.Add(60 * 24 * time.Hour)
	}
	return planning.MemberPrerequisiteObservation{Namespace: a.Namespace, NamespaceUID: ns, SecretName: name, SecretUID: uid, Hostnames: []string{"api.example.com"}, ResourceVersion: fmt.Sprint(now.UnixNano()), Fingerprint: fmt.Sprint(o.renewed), NotBefore: now.Add(-time.Hour), NotAfter: expiry, CheckedAt: now}, nil
}

type groupAuth struct{ denied bool }

func (a *groupAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	if a.denied {
		return security.ErrForbidden
	}
	return nil
}

type placementGroupFixture struct {
	f             *poolFixture
	service       *planning.MemberPrerequisiteService
	plan          *planning.Plan
	manifest      *manifest.Manifest
	observer      *groupObserver
	authorities   []*domain.TLSRenewalAuthority
	prerequisites []*domain.MemberPrerequisite
}

func newPlacementGroupFixture(t *testing.T) *placementGroupFixture {
	t.Helper()
	f := newPoolFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	scope := domain.Scope{OrgID: "org", ProjectID: "proj", EnvironmentID: "env"}
	observer := &groupObserver{}
	service := &planning.MemberPrerequisiteService{Store: f.plans, Authz: &groupAuth{}, Projects: projects.NewPgStore(f.db), Observer: observer}
	m := &manifest.Manifest{Version: 2, Services: map[string]manifest.Service{"api": {Source: ".", Kind: "http-service", Settings: manifest.Settings{Port: 8080, Build: &manifest.Build{Image: "registry.example/api@sha256:" + strings.Repeat("a", 64)}}}}, Federation: &manifest.Federation{Destinations: map[string]manifest.DestinationRef{}, PlacementGroups: map[string]manifest.FederationPlacementGroup{}}}
	group := manifest.FederationPlacementGroup{Service: "api", MinReady: 2, Routing: manifest.GroupRouting{Mode: "external", Hostname: "api.example.com"}, Members: map[string]manifest.PlacementMember{}}
	out := &placementGroupFixture{f: f, service: service, manifest: m, observer: observer}
	account := &connections.Connection{ID: "group-provider", OrgID: "org", Cloud: "digitalocean", Name: "Group provider", Account: "account", Details: map[string]string{"account_uuid": "account"}, Status: "ready", CreatedAt: now, UpdatedAt: now}
	if err := connections.NewPgStore(f.db).CreateConnection(ctx, account); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"east", "west"} {
		id := "dest_0000000000000000000000000" + fmt.Sprint(i+1)
		attachment := "group-attachment-" + name
		cluster := "group-cluster-" + name
		connection := "group-runtime-" + name
		ca := strings.Repeat(fmt.Sprint(i+1), 64)
		ns := "group-ns-" + name
		c := &connections.Connection{ID: connection, OrgID: "org", Cloud: "kubernetes", Method: connections.MethodManagedKubernetes, Name: connection, Status: "ready", CheckedAt: &now, Details: map[string]string{"namespace": "apps", "namespace_uid": ns, "cluster_ca": ca, "managed_attachment_id": attachment, "managed_resource_id": cluster, "managed_project_id": "proj"}, CreatedAt: now, UpdatedAt: now}
		if err := connections.NewPgStore(f.db).CreateConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
		ref := provider.ClusterRef{ID: cluster, AccountID: connections.AccountIdentity(account.Cloud, account.Account, account.Details), Region: name, Incarnation: "birth"}
		resource := &cloudinfra.ManagedResource{ID: cluster, OrgID: "org", CloudID: "cloud", Kind: "cluster", Provider: "digitalocean", AccountID: connections.AccountIdentity(account.Cloud, account.Account, account.Details), ConnectionID: account.ID, ExternalID: cluster, Incarnation: "birth", Ownership: "external", Status: "running", Revision: 1, SchemaVersion: 1, Observation: &provider.ClusterObservation{Ref: ref}}
		if _, err := f.store.ClaimManagedResource(ctx, resource, 0); err != nil {
			t.Fatal(err)
		}
		a := &cloudinfra.RuntimeAttachment{ID: attachment, OrgID: "org", CloudID: "cloud", ClusterID: cluster, ProjectID: "proj", Namespace: "apps", NamespaceUID: ns, ServiceAccountName: "runtime", ServiceAccountUID: "sa-" + name, RuntimeConnectionID: connection, TrustFingerprint: ca, TrustRevision: 1, Revision: 1, SchemaVersion: 1, CreatedAt: now, UpdatedAt: now}
		if err := f.store.SaveRuntimeAttachment(ctx, a, 0); err != nil {
			t.Fatal(err)
		}
		location := &cloudprofiles.CloudLocation{ID: "group-location-" + name, OrgID: "org", CloudID: "cloud", Name: "Group " + name, Provider: "digitalocean", AccountID: connections.AccountIdentity(account.Cloud, account.Account, account.Details), ConnectionID: account.ID, Region: name, Revision: 1, EvidenceRevision: 1, EvidenceSource: "operator-attested", EvidenceRef: "verification", VerifiedBy: "usr", CheckedAt: now}
		profile, err := f.clouds.GetProfile(ctx, "org", "", "cloud")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.clouds.SaveLocation(ctx, location, profile.Revision, 0); err != nil {
			t.Fatal(err)
		}
		// Match the provider account identity used by the authoritative location.
		if resource.AccountID != location.AccountID {
			t.Fatalf("fixture account mismatch %s/%s", resource.AccountID, location.AccountID)
		}
		target := "group-target-" + name
		if err = f.clouds.CreateTarget(ctx, &cloudprofiles.Target{ID: target, OrgID: "org", ProjectID: "proj", CloudID: "cloud", Name: target, DisplayName: target, Provider: "kubernetes", Runtime: "managed", ManagedResourceID: cluster, ConnectionID: connection, Region: name, LocationID: location.ID, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		physical := domain.PhysicalIdentity{Provider: "kubernetes", Account: ca, ResourceID: ns, NamespaceUID: ns}
		d := &domain.Destination{Scope: scope, ID: id, Name: name, ConnectionID: connection, Ownership: domain.Imported, Identity: physical, Generation: 1, CreatedAt: now, UpdatedAt: now, Capabilities: domain.CapabilitySet{Revision: "cap1", IdentityRevision: physical.Revision(), CheckedAt: now, ExpiresAt: now.Add(5 * time.Minute), Results: map[domain.Capability]domain.CapabilityResult{domain.Observe: {State: domain.Supported}, domain.ImmutableArtifact: {State: domain.Supported}, domain.HTTPProtocol: {State: domain.Supported}}}}
		if err = federationstore.NewPgStore(f.db).Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err = service.SaveDestinationMapping(ctx, actor, scope, id, target, attachment, 0); err != nil {
			t.Fatal(err)
		}
		authority, err := service.SaveRenewalAuthority(ctx, actor, planning.TLSRenewalAuthorityInput{ProjectID: "proj", AttachmentID: attachment, OwnerRef: "operator", Method: "external-dns01", VerificationEvidenceRef: "procedure-" + name, Active: true})
		if err != nil {
			t.Fatal(err)
		}
		prereq, err := service.Save(ctx, actor, "proj", planning.MemberPrerequisiteInput{AttachmentID: attachment, TLSSecretName: "tls", ExpectedTLSSecretUID: "secret-" + attachment, RenewalAuthorityID: authority.ID, ExpectedRenewalAuthorityGeneration: authority.Generation, DrainPolicy: domain.DrainPolicy{Mode: "attested"}})
		if err != nil {
			t.Fatal(err)
		}
		out.authorities = append(out.authorities, authority)
		out.prerequisites = append(out.prerequisites, prereq)
		m.Federation.Destinations[name] = manifest.DestinationRef{ID: id}
		group.Members[name] = manifest.PlacementMember{Destination: name, Replicas: 1, PrerequisiteID: prereq.ID, PrerequisiteRevision: prereq.Revision}
	}
	m.Federation.PlacementGroups["web"] = group
	m.Federation.PlacementGroups["other"] = group
	p := &planning.Plan{ID: "group-plan", OrgID: "org", ProjectID: "proj", EnvironmentID: "env", Environment: "production", Status: planning.StatusPlanning, ManifestID: "pool-manifest-proj", ManifestRevision: 1, CreatedBy: "usr", CreatedAt: now}
	if _, err := f.plans.CreatePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	out.plan = p
	return out
}
func TestPlacementGroupIdentityPersistence(t *testing.T) {
	x := newPlacementGroupFixture(t)
	ctx := t.Context()
	groups, err := x.service.PreviewPlacementGroups(ctx, x.plan, x.manifest, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Service != groups[1].Service || groups[0].Members[0].Identity.BindingID == groups[1].Members[0].Identity.BindingID {
		t.Fatal("group service names alias")
	}
	p := x.plan
	p.PlacementGroups = groups
	p.Status = planning.StatusBlocked
	p.Revision = "review"
	now := time.Now().UTC()
	p.PlannedAt = &now
	if ok, e := x.f.plans.FinishPlanning(ctx, p); e != nil || !ok {
		t.Fatalf("finish: %v %v", ok, e)
	}
	reopened := planning.NewPgStore(openPg(t, x.f.sql.Config().ConnString()))
	saved, err := reopened.GetPlan(ctx, "org", p.ID)
	if err != nil || !reflect.DeepEqual(saved.PlacementGroups, groups) {
		t.Fatalf("plan reload lost identities: %v", err)
	}
	for _, g := range groups {
		stored, e := reopened.GetPlacementGroupReview(ctx, g.Scope, g.GroupID, g.Generation)
		if e != nil || !reflect.DeepEqual(*stored, g) {
			t.Fatalf("review reload: %v", e)
		}
	}
	x.observer.renewed = true
	if err = x.service.RevalidatePlacementGroupReview(ctx, groups[0]); err != nil {
		t.Fatalf("same-identity renewal invalidated review: %v", err)
	}
	replay, err := x.service.PreviewPlacementGroups(ctx, p, x.manifest, nil, nil)
	if err != nil || !reflect.DeepEqual(groups, replay) {
		t.Fatalf("re-preview changed review: %v", err)
	}
	for _, q := range []string{`UPDATE loomplane_placement_group_members SET runtime_ref='alias'`, `DELETE FROM loomplane_placement_group_members`, `DELETE FROM loomplane_runtime_attachments WHERE id='group-attachment-east'`, `DELETE FROM loomplane_federation_destinations WHERE name='east'`, `UPDATE loomplane_plans SET status='approved',executable=true WHERE id='group-plan'`} {
		if _, e := x.f.sql.Exec(ctx, q); e == nil {
			t.Fatalf("unsafe mutation accepted: %s", q)
		}
	}
	var n int
	if err = x.f.sql.QueryRow(ctx, `SELECT count(*) FROM loomplane_bindings WHERE name='api'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("group leaked into legacy bindings: %d %v", n, err)
	}
	if _, err = reopened.GetPlacementGroupReview(ctx, domain.Scope{OrgID: "other", ProjectID: "proj", EnvironmentID: "env"}, groups[0].GroupID, groups[0].Generation); !errors.Is(err, planning.ErrNotFound) {
		t.Fatalf("foreign read: %v", err)
	}
}
func TestPlacementGroupIdentityStaleAuthority(t *testing.T) {
	for _, kind := range []string{"grant", "namespace", "secret", "renewal", "prerequisite", "destination", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			x := newPlacementGroupFixture(t)
			ctx := t.Context()
			groups, err := x.service.PreviewPlacementGroups(ctx, x.plan, x.manifest, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "destination":
				_, err = x.f.sql.Exec(ctx, `UPDATE loomplane_federation_destinations SET generation=generation+1 WHERE id=$1`, groups[0].Members[0].DestinationID)
			case "expiry":
				x.observer.expired = true
			case "grant":
				_, err = x.f.clouds.SetProjectGrant(ctx, "org", "cloud", "proj", nil, true)
			case "namespace":
				x.observer.namespaceUID = "recreated"
			case "secret":
				x.observer.secretUID = "recreated"
			case "renewal":
				a := x.authorities[0]
				_, err = x.service.SaveRenewalAuthority(ctx, security.Principal{OrgID: "org", UserID: "usr"}, planning.TLSRenewalAuthorityInput{ID: a.ID, ProjectID: "proj", AttachmentID: x.prerequisites[0].AttachmentID, ExpectedGeneration: a.Generation, OwnerRef: a.OwnerRef, Method: a.Method, VerificationEvidenceRef: a.VerificationEvidenceRef, Active: false})
			case "prerequisite":
				p := x.prerequisites[0]
				_, err = x.service.Save(ctx, security.Principal{OrgID: "org", UserID: "usr"}, "proj", planning.MemberPrerequisiteInput{ID: p.ID, AttachmentID: p.AttachmentID, ExpectedRevision: p.Revision, TLSSecretName: p.TLSSecretName, ExpectedTLSSecretUID: p.TLSSecretUID, RenewalAuthorityID: p.RenewalAuthorityID, ExpectedRenewalAuthorityGeneration: p.RenewalAuthorityGeneration, DrainPolicy: p.DrainPolicy})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = x.service.RevalidatePlacementGroupReview(ctx, groups[0]); err == nil {
				t.Fatal("stale authority accepted")
			}
		})
	}
}
func TestPlacementGroupIdentityPrerequisiteAuthority(t *testing.T) {
	x := newPlacementGroupFixture(t)
	ctx := t.Context()
	p := x.prerequisites[0]
	in := planning.MemberPrerequisiteInput{ID: p.ID, AttachmentID: p.AttachmentID, ExpectedRevision: p.Revision, TLSSecretName: p.TLSSecretName, ExpectedTLSSecretUID: p.TLSSecretUID, RenewalAuthorityID: p.RenewalAuthorityID, ExpectedRenewalAuthorityGeneration: p.RenewalAuthorityGeneration, DrainPolicy: p.DrainPolicy}
	actor := security.Principal{OrgID: "org", UserID: "usr"}
	if _, err := x.service.Save(ctx, actor, "second", in); err == nil {
		t.Fatal("cross-project save")
	}
	in.ExpectedRevision = 0
	if _, err := x.service.Save(ctx, actor, "proj", in); err == nil {
		t.Fatal("stale prerequisite save")
	}
	in.ExpectedRevision = p.Revision
	n := int64(30)
	in.DrainPolicy = domain.DrainPolicy{Mode: "bounded", MaxConnectionLifetimeSeconds: &n, TerminationGraceSeconds: 30}
	if _, err := x.service.Save(ctx, actor, "proj", in); err == nil {
		t.Fatal("caller duration substituted for verified application timeout")
	}
	config, err := planning.GroupConfigurationRevision(x.manifest, "production", "api")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.service.VerifyTimeout(ctx, actor, "proj", planning.TimeoutVerificationInput{AttachmentID: p.AttachmentID, ExpectedAttachmentRevision: 1, ConfigRevision: config, DrainPolicy: in.DrainPolicy, EvidenceRef: "operator-checked-config"}); err != nil {
		t.Fatal(err)
	}
	saved, err := x.service.Save(ctx, actor, "proj", in)
	if err != nil || saved.TimeoutVerificationID == "" {
		t.Fatalf("verified timeout: %v", err)
	}
	raw, err := json.Marshal(saved)
	if err != nil || strings.Contains(string(raw), "kubeconfig") || strings.Contains(string(raw), "private_key") {
		t.Fatal("credential escaped")
	}
	if _, err = x.f.sql.Exec(ctx, `INSERT INTO loomplane_variables(id,org_id,project_id,environment_id,environment,service,key,value,secret,created_by,updated_by,created_at,updated_at) VALUES('group-variable','org','proj','env','production','','MODE','production',false,'usr','usr',now(),now())`); err != nil {
		t.Fatal(err)
	}
	if _, err = x.service.PreviewPlacementGroups(ctx, x.plan, x.manifest, nil, nil); !errors.Is(err, planning.ErrGroupVariableSnapshotsUnavailable) {
		t.Fatalf("mutable variable qualified: %v", err)
	}
	x.service.Authz = &groupAuth{denied: true}
	if _, err = x.service.Save(ctx, actor, "proj", in); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("manager denial: %v", err)
	}
}

func TestPlacementGroupIdentityFleetOwnership(t *testing.T) {
	x := newPlacementGroupFixture(t)
	ctx := t.Context()
	groups, err := x.service.PreviewPlacementGroups(ctx, x.plan, x.manifest, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = x.f.sql.Exec(ctx, `UPDATE loomplane_projects SET deployment_mode='fleet' WHERE org_id='org' AND id='proj'; INSERT INTO loomplane_fleets(id,org_id,project_id,revision,status,created_at,updated_at) VALUES('group-fleet','org','proj',1,'active',now(),now()); INSERT INTO loomplane_fleet_stages(id,org_id,project_id,fleet_id,name,environment_kind,max_instances,policy_revision,created_at) VALUES('group-stage','org','proj','group-fleet','production','production',10,1,now())`); err != nil {
		t.Fatal(err)
	}
	if err = x.service.RevalidatePlacementGroupReview(ctx, groups[0]); err != nil {
		t.Fatalf("unmanaged environment in fleet project rejected: %v", err)
	}
	if _, err = x.f.sql.Exec(ctx, `INSERT INTO loomplane_fleet_instances(id,org_id,project_id,fleet_id,stage_id,environment_id,external_ref,profile_id,desired_release_id,status,revision,desired_generation,observed_generation,ownership_epoch,created_at,updated_at) VALUES('group-instance','org','proj','group-fleet','group-stage','env','owned','profile','release','deleted',1,1,0,1,now(),now())`); err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]func() error{
		"current review": func() error { return x.f.plans.ValidatePlacementGroupReview(ctx, groups[0], time.Now()) },
		"save":           func() error { return x.f.plans.SavePlacementGroupReview(ctx, x.plan, groups[0]) },
		"identity":       func() error { _, _, e := x.f.plans.PlacementGroupIdentity(ctx, x.plan, "new-group"); return e },
		"preview":        func() error { _, e := x.service.PreviewPlacementGroups(ctx, x.plan, x.manifest, nil, nil); return e },
	} {
		if err = check(); !errors.Is(err, projects.ErrFleetManaged) {
			t.Fatalf("%s bypassed fleet ownership: %v", name, err)
		}
	}
}
