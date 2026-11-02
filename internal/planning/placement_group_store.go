package planning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/projects"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// PlacementGroupIdentity reserves a stable header. It authorizes no effects.
func (s *PgStore) PlacementGroupIdentity(ctx context.Context, p *Plan, name string) (groupID string, nextGeneration int64, saveErr error) {
	var id string
	var generation int64
	err := s.groupTx(ctx, p.OrgID, func(tx driver.Tx) error {
		if err := guardGroupEnvironment(ctx, tx, p.OrgID, p.ProjectID, p.EnvironmentID); err != nil {
			return err
		}
		candidate := typeid.MustGenerate("pgroup").String()
		_, e := tx.Exec(ctx, `INSERT INTO loomplane_placement_groups(org_id,id,project_id,environment_id,name) VALUES($1,$2,$3,$4,$5) ON CONFLICT(org_id,project_id,environment_id,name) DO NOTHING`, p.OrgID, candidate, p.ProjectID, p.EnvironmentID, name)
		if e != nil {
			return e
		}
		e = tx.QueryRow(ctx, `SELECT id,generation+1 FROM loomplane_placement_groups WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND name=$4`, p.OrgID, p.ProjectID, p.EnvironmentID, name).Scan(&id, &generation)
		if e != nil {
			return e
		}
		var existing int64
		e = tx.QueryRow(ctx, `SELECT generation FROM loomplane_placement_group_revisions WHERE org_id=$1 AND group_id=$2 AND plan_id=$3`, p.OrgID, id, p.ID).Scan(&existing)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		generation = existing
		return nil
	})
	return id, generation, err
}

func (s *PgStore) SavePlacementGroupReview(ctx context.Context, p *Plan, group domain.QualifiedGroupPlan) error {
	if group.Scope != (domain.Scope{OrgID: p.OrgID, ProjectID: p.ProjectID, EnvironmentID: p.EnvironmentID}) || group.SchemaVersion != 1 || len(group.Members) < 2 || group.Revision == "" {
		return domain.ErrGroupProfile
	}
	expected := group.Revision
	group.Revision = ""
	computed, err := Digest(group)
	group.Revision = expected
	if err != nil {
		return err
	}
	if computed != expected {
		return ErrConflict
	}
	return s.groupTx(ctx, p.OrgID, func(tx driver.Tx) error {
		raw, e := json.Marshal(group)
		if e != nil {
			return e
		}
		var existing []byte
		e = tx.QueryRow(ctx, `SELECT body FROM loomplane_placement_group_revisions WHERE org_id=$1 AND group_id=$2 AND generation=$3 AND plan_id=$4`, p.OrgID, group.GroupID, group.Generation, p.ID).Scan(&existing)
		if e == nil {
			var saved domain.QualifiedGroupPlan
			if json.Unmarshal(existing, &saved) != nil || saved.Revision != group.Revision {
				return ErrConflict
			}
			return ValidatePlacementGroupReviewTx(ctx, tx, p.OrgID, p.ProjectID, p.EnvironmentID, group, s.nowForGroups())
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var current int64
		if generationErr := tx.QueryRow(ctx, `SELECT generation FROM loomplane_placement_groups WHERE org_id=$1 AND id=$2 AND project_id=$3 AND environment_id=$4 AND name=$5 FOR UPDATE`, p.OrgID, group.GroupID, p.ProjectID, p.EnvironmentID, group.Name).Scan(&current); generationErr != nil {
			return generationErr
		}
		if current+1 != group.Generation {
			return ErrConflict
		}
		if validationErr := ValidatePlacementGroupReviewTx(ctx, tx, p.OrgID, p.ProjectID, p.EnvironmentID, group, s.nowForGroups()); validationErr != nil {
			return validationErr
		}
		_, e = tx.Exec(ctx, `INSERT INTO loomplane_placement_group_revisions(org_id,group_id,generation,plan_id,revision,body) VALUES($1,$2,$3,$4,$5,$6)`, p.OrgID, group.GroupID, group.Generation, p.ID, group.Revision, raw)
		if e != nil {
			return e
		}
		for _, member := range group.Members {
			body, marshalErr := json.Marshal(member)
			if marshalErr != nil {
				return marshalErr
			}
			i := member.Identity
			_, e = tx.Exec(ctx, `INSERT INTO loomplane_placement_group_members(org_id,group_id,generation,member_id,destination_id,target_id,attachment_id,prerequisite_id,physical_identity,binding_id,instance_id,runtime_ref,release_id,revision,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, p.OrgID, group.GroupID, group.Generation, i.MemberID, member.DestinationID, member.TargetID, member.Prerequisite.AttachmentID, member.Prerequisite.ID, member.PhysicalIdentity.Account+"/"+member.PhysicalIdentity.NamespaceUID, i.BindingID, i.InstanceID, i.RuntimeRef, i.ReleaseID, group.Revision, body)
			if e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `UPDATE loomplane_placement_groups SET generation=$3,revision=revision+1,updated_at=now() WHERE org_id=$1 AND id=$2`, p.OrgID, group.GroupID, group.Generation)
		return e
	})
}
func (*PgStore) nowForGroups() time.Time { return time.Now().UTC() }

// ValidatePlacementGroupReviewTx is the shared current-authority check for the
// future ordinary approval transaction. The caller holds the organization lock.
// It must also reobserve TLS through MemberPrerequisiteService before acceptance.
func ValidatePlacementGroupReviewTx(ctx context.Context, tx driver.Tx, org, project, environment string, group domain.QualifiedGroupPlan, now time.Time) error {
	if group.Scope != (domain.Scope{OrgID: org, ProjectID: project, EnvironmentID: environment}) || now.IsZero() || group.SchemaVersion != 1 || group.GroupID == "" || group.Generation < 1 || len(group.Members) < 2 || len(group.Members) > 20 || group.MinReady != len(group.Members) || group.ConfigRevision == "" || group.Revision == "" {
		return ErrConflict
	}
	expected := group.Revision
	group.Revision = ""
	computed, digestErr := Digest(group)
	group.Revision = expected
	if digestErr != nil || computed != expected {
		return ErrConflict
	}
	if err := guardGroupEnvironment(ctx, tx, org, project, environment); err != nil {
		return err
	}
	seenMembers, seenDestinations := map[string]bool{}, map[string]bool{}
	for _, member := range group.Members {
		if member.Replicas < 1 || seenMembers[member.Identity.MemberID] || seenDestinations[member.DestinationID] {
			return ErrConflict
		}
		seenMembers[member.Identity.MemberID], seenDestinations[member.DestinationID] = true, true
		identity, identityErr := domain.NewMemberIdentity(group.GroupID, group.Generation, member.Identity.MemberID)
		if identityErr != nil || identity != member.Identity {
			return ErrConflict
		}
		raw, err := json.Marshal(member)
		if err != nil {
			return err
		}
		var current bool
		if currentErr := tx.QueryRow(ctx, `SELECT loomplane_group_member_current($1,$2,$3,$4::jsonb,$5,$6)`, org, project, environment, raw, group.ConfigRevision, now).Scan(&current); currentErr != nil {
			return currentErr
		}
		if !current {
			return ErrConflict
		}
	}
	return nil
}
func (s *PgStore) ValidatePlacementGroupReview(ctx context.Context, group domain.QualifiedGroupPlan, now time.Time) error {
	return s.groupTx(ctx, group.OrgID, func(tx driver.Tx) error {
		return ValidatePlacementGroupReviewTx(ctx, tx, group.OrgID, group.ProjectID, group.EnvironmentID, group, now)
	})
}
func (s *PgStore) GetPlacementGroupReview(ctx context.Context, scope domain.Scope, group string, generation int64) (*domain.QualifiedGroupPlan, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT r.body FROM loomplane_placement_group_revisions r JOIN loomplane_placement_groups g ON g.org_id=r.org_id AND g.id=r.group_id WHERE r.org_id=$1 AND g.project_id=$2 AND g.environment_id=$3 AND r.group_id=$4 AND r.generation=$5`, scope.OrgID, scope.ProjectID, scope.EnvironmentID, group, generation).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var out domain.QualifiedGroupPlan
	if json.Unmarshal(raw, &out) != nil || out.SchemaVersion != 1 || out.Scope != scope || out.GroupID != group || out.Generation != generation {
		return nil, ErrConflict
	}
	return &out, nil
}

func (s *MemberPrerequisiteService) groupMemberFacts(ctx context.Context, scope domain.Scope, destination, prerequisite string) (GroupMemberFacts, error) {
	var out GroupMemberFacts
	var target, attachment string
	var mappingRevision int64
	err := s.Store.db.QueryRow(ctx, `SELECT target_id,attachment_id,revision FROM loomplane_group_destination_mappings WHERE org_id=$1 AND project_id=$2 AND environment_id=$3 AND destination_id=$4`, scope.OrgID, scope.ProjectID, scope.EnvironmentID, destination).Scan(&target, &attachment, &mappingRevision)
	if err != nil {
		return out, groupStoreError(err)
	}
	facts, err := s.Store.GroupAttachmentScope(ctx, scope.OrgID, scope.ProjectID, attachment, target)
	if err != nil {
		return out, err
	}
	p, err := s.Store.GetMemberPrerequisite(ctx, scope.OrgID, scope.ProjectID, prerequisite)
	if err != nil {
		return out, err
	}
	if p.AttachmentID != attachment {
		return out, ErrConflict
	}
	d, err := s.Store.GetFederationDestination(ctx, scope.OrgID, destination, s.now())
	if err != nil {
		return out, err
	}
	t, err := cloudprofiles.NewPgStore(s.Store.db).GetTarget(ctx, scope.OrgID, scope.ProjectID, facts.Attachment.CloudID, target)
	if err != nil {
		return out, err
	}
	location, err := cloudprofiles.NewPgStore(s.Store.db).PlacementLocation(ctx, scope.OrgID, scope.ProjectID, t.CloudID, t.LocationID)
	if err != nil {
		return out, err
	}
	if location.Region != t.Region || location.CloudID != t.CloudID || location.EvidenceRevision != location.Revision || location.CheckedAt.IsZero() || location.CheckedAt.After(s.now()) || location.EvidenceSource == "" || location.EvidenceRef == "" || location.VerifiedBy == "" {
		return out, ErrConflict
	}
	c, err := connections.NewPgStore(s.Store.db).GetConnection(ctx, scope.OrgID, d.ConnectionID)
	if err != nil {
		return out, err
	}
	a, err := s.Store.groupAuthority(ctx, scope.OrgID, scope.ProjectID, attachment, p.RenewalAuthorityID)
	if err != nil {
		return out, err
	}
	if s.Observer == nil {
		return out, ErrPrerequisiteObservation
	}
	observation, err := s.Observer.Observe(ctx, facts.Attachment, p.TLSSecretName)
	if err != nil {
		return out, err
	}
	if observationErr := s.Store.recordGroupObservation(ctx, facts, p, observation); observationErr != nil {
		return out, observationErr
	}
	config := ""
	if p.DrainPolicy.Mode == "bounded" {
		_, config, err = s.Store.groupTimeout(ctx, scope.OrgID, scope.ProjectID, attachment, p.NamespaceUID, p.DrainPolicy, p.TimeoutVerificationID)
		if err != nil {
			return out, err
		}
	}
	locationHash := sha256.Sum256([]byte(location.Provider + "/" + location.AccountID + "/" + location.Region))
	out = GroupMemberFacts{Destination: d, Prerequisite: *p, Authority: *a, Observation: observation, TimeoutConfigRevision: config, Plan: domain.GroupMemberPlan{LocationIdentity: hex.EncodeToString(locationHash[:]), MappingRevision: mappingRevision, CloudID: t.CloudID, TargetID: t.ID, TargetRevision: t.Revision, AttachmentRevision: facts.Attachment.Revision, ConnectionID: c.ID, ConnectionGeneration: c.Generation, LocationID: location.ID, LocationRevision: location.Revision, LocationEvidenceRevision: location.EvidenceRevision, GrantRevision: facts.GrantRevision}}
	return out, nil
}

// guardGroupEnvironment serializes ordinary review writes with authoritative
// environment ownership. Project deployment mode alone does not own an environment.
func guardGroupEnvironment(ctx context.Context, tx driver.Tx, org, project, environment string) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM loomplane_environments WHERE org_id=$1 AND project_id=$2 AND id=$3 FOR UPDATE`, org, project, environment).Scan(&id); err != nil {
		return groupStoreError(err)
	}
	return projects.GuardUnmanagedEnvironment(ctx, tx, org, environment)
}
func (s *PgStore) guardGroupEnvironment(ctx context.Context, scope domain.Scope) error {
	return s.groupTx(ctx, scope.OrgID, func(tx driver.Tx) error {
		return guardGroupEnvironment(ctx, tx, scope.OrgID, scope.ProjectID, scope.EnvironmentID)
	})
}
