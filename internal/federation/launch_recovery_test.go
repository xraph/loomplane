package federation

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

type launchMemory struct{ record *LaunchAttempt }

func (m *launchMemory) ReserveLaunch(ctx context.Context, d domain.Destination, in LaunchIntent, actor string, guard func(context.Context) error) (*LaunchAttempt, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	if m.record != nil {
		return nil, ErrConflict
	}
	m.record = &LaunchAttempt{Scope: d.Scope, LaunchIntent: in, DestinationID: d.ID, DestinationGeneration: d.Generation, IdentityRevision: d.Identity.Revision(), State: LaunchPending, Generation: 1, RecordedBy: actor}
	copyRecord := *m.record
	return &copyRecord, nil
}
func (m *launchMemory) GetLaunch(_ context.Context, org, id string) (*LaunchAttempt, error) {
	if m.record == nil || m.record.OrgID != org || m.record.ID != id {
		return nil, ErrNotFound
	}
	copyRecord := *m.record
	return &copyRecord, nil
}
func (m *launchMemory) ConfirmLaunch(ctx context.Context, org, id string, expected int64, uid, actor string, guard func(context.Context) error) (*LaunchAttempt, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	if m.record == nil || m.record.OrgID != org || m.record.ID != id || m.record.Generation != expected || m.record.State != LaunchPending {
		return nil, ErrConflict
	}
	m.record.Generation++
	m.record.State = LaunchDelivered
	m.record.ConfigMapUID = uid
	m.record.RecordedBy = actor
	copyRecord := *m.record
	return &copyRecord, nil
}

func TestLaunchRecoveryConfirmsLostResponseWithoutReissuing(t *testing.T) {
	svc, ready, _, p, in, _ := launchServiceFixture(t)
	ledger := &launchMemory{}
	svc.attempts = ledger
	writes := 0
	var created *corev1.ConfigMap
	svc.deliver = func(_ context.Context, plan *agentinstall.KubernetesPlan, _ domain.Destination, r workloadidentity.EnrollmentRecord, s agentinstall.LaunchSettings, _ func(context.Context) error) (*corev1.ConfigMap, error) {
		writes++
		if ledger.record == nil || ledger.record.State != LaunchPending {
			t.Fatal("provider write preceded durable intent")
		}
		var err error
		created, err = plan.DesiredConfigMap(r, s)
		if err != nil {
			t.Fatal(err)
		}
		created.UID = "created-uid"
		return nil, errors.New("response lost")
	}
	if _, err := svc.Deliver(t.Context(), p, in); err == nil {
		t.Fatal("uncertain result reported successful")
	}
	if _, err := svc.Deliver(t.Context(), p, in); !errors.Is(err, ErrConflict) || writes != 1 {
		t.Fatal("uncertain write automatically retried", err, writes)
	}
	// Simulate process restart and an agent that has already consumed the handle.
	restarted := NewLaunchService(svc.installations, ready, svc.credentials, svc.keys, svc.cfg, svc.org, ledger)
	ready.fail = true
	reads := 0
	restarted.inspect = func(context.Context, domain.Destination, *LaunchAttempt) (*corev1.ConfigMap, error) {
		reads++
		return created.DeepCopy(), nil
	}
	result, err := restarted.Recover(t.Context(), p, in.DestinationID, in.AttemptID)
	if err != nil || result.ConfigMapUID != "created-uid" || writes != 1 || reads != 1 || ledger.record.State != LaunchDelivered {
		t.Fatal("lost response not reconciled", err)
	}
	if _, err = restarted.Recover(t.Context(), p, in.DestinationID, in.AttemptID); err != nil || reads != 1 {
		t.Fatal("confirmed evidence was not idempotent", err)
	}
}
func TestLaunchRecoveryRejectsUnrelatedResource(t *testing.T) {
	for _, mode := range []string{"content", "owner", "permission", "missing"} {
		t.Run(mode, func(t *testing.T) {
			svc, _, _, p, in, _ := launchServiceFixture(t)
			ledger := &launchMemory{}
			svc.attempts = ledger
			var created *corev1.ConfigMap
			svc.deliver = func(_ context.Context, plan *agentinstall.KubernetesPlan, _ domain.Destination, r workloadidentity.EnrollmentRecord, s agentinstall.LaunchSettings, _ func(context.Context) error) (*corev1.ConfigMap, error) {
				created, _ = plan.DesiredConfigMap(r, s)
				created.UID = "created-uid"
				return nil, errors.New("unknown result")
			}
			if _, err := svc.Deliver(t.Context(), p, in); err == nil {
				t.Fatal("fixture write did not fail")
			}
			svc.inspect = func(context.Context, domain.Destination, *LaunchAttempt) (*corev1.ConfigMap, error) {
				switch mode {
				case "content":
					created.Data[agentinstall.ConfigKey] = "{}"
				case "owner":
					created.OwnerReferences[0].UID = "foreign-pod"
				case "permission":
					svc.installations.destinations.authz = &authorizer{deny: true}
				case "missing":
					return nil, ErrNotFound
				}
				return created, nil
			}
			if _, err := svc.Recover(t.Context(), p, in.DestinationID, in.AttemptID); err == nil || ledger.record.State != LaunchPending {
				t.Fatal("unrelated or unverified result confirmed", err)
			}
		})
	}
}

type launchScopedAuth struct{ destination string }

func (a launchScopedAuth) Require(_ context.Context, _ security.Principal, _ security.Permission, id string) error {
	if id != a.destination {
		return security.ErrForbidden
	}
	return nil
}
func TestLaunchRecoveryUsesDestinationPermission(t *testing.T) {
	svc, _, _, p, in, _ := launchServiceFixture(t)
	if _, err := svc.Deliver(t.Context(), p, in); err != nil {
		t.Fatal(err)
	}
	svc.installations.destinations.authz = launchScopedAuth{destination: "other"}
	if _, err := svc.Recover(t.Context(), p, "other", in.AttemptID); !errors.Is(err, security.ErrForbidden) {
		t.Fatal("attempt accepted under another destination", err)
	}
	svc.installations.destinations.authz = launchScopedAuth{destination: in.DestinationID}
	for _, change := range []func(*security.Principal){func(p *security.Principal) { p.UserID = "" }, func(p *security.Principal) { p.OrgID = "foreign" }, func(p *security.Principal) { p.ReadOnly = true }} {
		caller := p
		change(&caller)
		if _, err := svc.Recover(t.Context(), caller, in.DestinationID, in.AttemptID); err == nil {
			t.Fatal("unauthorized recovery")
		}
	}
	if _, err := svc.Recover(t.Context(), p, in.DestinationID, in.AttemptID); err != nil {
		t.Fatal("scoped operator denied", err)
	}
}
