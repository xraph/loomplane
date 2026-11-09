package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrGroupSnapshot = errors.New("accepted placement group configuration is unavailable")

// GroupSnapshotState is private execution state. Candidate keys, source metadata
// and the saved actor never enter a public plan, member receipt or event.
type GroupSnapshotState struct {
	ID         string
	Group      domain.QualifiedGroupPlan
	Selection  environments.RuntimeSelection
	Actor      security.Principal
	Epoch      int64
	Candidates []GroupSnapshotCandidate
	Winner     *GroupSnapshotCandidate
}

type GroupSnapshotCandidate struct {
	ID       string
	Epoch    int64
	Key      string
	Identity string
	Version  int64
}

// GroupSnapshotRepository uses short transactions only. Begin advances the
// publication epoch when writing an unpublished claim. Issue commits one fresh
// candidate before returning; its successful return authorizes exactly one write.
// A caller receiving an uncertain response must never write that candidate.
// Authorize and Publish revalidate saved authority and the current epoch.
type GroupSnapshotRepository interface {
	BeginSnapshot(context.Context, domain.QualifiedGroupPlan, bool) (GroupSnapshotState, error)
	AuthorizeSnapshot(context.Context, GroupSnapshotState) error
	IssueSnapshot(context.Context, GroupSnapshotState) (GroupSnapshotCandidate, error)
	PublishSnapshot(context.Context, GroupSnapshotState, GroupSnapshotCandidate) error
}

type groupSnapshotSecrets interface {
	VersionedResourceSecrets
	SnapshotMetadata(context.Context, string) (string, int64, error)
}
type groupSnapshotVariables interface {
	CaptureRuntimeSelection(context.Context, security.Principal, environments.RuntimeSelection) (map[string]string, map[string]bool, error)
}
type groupSnapshotPayload struct {
	SchemaVersion int `json:"schema_version"`
	domain.Scope
	GroupID           string            `json:"group_id"`
	Generation        int64             `json:"generation"`
	ClaimID           string            `json:"claim_id"`
	CandidateID       string            `json:"candidate_id"`
	IssuedEpoch       int64             `json:"issued_epoch"`
	ConfigRevision    string            `json:"config_revision"`
	SelectionRevision string            `json:"selection_revision"`
	Values            map[string]string `json:"values"`
	Secrets           map[string]bool   `json:"secrets"`
}

// GroupConfigurationSnapshots copies exact reviewed sources into an independent
// encrypted snapshot. No SQL connection is held across source or Vault calls.
type GroupConfigurationSnapshots struct {
	Store     GroupSnapshotRepository
	Secrets   groupSnapshotSecrets
	Variables groupSnapshotVariables
}

func validGroupSnapshotState(s GroupSnapshotState, g domain.QualifiedGroupPlan) bool {
	return domain.ValidIdentitySegment(s.ID) && domain.ValidIdentitySegment(g.OrgID) && domain.ValidIdentitySegment(g.ProjectID) && domain.ValidIdentitySegment(g.EnvironmentID) && domain.ValidIdentitySegment(g.GroupID) && g.Generation > 0 && s.Epoch > 0 && s.Group.Scope == g.Scope && s.Group.GroupID == g.GroupID && s.Group.Generation == g.Generation && s.Group.Revision == g.Revision && s.Group.ConfigRevision == g.ConfigRevision && s.Selection.OrgID == g.OrgID && s.Selection.ProjectID == g.ProjectID && s.Selection.EnvironmentID == g.EnvironmentID && s.Selection.Revision != "" && s.Actor.OrgID == g.OrgID && s.Actor.UserID != "" && !s.Actor.ReadOnly && !s.Actor.IsFleet()
}
func validGroupSnapshotCandidate(s GroupSnapshotState, c GroupSnapshotCandidate) bool {
	return domain.ValidIdentitySegment(c.ID) && c.Epoch > 0 && c.Epoch <= s.Epoch && c.Key == GroupSnapshotKey(s.Group, s.ID, c.ID)
}
func snapshotPayloadMatches(p groupSnapshotPayload, s GroupSnapshotState, c GroupSnapshotCandidate) bool {
	return p.SchemaVersion == 1 && p.Scope == s.Group.Scope && p.GroupID == s.Group.GroupID && p.Generation == s.Group.Generation && p.ClaimID == s.ID && p.CandidateID == c.ID && p.IssuedEpoch == c.Epoch && p.ConfigRevision == s.Group.ConfigRevision && p.SelectionRevision == s.Selection.Revision && p.Values != nil && p.Secrets != nil
}
func (g *GroupConfigurationSnapshots) readCandidate(ctx context.Context, s GroupSnapshotState, c GroupSnapshotCandidate) (groupSnapshotPayload, GroupSnapshotCandidate, error) {
	var payload groupSnapshotPayload
	if !validGroupSnapshotCandidate(s, c) {
		return payload, c, ErrGroupSnapshot
	}
	if err := g.Store.AuthorizeSnapshot(ctx, s); err != nil {
		return payload, c, err
	}
	identity, version, err := g.Secrets.SnapshotMetadata(ctx, c.Key)
	if err != nil {
		return payload, c, err
	}
	if identity == "" || version != 1 || c.Identity != "" && (c.Identity != identity || c.Version != version) {
		return payload, c, ErrGroupSnapshot
	}
	raw, err := g.Secrets.GetVersion(ctx, c.Key, version)
	if err != nil {
		return payload, c, err
	}
	defer clear(raw)
	if json.Unmarshal(raw, &payload) != nil || !snapshotPayloadMatches(payload, s, c) {
		return groupSnapshotPayload{}, c, ErrGroupSnapshot
	}
	after, afterVersion, err := g.Secrets.SnapshotMetadata(ctx, c.Key)
	if err != nil || after != identity || afterVersion != version {
		return groupSnapshotPayload{}, c, ErrGroupSnapshot
	}
	if authorityErr := g.Store.AuthorizeSnapshot(ctx, s); authorityErr != nil {
		return groupSnapshotPayload{}, c, authorityErr
	}
	c.Identity, c.Version = identity, version
	return payload, c, nil
}
func (g *GroupConfigurationSnapshots) Publish(ctx context.Context, group domain.QualifiedGroupPlan) (string, error) {
	if g.Store == nil || g.Secrets == nil || g.Variables == nil {
		return "", ErrGroupSnapshot
	}
	state, err := g.Store.BeginSnapshot(ctx, group, true)
	if err != nil {
		return "", err
	}
	if !validGroupSnapshotState(state, group) {
		return "", ErrGroupSnapshot
	}
	if state.Winner != nil {
		if state.Winner.Identity == "" || state.Winner.Version != 1 {
			return "", ErrGroupSnapshot
		}
		if _, _, err = g.readCandidate(ctx, state, *state.Winner); err != nil {
			return "", ErrGroupSnapshot
		}
		return state.ID, nil
	}
	// Old attempts contribute verified data only. The current epoch owns the CAS.
	for _, candidate := range state.Candidates {
		_, observed, readErr := g.readCandidate(ctx, state, candidate)
		if errors.Is(readErr, ErrNoCredential) {
			continue
		}
		if readErr != nil {
			return "", readErr
		}
		if publishErr := g.Store.PublishSnapshot(ctx, state, observed); publishErr != nil {
			return "", publishErr
		}
		return state.ID, nil
	}
	candidate, err := g.Store.IssueSnapshot(ctx, state)
	if err != nil {
		return "", err
	}
	if !validGroupSnapshotCandidate(state, candidate) || candidate.Epoch != state.Epoch || candidate.Identity != "" || candidate.Version != 0 {
		return "", ErrGroupSnapshot
	}
	if authorityErr := g.Store.AuthorizeSnapshot(ctx, state); authorityErr != nil {
		return "", authorityErr
	}
	// A newly issued key must be absent. Never adopt or overwrite a collision.
	if _, _, err = g.Secrets.SnapshotMetadata(ctx, candidate.Key); !errors.Is(err, ErrNoCredential) {
		return "", ErrGroupSnapshot
	}
	values, flags, err := g.Variables.CaptureRuntimeSelection(ctx, state.Actor, state.Selection)
	if err != nil {
		return "", ErrGroupSnapshot
	}
	payload := groupSnapshotPayload{SchemaVersion: 1, Scope: group.Scope, GroupID: group.GroupID, Generation: group.Generation, ClaimID: state.ID, CandidateID: candidate.ID, IssuedEpoch: candidate.Epoch, ConfigRevision: group.ConfigRevision, SelectionRevision: state.Selection.Revision, Values: values, Secrets: flags}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", ErrGroupSnapshot
	}
	defer clear(raw)
	if authorityErr := g.Store.AuthorizeSnapshot(ctx, state); authorityErr != nil {
		return "", authorityErr
	}
	version, err := g.Secrets.PutVersion(ctx, candidate.Key, raw)
	if err != nil || version != 1 {
		return "", ErrGroupSnapshot
	}
	_, observed, err := g.readCandidate(ctx, state, candidate)
	if err != nil {
		return "", err
	}
	if publishErr := g.Store.PublishSnapshot(ctx, state, observed); publishErr != nil {
		return "", publishErr
	}
	return state.ID, nil
}
func (g *GroupConfigurationSnapshots) Values(ctx context.Context, group domain.QualifiedGroupPlan) (values map[string]string, secrets map[string]bool, err error) {
	if g.Store == nil || g.Secrets == nil {
		return nil, nil, ErrGroupSnapshot
	}
	state, err := g.Store.BeginSnapshot(ctx, group, false)
	if err != nil {
		return nil, nil, err
	}
	if !validGroupSnapshotState(state, group) || state.Winner == nil || state.Winner.Identity == "" || state.Winner.Version != 1 {
		return nil, nil, ErrGroupSnapshot
	}
	payload, _, err := g.readCandidate(ctx, state, *state.Winner)
	if err != nil {
		return nil, nil, err
	}
	return payload.Values, payload.Secrets, nil
}

func GroupSnapshotKey(group domain.QualifiedGroupPlan, claim, candidate string) string {
	return fmt.Sprintf("%s/%s/group-config/%s/%d/%s/%s", group.OrgID, group.EnvironmentID, group.GroupID, group.Generation, claim, candidate)
}
