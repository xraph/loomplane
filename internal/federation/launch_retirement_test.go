package federation

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/xraph/loomplane/internal/agentinstall"
	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

func (m *launchMemory) BeginLaunchRetirement(ctx context.Context, org, id string, expected int64, actor string, guard func(context.Context) error) (*LaunchAttempt, error) {
	return m.transitionRetirement(ctx, org, id, expected, actor, LaunchRetiring, guard)
}
func (m *launchMemory) CompleteLaunchRetirement(ctx context.Context, org, id string, expected int64, actor string, guard func(context.Context) error) (*LaunchAttempt, error) {
	return m.transitionRetirement(ctx, org, id, expected, actor, LaunchRetired, guard)
}
func (m *launchMemory) transitionRetirement(ctx context.Context, org, id string, expected int64, actor, target string, guard func(context.Context) error) (*LaunchAttempt, error) {
	if err := guard(ctx); err != nil {
		return nil, err
	}
	r, err := m.GetLaunch(ctx, org, id)
	if err != nil {
		return nil, err
	}
	if r.Generation != expected || !launchTransitionAllowed(r.State, target) {
		return nil, ErrConflict
	}
	m.record.State = target
	m.record.Generation++
	m.record.RecordedBy = actor
	return m.GetLaunch(ctx, org, id)
}
func TestLaunchRetirementResumesAfterRevocationFailure(t *testing.T) {
	launch, _, _, p, in, _ := launchServiceFixture(t)
	launch.deliver = func(context.Context, *agentinstall.KubernetesPlan, domain.Destination, workloadidentity.EnrollmentRecord, agentinstall.LaunchSettings, func(context.Context) error) (*corev1.ConfigMap, error) {
		return nil, errors.New("unknown write result")
	}
	if _, err := launch.Deliver(t.Context(), p, in); err == nil {
		t.Fatal("fixture delivered")
	}
	attempts, ok := launch.attempts.(*launchMemory)
	if !ok {
		t.Fatal("unexpected fixture")
	}
	revokes, cleanups := 0, 0
	fail := true
	revoke := func(context.Context, string, string) error {
		revokes++
		if attempts.record.State != LaunchRetiring {
			t.Fatal("revoked before durable retirement")
		}
		if fail {
			return errors.New("SPIRE unavailable")
		}
		return nil
	}
	svc := NewLaunchRetirementService(launch, revoke)
	svc.cleanup = func(_ context.Context, _ domain.Destination, _ *LaunchAttempt, guard func(context.Context) error) error {
		cleanups++
		return guard(t.Context())
	}
	if _, err := svc.Retire(t.Context(), p, in.DestinationID, in.AttemptID); err == nil || attempts.record.State != LaunchRetiring || cleanups != 0 {
		t.Fatal("failed revocation released cleanup", err)
	}
	fail = false
	if _, err := launch.Recover(t.Context(), p, in.DestinationID, in.AttemptID); !errors.Is(err, ErrConflict) {
		t.Fatal("retiring launch recovered", err)
	}
	result, err := svc.Retire(t.Context(), p, in.DestinationID, in.AttemptID)
	if err != nil || result.State != LaunchRetired || revokes != 2 || cleanups != 1 {
		t.Fatal("retirement not resumed", err)
	}
	if _, err = svc.Retire(t.Context(), p, in.DestinationID, in.AttemptID); err != nil || revokes != 2 || cleanups != 1 {
		t.Fatal("retired attempt performed side effects", err)
	}
}

func TestLaunchRetirementRechecksPermissionAndPhysicalIdentity(t *testing.T) {
	for _, mode := range []string{"read only", "foreign destination", "permission after revoke", "permission after cleanup", "physical identity", "new installation"} {
		t.Run(mode, func(t *testing.T) {
			launch, _, installations, p, in, _ := launchServiceFixture(t)
			if _, err := launch.Deliver(t.Context(), p, in); err != nil {
				t.Fatal(err)
			}
			ledger, ok := launch.attempts.(*launchMemory)
			if !ok {
				t.Fatal("unexpected fixture")
			}
			revokes, cleanups := 0, 0
			svc := NewLaunchRetirementService(launch, func(context.Context, string, string) error {
				revokes++
				if mode == "permission after revoke" {
					launch.installations.destinations.authz = &authorizer{deny: true}
				}
				return nil
			})
			svc.cleanup = func(ctx context.Context, _ domain.Destination, _ *LaunchAttempt, guard func(context.Context) error) error {
				if err := guard(ctx); err != nil {
					return err
				}
				cleanups++
				if mode == "permission after cleanup" {
					launch.installations.destinations.authz = &authorizer{deny: true}
				}
				return nil
			}
			switch mode {
			case "read only":
				p.ReadOnly = true
			case "foreign destination":
				launch.installations.destinations.authz = launchScopedAuth{destination: "other"}
				in.DestinationID = "other"
			case "physical identity":
				d := installations.destinations.rows[in.DestinationID]
				d.Identity.NamespaceUID = "foreign"
				installations.destinations.rows[d.ID] = d
			case "new installation":
				d := installations.destinations.rows[in.DestinationID]
				d.Generation++
				installations.destinations.rows[d.ID] = d
				installations.record.Generation = d.Generation
				installations.record.Profile.PodUID = "replacement"
			}
			result, err := svc.Retire(t.Context(), p, in.DestinationID, in.AttemptID)
			if mode == "new installation" {
				if err != nil || result.State != LaunchRetired || cleanups != 1 {
					t.Fatal("new generation blocked old UID cleanup", err)
				}
				return
			}
			if err == nil || ledger.record.State == LaunchRetired {
				t.Fatal("unauthorized retirement completed", err)
			}
			if mode == "read only" || mode == "foreign destination" || mode == "physical identity" {
				if revokes != 0 || cleanups != 0 {
					t.Fatal("unauthorized side effect")
				}
			}
			if mode == "permission after revoke" && cleanups != 0 {
				t.Fatal("revoked permission reached provider cleanup")
			}
		})
	}
}

func TestLaunchDeliveryStopsWhenRetirementStarts(t *testing.T) {
	launch, _, _, p, in, _ := launchServiceFixture(t)
	ledger, ok := launch.attempts.(*launchMemory)
	if !ok {
		t.Fatal("unexpected fixture")
	}
	launch.deliver = func(ctx context.Context, _ *agentinstall.KubernetesPlan, _ domain.Destination, _ workloadidentity.EnrollmentRecord, _ agentinstall.LaunchSettings, guard func(context.Context) error) (*corev1.ConfigMap, error) {
		if _, err := ledger.BeginLaunchRetirement(ctx, p.OrgID, in.AttemptID, 1, p.UserID, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if err := guard(ctx); err != nil {
			return nil, err
		}
		t.Error("provider write guard ignored durable retirement")
		return nil, errors.New("would write retired attempt")
	}
	if _, err := launch.Deliver(t.Context(), p, in); err == nil {
		t.Fatal("retired attempt delivered")
	}
}
