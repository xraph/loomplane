package cloudinfra

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// platformClouds resolves every Cloud as the platform-owned Loomplane Cloud.
type platformClouds struct{}

func (platformClouds) GetProfile(_ context.Context, org, _, id string) (*cloudprofiles.Profile, error) {
	return &cloudprofiles.Profile{ID: id, OrgID: org, Name: "Loomplane Cloud", PlatformOwned: true, Revision: 1}, nil
}

// memberAuth allows everything and confirms current membership.
type memberAuth struct{ cleanupTestAuth }

func (memberAuth) RequireCurrentMembership(context.Context, security.Principal) error { return nil }

func TestPlatformOwnedCloudTakesNoInfrastructure(t *testing.T) {
	svc := NewService(Deps{Store: &capacityRuntimeStore{}, Clouds: platformClouds{}, Authz: memberAuth{}})
	p := security.Principal{OrgID: "org", UserID: "manager"}
	ctx := t.Context()
	checks := map[string]error{}
	_, checks["server"] = svc.PreviewServer(ctx, p, "cloud", ServerInput{})
	_, checks["apply server"] = svc.ApplyServerOperation(ctx, p, "cloud", "op", ApplyInput{})
	_, checks["pool"] = svc.SavePool(ctx, p, "cloud", "", PoolInput{Name: "pool"})
	_, checks["scaling pool"] = svc.CreateScalingPool(ctx, p, "cloud", ScalingPoolInput{})
	_, checks["capacity policy"] = svc.SaveCapacityPolicy(ctx, p, "cloud", CapacityPolicyInput{})
	_, _, checks["cluster"] = svc.clusterAuthority(ctx, p, "cloud", ClusterInput{CloudRevision: 1})
	_, checks["preview drain"] = svc.PreviewDrain(ctx, p, "cloud", "server")
	_, checks["apply drain"] = svc.ApplyDrain(ctx, p, "cloud", "server", ApplyInput{})
	_, checks["preview move"] = svc.PreviewMove(ctx, p, "cloud", MoveInput{})
	_, checks["accept move"] = svc.AcceptMove(ctx, p, "cloud", MoveInput{})
	_, checks["preview recovery"] = svc.PreviewReservationRecovery(ctx, p, "cloud", RecoveryInput{})
	_, checks["recover reservation"] = svc.RecoverReservation(ctx, p, "cloud", RecoveryInput{})
	_, checks["refresh capacity"] = svc.RefreshCapacity(ctx, p, "cloud")
	_, checks["approve expansion"] = svc.ApproveExpansion(ctx, p, "cloud", "expansion", 1)
	for name, err := range checks {
		if !errors.Is(err, cloudprofiles.ErrPlatformOwned) {
			t.Errorf("%s: err = %v, want ErrPlatformOwned", name, err)
		}
	}
	// Reads stay open.
	if _, err := svc.GetCapacityPolicy(ctx, p, "cloud"); errors.Is(err, cloudprofiles.ErrPlatformOwned) {
		t.Fatalf("read refused: %v", err)
	}
}
