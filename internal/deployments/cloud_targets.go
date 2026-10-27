package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/planning"
)

func (r *Runner) validateCloudPlacement(ctx context.Context, plan *planning.Plan, a planning.Action, conn *connections.Connection) error {
	return r.validateCloudPlacementAuthority(ctx, plan, a, conn, false)
}

func (r *Runner) validateCloudPlacementAuthority(ctx context.Context, plan *planning.Plan, a planning.Action, conn *connections.Connection, retained bool) error {
	if a.Provider == "server" && a.Op != planning.Delete && a.Op != planning.Keep && (a.Service == nil || a.Service.CloudPlacement == nil || a.Service.CloudPlacement.PoolID == "") {
		if reader, ok := r.plans.(interface {
			PooledConnections(context.Context, string) (map[string]bool, error)
		}); ok {
			hosts, err := reader.PooledConnections(ctx, plan.OrgID)
			if err != nil {
				return err
			}
			if hosts[a.ConnectionID] || conn != nil && hosts[conn.ID] {
				return fmt.Errorf("shared host requires a reviewed pool reservation")
			}
		}
	}
	pin, reviewed := reviewedPlacement(a)
	if pin == nil || a.Op == planning.Delete || a.Op == planning.Keep && !a.AdoptCloudPlacement {
		return nil
	}
	stale := func(reason string) error { return fmt.Errorf("cloud target for %s %s; plan again", a.Name, reason) }
	if r.cloudProfiles == nil {
		return stale("cannot be revalidated because the Cloud resolver is unavailable")
	}
	if pin.AccountIdentity == "" || pin.CloudRevision < 1 || pin.AssignmentRevision < 1 || !pin.Default && pin.TargetRevision < 1 {
		return stale("has no verified identity or revision")
	}
	if pin.Provider != a.Provider || reviewed.provider != a.Provider || pin.ConnectionID != a.ConnectionID || pin.Region != a.Region || reviewed.region != a.Region {
		return stale("differs from the reviewed action")
	}
	var assignment *cloudprofiles.Assignment
	var err error
	if retained {
		reader, ok := r.cloudProfiles.(interface {
			RetainedAssignment(context.Context, string, string, string) (*cloudprofiles.Assignment, error)
		})
		if !ok {
			return stale("has no retained cleanup resolver")
		}
		assignment, err = reader.RetainedAssignment(ctx, plan.OrgID, plan.ProjectID, plan.EnvironmentID)
	} else {
		assignment, err = r.cloudProfiles.Resolve(ctx, plan.OrgID, plan.ProjectID, plan.EnvironmentID)
	}
	if err != nil {
		return fmt.Errorf("revalidate Cloud target for %s: %w", a.Name, err)
	}
	if assignment == nil || assignment.OrgID != plan.OrgID || assignment.ProjectID != plan.ProjectID || assignment.EnvironmentID != plan.EnvironmentID || assignment.CloudID != pin.CloudID || assignment.CloudRevision != pin.CloudRevision || assignment.Revision != pin.AssignmentRevision || !retained && (pin.GrantRevision > 0 && assignment.GrantRevision != pin.GrantRevision || pin.GrantRevision == 0 && assignment.GrantRevision > 1) {
		return stale("assignment changed since review or is outside this scope")
	}
	if pin.Default {
		// The Cloud revision already pins its default; check it is still reachable.
		d := assignment.DefaultPlacement
		if d == nil || pin.TargetID != "" || pin.PoolID != "" || d.Provider != pin.Provider || d.ConnectionID != pin.ConnectionID {
			return stale("default placement changed since review or is outside this grant")
		}
	} else {
		i := slices.IndexFunc(assignment.Targets, func(t cloudprofiles.Target) bool { return t.ID == pin.TargetID })
		if i < 0 {
			return stale("is missing from the assigned Cloud")
		}
		target := assignment.Targets[i]
		if target.OrgID != plan.OrgID || target.CloudID != pin.CloudID || target.Revision != pin.TargetRevision || target.Name != pin.TargetName || target.Provider != pin.Provider || (pin.PoolID == "" && target.ConnectionID != pin.ConnectionID) || target.PoolID != pin.PoolID {
			return stale("changed since review or is outside this scope")
		}
	}
	if pin.PoolID != "" && (a.Compensation == nil || !a.Compensation.Retained) {
		validator, ok := r.plans.(interface {
			ValidatePlacement(context.Context, string, string, string, string, string, cloudinfra.Placement) error
		})
		if !ok {
			return stale("capacity reservation cannot be verified")
		}
		if err = validator.ValidatePlacement(ctx, plan.OrgID, plan.ProjectID, plan.EnvironmentID, plan.ID, a.Name, pin.PoolPlacement()); err != nil {
			return fmt.Errorf("validate reserved capacity: %w", err)
		}
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
	if pin.PoolID != "" && conn.Generation != pin.ConnectionGeneration {
		return stale("runtime generation changed")
	}
	if planning.OwnedCloud(conn.Cloud) && !reflect.DeepEqual(reviewed.settings, planning.ReviewedConnectionSettings(conn.Cloud, conn.Details)) {
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
	pin, _ := reviewedPlacement(a)
	if pin == nil {
		return fmt.Errorf("cloud placement adoption for %s has no reviewed placement; plan again", a.Name)
	}
	bindings, err := r.plans.ListBindings(ctx, d.OrgID, d.EnvironmentID)
	if err != nil {
		return err
	}
	for _, b := range bindings {
		if string(b.Subject)+":"+b.Name != a.ID {
			continue
		}
		if b.OrgID != d.OrgID || b.ProjectID != d.ProjectID || b.EnvironmentID != d.EnvironmentID || b.Provider != a.Provider || b.Region != a.Region || b.ConnectionID != a.ConnectionID || b.Drift != "" || !sameAppliedConfig(b.Config, a) {
			return fmt.Errorf("serving binding for %s changed since this metadata adoption was reviewed; plan again", a.Name)
		}
		if !planning.MatchesCloudAdoption(b, a.CloudAdoption) {
			ref := a.CloudAdoption
			applied := appliedPlacement(b.Config, a)
			if ref == nil || ref.BindingID != b.ID || ref.ExternalID != b.ExternalID || ref.Revision != b.Revision || ref.PlanID != b.PlanID || applied == nil || *applied != *pin {
				return fmt.Errorf("serving binding for %s changed since this metadata adoption was reviewed; plan again", a.Name)
			}
			step.URL = b.URL
			return heartbeat([]byte("cloud-placement-adopted"))
		}

		store, ok := r.plans.(planning.CloudAdoptionStore)
		if !ok {
			return fmt.Errorf("cloud placement adoption requires atomic binding storage")
		}
		updated, adoptionErr := store.AdoptCloudPlacement(ctx, b, pin, r.now().UTC())
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

// reviewedConfig is what an action's Cloud placement pins it to.
type reviewedConfig struct {
	provider, region string
	settings         map[string]string
}

// reviewedPlacement is an action's Cloud placement: a service's, or a
// composed resource's or component's. Nothing else has one.
func reviewedPlacement(a planning.Action) (*planning.CloudPlacement, reviewedConfig) {
	switch {
	case a.Service != nil:
		return a.Service.CloudPlacement, reviewedConfig{a.Service.Provider, a.Service.Region, a.Service.TargetSettings}
	case a.Resource != nil:
		return a.Resource.CloudPlacement, reviewedConfig{a.Resource.Provider, a.Resource.Region, a.Resource.TargetSettings}
	}
	return nil, reviewedConfig{}
}

// sameAppliedConfig reports whether a binding still runs the action's
// reviewed configuration, apart from its placement.
func sameAppliedConfig(applied []byte, a planning.Action) bool {
	switch {
	case a.Service != nil:
		return planning.SameProviderConfig(applied, a.Service)
	case a.Resource != nil:
		return planning.SameResourceConfig(applied, a.Resource)
	}
	return false
}

// appliedPlacement is the placement a binding already records.
func appliedPlacement(applied []byte, a planning.Action) *planning.CloudPlacement {
	if a.Service != nil {
		var cfg planning.ServiceConfig
		if json.Unmarshal(applied, &cfg) != nil {
			return nil
		}
		return cfg.CloudPlacement
	}
	var cfg planning.ResourceConfig
	if json.Unmarshal(applied, &cfg) != nil {
		return nil
	}
	return cfg.CloudPlacement
}

func poolBinding(b *planning.Binding) bool {
	if b == nil {
		return false
	}
	var cfg planning.ServiceConfig
	return json.Unmarshal(b.Config, &cfg) == nil && cfg.CloudPlacement != nil && cfg.CloudPlacement.PoolID != ""
}
