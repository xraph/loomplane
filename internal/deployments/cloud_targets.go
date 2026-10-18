package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func (r *Runner) validateCloudPlacement(ctx context.Context, plan *planning.Plan, a planning.Action, conn *connections.Connection) error {
	if a.Service == nil || a.Service.CloudPlacement == nil || a.Op == planning.Delete || a.Op == planning.Keep && !a.AdoptCloudPlacement {
		return nil
	}
	pin := a.Service.CloudPlacement
	stale := func(reason string) error { return fmt.Errorf("cloud target for %s %s; plan again", a.Name, reason) }
	if r.cloudProfiles == nil {
		return stale("cannot be revalidated because the Cloud resolver is unavailable")
	}
	if pin.AccountIdentity == "" || pin.CloudRevision < 1 || pin.AssignmentRevision < 1 || pin.TargetRevision < 1 {
		return stale("has no verified identity or revision")
	}
	if pin.Provider != a.Provider || a.Service.Provider != a.Provider || pin.ConnectionID != a.ConnectionID || pin.Region != a.Region || a.Service.Region != a.Region {
		return stale("differs from the reviewed action")
	}
	assignment, err := r.cloudProfiles.Resolve(ctx, plan.OrgID, plan.ProjectID, plan.EnvironmentID)
	if err != nil {
		return fmt.Errorf("revalidate Cloud target for %s: %w", a.Name, err)
	}
	if assignment == nil || assignment.OrgID != plan.OrgID || assignment.ProjectID != plan.ProjectID || assignment.EnvironmentID != plan.EnvironmentID || assignment.CloudID != pin.CloudID || assignment.CloudRevision != pin.CloudRevision || assignment.Revision != pin.AssignmentRevision {
		return stale("assignment changed since review or is outside this scope")
	}
	i := slices.IndexFunc(assignment.Targets, func(t cloudprofiles.Target) bool { return t.ID == pin.TargetID })
	if i < 0 {
		return stale("is missing from the assigned Cloud")
	}
	target := assignment.Targets[i]
	if target.OrgID != plan.OrgID || target.ProjectID != plan.ProjectID || target.CloudID != pin.CloudID || target.Revision != pin.TargetRevision || target.Name != pin.TargetName || target.Provider != pin.Provider || target.ConnectionID != pin.ConnectionID {
		return stale("changed since review or is outside this scope")
	}
	if conn == nil {
		conn, _, err = r.credentials.Credential(ctx, plan.OrgID, pin.ConnectionID)
		if err != nil {
			return err
		}
	}
	prov, err := r.providers.Get(a.Provider)
	if err != nil {
		return err
	}
	if conn == nil || conn.ID != pin.ConnectionID || conn.Cloud != prov.Cloud || conn.Status != connections.StatusReady {
		return stale("connection is no longer ready for this provider")
	}
	if planning.OwnedCloud(conn.Cloud) && !reflect.DeepEqual(a.Service.TargetSettings, planning.ReviewedConnectionSettings(conn.Cloud, conn.Details)) {
		return stale("settings changed since review")
	}
	identity := connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)
	if identity == "" || identity != pin.AccountIdentity {
		return stale("account identity changed or is unverified; recheck or reconnect under Integrations")
	}
	return nil
}

func (r *Runner) adoptCloudPlacement(ctx context.Context, d *Deployment, plan *planning.Plan, a planning.Action, step *Step, heartbeat func([]byte) error) error {
	if err := r.validateCloudPlacement(ctx, plan, a, nil); err != nil {
		return err
	}
	bindings, err := r.plans.ListBindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if string(b.Subject)+":"+b.Name != a.ID {
			continue
		}
		if b.OrgID != d.OrgID || b.ProjectID != d.ProjectID || b.EnvironmentID != d.EnvironmentID || b.Provider != a.Provider || b.Region != a.Region || b.ConnectionID != a.ConnectionID || b.Drift != "" || !planning.SameProviderConfig(b.Config, a.Service) {
			return fmt.Errorf("serving binding for %s changed since this metadata adoption was reviewed; plan again", a.Name)
		}
		if !planning.MatchesCloudAdoption(b, a.CloudAdoption) {
			var applied planning.ServiceConfig
			ref := a.CloudAdoption
			if ref == nil || ref.BindingID != b.ID || ref.ExternalID != b.ExternalID || ref.Revision != b.Revision || ref.PlanID != b.PlanID || json.Unmarshal(b.Config, &applied) != nil || applied.CloudPlacement == nil || *applied.CloudPlacement != *a.Service.CloudPlacement {
				return fmt.Errorf("serving binding for %s changed since this metadata adoption was reviewed; plan again", a.Name)
			}
			step.URL = b.URL
			return heartbeat([]byte("cloud-placement-adopted"))
		}

		store, ok := r.plans.(planning.CloudAdoptionStore)
		if !ok {
			return fmt.Errorf("cloud placement adoption requires atomic binding storage")
		}
		updated, adoptionErr := store.AdoptCloudPlacement(ctx, b, a.Service.CloudPlacement, r.now().UTC())
		if adoptionErr != nil {
			return adoptionErr
		}
		if !updated {
			return fmt.Errorf("serving binding for %s changed during Cloud placement adoption; plan again", a.Name)
		}

		step.URL = b.URL
		return heartbeat([]byte("cloud-placement-adopted"))
	}
	return fmt.Errorf("serving binding for %s is missing; plan again", a.Name)
}
