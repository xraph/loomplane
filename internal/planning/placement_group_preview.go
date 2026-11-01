package planning

import (
	"context"
	"errors"
	"slices"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
	"github.com/xraph/loomplane/pkg/manifest"
)

const FeatureMultiLocationDelivery = "multi_location_delivery"

var ErrGroupVariableSnapshotsUnavailable = errors.New("placement group immutable variable snapshots are unavailable")

// GroupConfigurationRevision pins the selected environment's effective settings.
// Runtime variables require a separate immutable version snapshot contract.
func GroupConfigurationRevision(m *manifest.Manifest, environment, service string) (string, error) {
	svc, ok := m.Services[service]
	if !ok {
		return "", domain.ErrGroupProfile
	}
	return Digest(struct {
		Kind     string
		Settings manifest.Settings
		Needs    []string
	}{svc.Kind, svc.For(environment, m.Defaults), svc.Needs})
}

// PreviewPlacementGroups records review identities only. All execution remains
// unavailable until the ordinary approval and member runtime fences are wired.
func (s *MemberPrerequisiteService) PreviewPlacementGroups(ctx context.Context, p *Plan, m *manifest.Manifest, g *graph.Graph, bindings []*Binding) ([]domain.QualifiedGroupPlan, error) {
	if s.Store == nil || s.Authz == nil || m == nil || m.Federation == nil {
		return nil, domain.ErrGroupProfile
	}
	if err := s.Authz.Require(ctx, security.Principal{OrgID: p.OrgID, UserID: p.CreatedBy}, security.ProjectRead, p.ProjectID); err != nil {
		return nil, err
	}
	scope := domain.Scope{OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID}
	if err := s.Store.guardGroupEnvironment(ctx, scope); err != nil {
		return nil, err
	}
	out := make([]domain.QualifiedGroupPlan, 0, len(m.Federation.PlacementGroups))
	for _, name := range federationKeys(m.Federation.PlacementGroups) {
		group := m.Federation.PlacementGroups[name]
		svc := m.Services[group.Service]
		var variables bool
		err := s.Store.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_variables WHERE org_id=$1 AND (project_id IS NULL OR project_id=$2) AND (environment='' OR environment=$3))`, p.OrgID, p.ProjectID, p.Environment).Scan(&variables)
		if err != nil {
			return nil, err
		}
		if variables {
			return nil, ErrGroupVariableSnapshotsUnavailable
		}
		config, err := GroupConfigurationRevision(m, p.Environment, group.Service)
		if err != nil {
			return nil, err
		}
		id, generation, err := s.Store.PlacementGroupIdentity(ctx, p, name)
		if err != nil {
			return nil, err
		}
		in := GroupProfileInput{Scope: scope, Enabled: true, Manifest: m, Environment: p.Environment, Name: name, GroupID: id, Generation: generation, ConfigRevision: config, Now: s.now(), Members: map[string]GroupMemberFacts{}, UnverifiedDependencies: len(svc.Needs) > 0}
		// A legacy binding must be retired through its existing reviewed workflow.
		// Group identities never adopt it by service name.
		for _, b := range bindings {
			if b.Subject == ServiceSubject && b.Name == group.Service {
				return nil, domain.ErrGroupProfile
			}
		}
		if g != nil {
			for _, edge := range g.Edges {
				if edge.Kind == graph.Needs {
					in.UnverifiedDependencies = true
				}
			}
		}
		for _, memberName := range federationKeys(group.Members) {
			member := group.Members[memberName]
			facts, loadErr := s.groupMemberFacts(ctx, scope, m.Federation.Destinations[member.Destination].For(p.Environment), member.PrerequisiteID)
			if loadErr != nil {
				return nil, loadErr
			}
			in.Members[memberName] = facts
		}
		in.Now = s.now()
		qualified, err := ValidatePlacementGroupProfile(in)
		if err != nil {
			return nil, err
		}
		if p.Status == StatusPlanning {
			if saveErr := s.Store.SavePlacementGroupReview(ctx, p, qualified); saveErr != nil {
				return nil, saveErr
			}
		}
		out = append(out, qualified)
	}
	return out, nil
}

// RevalidatePlacementGroupReview refreshes external identities before the later
// acceptance transaction. It does not approve or change a plan's state.
func (s *MemberPrerequisiteService) RevalidatePlacementGroupReview(ctx context.Context, review domain.QualifiedGroupPlan) error {
	if err := s.Store.guardGroupEnvironment(ctx, review.Scope); err != nil {
		return err
	}
	for _, member := range review.Members {
		facts, err := s.groupMemberFacts(ctx, review.Scope, member.DestinationID, member.Prerequisite.ID)
		if err != nil {
			return err
		}
		if !sameGroupPrerequisite(facts.Prerequisite.Pin(), member.Prerequisite) || facts.Authority.Generation != member.Prerequisite.RenewalAuthorityGeneration || !facts.Authority.Active {
			return ErrConflict
		}
		observation := facts.Observation
		p := member.Prerequisite
		if observation.SecretUID != p.TLSSecretUID || observation.NamespaceUID != p.NamespaceUID {
			return ErrConflict
		}
		if !slices.Equal(observation.Hostnames, p.Hostnames) || observation.VerifyHostname(review.Routing.Hostname, s.now()) != nil {
			return ErrConflict
		}
	}
	return s.Store.ValidatePlacementGroupReview(ctx, review, s.now())
}

func (s *Service) BindPlacementGroupPrerequisites(prerequisites *MemberPrerequisiteService) {
	s.memberPrerequisites = prerequisites
}
