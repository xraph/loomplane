package planning

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/security"
)

// defaultPlacementSelector makes a migration plan move only the services the
// Cloud's default placed. Target IDs are typeids, so it never names a target.
const defaultPlacementSelector = "@default"

// selectsPlacement reports whether a migration touches a service's pin: every
// pin for a reassignment, one target's pins, or the default's.
func selectsPlacement(selector string, pin *CloudPlacement) bool {
	switch selector {
	case "":
		return true
	case defaultPlacementSelector:
		return pin.Default
	}
	return !pin.Default && pin.TargetID == selector
}

// previewDefaultSwitch records the reviewed default change. Every environment
// using the Cloud is a participant; services there with a placement of their
// own keep it.
func (s *Service) previewDefaultSwitch(ctx context.Context, st cloudprofiles.SyncStore, p security.Principal, in cloudprofiles.ChangeInput, preview *cloudprofiles.Preview, snapshot *cloudprofiles.Snapshot, options []ConnectionOption) error {
	if in.TargetID != "" || in.EnvironmentID != "" || in.Runtime != "" && in.Runtime != "managed" || in.PoolID != "" {
		return fmt.Errorf("%w: a default placement is one managed provider and connection", cloudprofiles.ErrInvalid)
	}
	profile, err := st.GetProfile(ctx, p.OrgID, "", in.CloudID)
	if err != nil {
		return err
	}
	if profile.Revision != in.Revision {
		return cloudprofiles.ErrConflict
	}
	var after *cloudprofiles.DefaultPlacement
	if provider := strings.TrimSpace(in.Provider); provider != "" {
		after = &cloudprofiles.DefaultPlacement{Provider: provider, ConnectionID: strings.TrimSpace(in.ConnectionID), Region: strings.TrimSpace(in.Region)}
		if after.ConnectionID == "" {
			return fmt.Errorf("%w: a default placement needs a connection", cloudprofiles.ErrInvalid)
		}
	} else if in.ConnectionID != "" || in.Region != "" {
		return fmt.Errorf("%w: name the provider for this connection, or send neither to clear the default", cloudprofiles.ErrInvalid)
	}
	before := profile.DefaultPlacement
	if sameDefault(before, after) {
		return fmt.Errorf("%w: the proposed default matches the current one", cloudprofiles.ErrInvalid)
	}
	preview.BeforeDefault, preview.AfterDefault = before, after
	snapshot.ProfileRevisions[profile.ID] = profile.Revision
	if before != nil {
		snapshot.Connections = append(snapshot.Connections, before.ConnectionID)
	}
	if after != nil {
		snapshot.Connections = append(snapshot.Connections, after.ConnectionID)
		target := cloudprofiles.Target{OrgID: p.OrgID, CloudID: profile.ID, Runtime: "managed", Provider: after.Provider, ConnectionID: after.ConnectionID, Region: after.Region}
		if validateErr := s.validateSyncTarget(ctx, target, options); validateErr != nil {
			preview.Blockers = append(preview.Blockers, validateErr.Error())
		}
	}
	return nil
}

// projectGrant returns nil when the project has never been granted the Cloud.
func (s *Service) projectGrant(ctx context.Context, org, cloud, project string) (*cloudprofiles.ProjectGrant, error) {
	grants, ok := s.cloudProfiles.(cloudprofiles.GrantStore)
	if !ok {
		return nil, nil
	}
	grant, err := grants.GetProjectGrant(ctx, org, cloud, project)
	if errors.Is(err, cloudprofiles.ErrNotFound) {
		return nil, nil
	}
	return grant, err
}

// defaultAttributes is an untyped nil for an absent default in audit records.
func defaultAttributes(p *cloudprofiles.DefaultPlacement) any {
	if p == nil {
		return nil
	}
	return map[string]any{"provider": p.Provider, "connection_id": p.ConnectionID, "region": p.Region}
}

func sameDefault(a, b *cloudprofiles.DefaultPlacement) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
