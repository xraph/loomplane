package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/access"
	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/runtimecatalog"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func statusEvidence() (preparationEvidence, time.Time) {
	now := time.Now().UTC()
	scope := domain.Scope{OrgID: "org", ProjectID: "project", EnvironmentID: "env"}
	d := domain.Destination{Scope: scope, ID: "destination", ConnectionID: "conn", Generation: 2, Identity: domain.PhysicalIdentity{Provider: "kubernetes", Account: "cluster", ResourceID: "namespace-uid", NamespaceUID: "namespace-uid"}}
	p := &Preparation{Scope: scope, PreparationIntent: PreparationIntent{ID: "01234567-89ab-cdef-0123-456789abcdef", Namespace: "apps", NamespaceUID: "namespace-uid"}, DestinationID: d.ID, DestinationGeneration: 1, IdentityRevision: d.Identity.Revision(), State: PreparationPrepared, PodUID: "pod-uid", EnrollmentID: "enrollment"}
	name := "loomplane-agent-" + p.ID
	profile := KubernetesAttestationProfile{Scope: scope, DestinationID: d.ID, ConnectionID: d.ConnectionID, IdentityRevision: d.Identity.Revision(), PodName: name, PodUID: p.PodUID, ServiceAccount: name, Container: agentinstall.ContainerName, SPIRECluster: "cluster", SPIRENamespace: "spire", SPIREServiceAccount: "spire-agent"}
	a := domain.AgentIdentity{Scope: scope, AgentID: "agent", DestinationID: d.ID, Epoch: 1}
	a.URI, _ = workloadidentity.AgentURI("loomplane.test", a)
	r := &workloadidentity.EnrollmentRecord{ID: p.EnrollmentID, Binding: workloadidentity.Binding{Agent: a, Destination: d, Method: "kubernetes", Namespace: p.Namespace, NamespaceUID: p.NamespaceUID, PodUID: p.PodUID, ServiceAccount: name, Container: agentinstall.ContainerName}, Registration: runtimecatalog.Registration{Agent: a, Active: true, DestinationGeneration: d.Generation}, ExpiresAt: now.Add(-time.Minute)}
	return preparationEvidence{preparation: p, destination: &d, installation: &KubernetesInstallation{Profile: profile, Generation: 2}, launch: &LaunchAttempt{Scope: scope, LaunchIntent: LaunchIntent{ID: p.ID, EnrollmentID: r.ID, Namespace: p.Namespace, PodUID: p.PodUID, ConfigMapName: name}, DestinationID: d.ID, DestinationGeneration: d.Generation, IdentityRevision: d.Identity.Revision(), State: LaunchDelivered, ConfigMapUID: "config-uid"}, enrollment: r, phase: "consumed", epoch: 1, active: true, policyGeneration: 3, delivery: &access.DeliveryState{Agent: a, Sequence: 5, AcknowledgedSequence: 5, Revision: "revision", PolicyGeneration: 3, AcknowledgedPolicyGeneration: 3, IssuedAt: now.Add(-time.Second), ReceivedAt: now.Add(-time.Second), ExpiresAt: now.Add(30 * time.Second)}}, now
}
func TestPreparationStatusNeverTreatsDeliveryAsCurrentPolicy(t *testing.T) {
	tests := []struct {
		name, want string
		change     func(*preparationEvidence, time.Time)
	}{
		{"current", "policy_current", func(*preparationEvidence, time.Time) {}},
		{"old consumed bootstrap", "policy_current", func(e *preparationEvidence, n time.Time) { e.enrollment.Binding.CheckedAt = n.Add(-time.Hour) }},
		{"renewal awaiting acknowledgment", "policy_pending", func(e *preparationEvidence, n time.Time) { e.delivery.Sequence++; e.delivery.IssuedAt = n }},
		{"retiring", "retiring", func(e *preparationEvidence, _ time.Time) { e.preparation.State = PreparationRetiring }},
		{"retired history", "retired", func(e *preparationEvidence, _ time.Time) {
			e.preparation.State = PreparationRetired
			e.enrollment = nil
			e.installation = nil
			e.launch = nil
		}},
		{"preparing", "preparing", func(e *preparationEvidence, _ time.Time) {
			e.preparation.State = PreparationPodPending
			e.destination.Generation = 1
			e.installation = nil
			e.launch = nil
			e.preparation.EnrollmentID = ""
		}},
		{"superseded while preparing", "superseded", func(e *preparationEvidence, _ time.Time) { e.preparation.State = PreparationPodPending }},
		{"unrecorded installation", "awaiting_launch", func(e *preparationEvidence, _ time.Time) {
			e.destination.Generation = 1
			e.installation = nil
			e.launch = nil
			e.preparation.EnrollmentID = ""
		}},
		{"lost delivery reply", "delivery_pending", func(e *preparationEvidence, _ time.Time) { e.launch.State = LaunchPending; e.launch.ConfigMapUID = "" }},
		{"delivered not enrolled", "enrolling", func(e *preparationEvidence, n time.Time) {
			e.phase = "ready"
			e.enrollment.ExpiresAt = n.Add(time.Minute)
		}},
		{"expired bootstrap", "bootstrap_expired", func(e *preparationEvidence, _ time.Time) { e.phase = "ready" }},
		{"consumed without policy", "awaiting_policy", func(e *preparationEvidence, _ time.Time) { e.delivery = nil }},
		{"new delivery not applied", "policy_pending", func(e *preparationEvidence, _ time.Time) { e.delivery.Sequence++ }},
		{"policy changed", "policy_pending", func(e *preparationEvidence, _ time.Time) { e.policyGeneration++ }},
		{"wrong acknowledged policy", "policy_pending", func(e *preparationEvidence, _ time.Time) { e.delivery.AcknowledgedPolicyGeneration-- }},
		{"expired lease", "lease_expired", func(e *preparationEvidence, n time.Time) { e.delivery.ExpiresAt = n.Add(domain.MaxClockSkew) }},
		{"missing server receipt", "awaiting_policy", func(e *preparationEvidence, _ time.Time) { e.delivery.ReceivedAt = time.Time{} }},
		{"future receipt", "awaiting_policy", func(e *preparationEvidence, n time.Time) { e.delivery.ReceivedAt = n.Add(time.Second) }},
		{"inactive observer", "revoked", func(e *preparationEvidence, _ time.Time) { e.active = false }},
		{"missing retained enrollment", "revoked", func(e *preparationEvidence, _ time.Time) { e.enrollment = nil }},
		{"revoking", "revoked", func(e *preparationEvidence, _ time.Time) { e.phase = "revoking" }},
		{"new epoch", "superseded", func(e *preparationEvidence, _ time.Time) { e.epoch++ }},
		{"new destination generation", "superseded", func(e *preparationEvidence, _ time.Time) { e.destination.Generation++ }},
		{"replaced pod", "superseded", func(e *preparationEvidence, _ time.Time) { e.installation.Profile.PodUID = "replacement" }},
		{"foreign launch", "superseded", func(e *preparationEvidence, _ time.Time) { e.launch.DestinationID = "foreign" }},
		{"foreign binding namespace", "superseded", func(e *preparationEvidence, _ time.Time) { e.enrollment.Binding.NamespaceUID = "foreign" }},
		{"foreign enrollment", "superseded", func(e *preparationEvidence, _ time.Time) { e.enrollment.Binding.Agent.DestinationID = "foreign" }},
		{"quarantined destination", "unverified", func(e *preparationEvidence, _ time.Time) { e.destination.Quarantined = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, now := statusEvidence()
			tt.change(&e, now)
			got := e.status(now)
			if got.State != tt.want {
				t.Fatalf("got %q, want %q", got.State, tt.want)
			}
			if got.State == "policy_current" && !got.PolicyValidUntil.After(now) {
				t.Fatal("current policy has no remaining lease")
			}
		})
	}
}

type statusReader struct {
	result *PreparationStatus
	err    error
	calls  int
	read   func()
}

func (s *statusReader) ReadPreparationStatus(context.Context, string, string, string) (*PreparationStatus, error) {
	s.calls++
	if s.read != nil {
		s.read()
	}
	return s.result, s.err
}
func TestPreparationStatusRechecksScopeAndPermission(t *testing.T) {
	f := newPreparedLaunchFixture(t)
	d := f.installation.destinations.rows[f.input.DestinationID]
	read := &statusReader{result: &PreparationStatus{Scope: d.Scope, PreparationID: f.input.PreparationID, DestinationID: d.ID, DestinationGeneration: d.Generation, IdentityRevision: d.Identity.Revision(), State: "preparing"}}
	svc := NewPreparationStatusService(f.service.preparation.launch.installations, read)
	ro := f.principal
	ro.ReadOnly = true
	if _, err := svc.Get(t.Context(), ro, d.ID, f.input.PreparationID); err != nil {
		t.Fatal(err)
	}
	read.read = func() { f.service.preparation.launch.installations.destinations.authz = &authorizer{deny: true} }
	if got, err := svc.Get(t.Context(), f.principal, d.ID, f.input.PreparationID); err == nil || got != nil {
		t.Fatal("revoked read permission returned status", err)
	}
	f.service.preparation.launch.installations.destinations.authz = &authorizer{}
	read.read = nil
	read.result.OrgID = "foreign"
	if got, err := svc.Get(t.Context(), f.principal, d.ID, f.input.PreparationID); !errors.Is(err, ErrIdentityChanged) || got != nil {
		t.Fatal("foreign status returned", err)
	}
	before := read.calls
	if _, err := svc.Get(t.Context(), security.Principal{}, d.ID, f.input.PreparationID); err == nil {
		t.Fatal("anonymous read accepted")
	}
	if before != read.calls {
		t.Fatal("anonymous read touched store")
	}
}

func TestPreparationStatusFailsClosedAfterRead(t *testing.T) {
	for _, mode := range []string{"changed destination", "expired during read", "store failure", "disabled", "foreign destination", "foreign organization"} {
		t.Run(mode, func(t *testing.T) {
			f := newPreparedLaunchFixture(t)
			installations := f.service.preparation.launch.installations
			d := f.installation.destinations.rows[f.input.DestinationID]
			until := time.Now().Add(time.Minute)
			read := &statusReader{result: &PreparationStatus{Scope: d.Scope, PreparationID: f.input.PreparationID, DestinationID: d.ID, DestinationGeneration: d.Generation, IdentityRevision: d.Identity.Revision(), State: "policy_current", PolicyValidUntil: &until}}
			svc := NewPreparationStatusService(installations, read)
			principal := f.principal
			switch mode {
			case "changed destination":
				read.read = func() { next := d; next.Generation++; f.installation.destinations.rows[d.ID] = next }
			case "expired during read":
				svc.now = func() time.Time { return until }
			case "store failure":
				read.err = errors.New("database unavailable")
			case "disabled":
				installations.destinations.enabled = false
			case "foreign destination":
				read.result.DestinationID = "foreign"
			case "foreign organization":
				principal.OrgID = "foreign"
			}
			got, err := svc.Get(t.Context(), principal, d.ID, f.input.PreparationID)
			if mode == "expired during read" {
				if err != nil || got.State != "lease_expired" {
					t.Fatal(got, err)
				}
				return
			}
			if err == nil || got != nil {
				t.Fatal("unusable or unauthorized evidence returned", got, err)
			}
			if mode == "store failure" && !errors.Is(err, read.err) {
				t.Fatal("lost storage failure", err)
			}
			if (mode == "disabled" || mode == "foreign organization") && read.calls != 0 {
				t.Fatal("denied read touched status store")
			}
		})
	}
}
