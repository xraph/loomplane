package deployments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"
	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/cloudprofiles"
	"github.com/xraph/loomplane/internal/environments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
	domain "github.com/xraph/loomplane/pkg/federation"
)

// GroupExecutionAuthority separates external prechecks from the current facts
// checked on the transaction connection. Both are mandatory for every issuance.
type GroupExecutionAuthority interface {
	LockGroupAuthorityTx(context.Context, driver.Tx) error
	AuthorizePlacementGroup(context.Context, domain.QualifiedGroupPlan) (security.Principal, error)
	AuthorizePlacementGroupTx(context.Context, driver.Tx, domain.QualifiedGroupPlan, security.Principal) error
}

type PlacementGroupStore struct {
	DB        *pgdriver.PgDB
	Authority GroupExecutionAuthority
}

func (s *PlacementGroupStore) snapshotTx(ctx context.Context, group domain.QualifiedGroupPlan, fn func(driver.Tx, security.Principal) error) (err error) {
	if s.DB == nil || s.Authority == nil {
		return ErrGroupSnapshot
	}
	actor, err := s.Authority.AuthorizePlacementGroup(ctx, group)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if sourceErr := environments.LockRuntimeSourcesTx(ctx, tx); sourceErr != nil {
		return sourceErr
	}
	if authorityErr := s.Authority.LockGroupAuthorityTx(ctx, tx); authorityErr != nil {
		return authorityErr
	}
	if lockErr := cloudprofiles.LockOrganization(ctx, tx, group.OrgID); lockErr != nil {
		return lockErr
	}
	if authorityErr := s.Authority.AuthorizePlacementGroupTx(ctx, tx, group, actor); authorityErr != nil {
		return authorityErr
	}
	if callbackErr := fn(tx, actor); callbackErr != nil {
		return callbackErr
	}
	return tx.Commit()
}
func readGroupSnapshotState(ctx context.Context, tx driver.Tx, group domain.QualifiedGroupPlan) (GroupSnapshotState, error) {
	var out GroupSnapshotState
	var body, config, actor []byte
	var winnerID, identity *string
	var version *int64
	err := tx.QueryRow(ctx, `SELECT e.snapshot_id,e.epoch,e.snapshot_candidate,e.snapshot_identity,e.snapshot_version,r.body,c.body,e.actor FROM loomplane_group_executions e JOIN loomplane_placement_group_revisions r USING(org_id,group_id,generation) JOIN loomplane_group_configurations c USING(org_id,group_id,generation) WHERE e.org_id=$1 AND e.group_id=$2 AND e.generation=$3 AND e.cancelled_at IS NULL FOR UPDATE OF e`, group.OrgID, group.GroupID, group.Generation).Scan(&out.ID, &out.Epoch, &winnerID, &identity, &version, &body, &config, &actor)
	if err != nil {
		return out, err
	}
	var private planning.GroupExecutionConfiguration
	if json.Unmarshal(body, &out.Group) != nil || json.Unmarshal(config, &private) != nil || json.Unmarshal(actor, &out.Actor) != nil || out.Group.Revision != group.Revision || out.Group.Scope != group.Scope || private.Revision != group.ConfigRevision {
		return out, ErrGroupSnapshot
	}
	out.Selection = private.Selection
	rows, err := tx.Query(ctx, `SELECT id,epoch,vault_key FROM loomplane_group_snapshot_candidates WHERE org_id=$1 AND group_id=$2 AND generation=$3 ORDER BY epoch`, group.OrgID, group.GroupID, group.Generation)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c GroupSnapshotCandidate
		if scanErr := rows.Scan(&c.ID, &c.Epoch, &c.Key); scanErr != nil {
			return out, scanErr
		}
		out.Candidates = append(out.Candidates, c)
		if winnerID != nil && c.ID == *winnerID {
			if identity == nil || version == nil {
				return out, ErrGroupSnapshot
			}
			c.Identity, c.Version = *identity, *version
			out.Winner = &c
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return out, rowsErr
	}
	if winnerID != nil && out.Winner == nil {
		return out, ErrGroupSnapshot
	}
	return out, nil
}
func (s *PlacementGroupStore) BeginSnapshot(ctx context.Context, group domain.QualifiedGroupPlan, write bool) (GroupSnapshotState, error) {
	var out GroupSnapshotState
	err := s.snapshotTx(ctx, group, func(tx driver.Tx, _ security.Principal) error {
		var err error
		out, err = readGroupSnapshotState(ctx, tx, group)
		if err != nil {
			return err
		}
		if write && out.Winner == nil {
			return tx.QueryRow(ctx, `UPDATE loomplane_group_executions SET epoch=epoch+1 WHERE org_id=$1 AND group_id=$2 AND generation=$3 AND snapshot_candidate IS NULL AND cancelled_at IS NULL RETURNING epoch`, group.OrgID, group.GroupID, group.Generation).Scan(&out.Epoch)
		}
		return nil
	})
	return out, err
}
func sameSnapshotEpoch(actual, expected GroupSnapshotState) bool {
	return actual.ID == expected.ID && actual.Epoch == expected.Epoch && actual.Group.Revision == expected.Group.Revision && actual.Selection.Revision == expected.Selection.Revision
}
func (s *PlacementGroupStore) AuthorizeSnapshot(ctx context.Context, state GroupSnapshotState) error {
	return s.snapshotTx(ctx, state.Group, func(tx driver.Tx, _ security.Principal) error {
		current, err := readGroupSnapshotState(ctx, tx, state.Group)
		if err != nil {
			return err
		}
		if !sameSnapshotEpoch(current, state) {
			return ErrGroupSnapshot
		}
		return nil
	})
}
func (s *PlacementGroupStore) IssueSnapshot(ctx context.Context, state GroupSnapshotState) (GroupSnapshotCandidate, error) {
	var candidate GroupSnapshotCandidate
	err := s.snapshotTx(ctx, state.Group, func(tx driver.Tx, _ security.Principal) error {
		current, err := readGroupSnapshotState(ctx, tx, state.Group)
		if err != nil {
			return err
		}
		if !sameSnapshotEpoch(current, state) || current.Winner != nil || current.Epoch < 1 {
			return ErrGroupSnapshot
		}
		candidate = GroupSnapshotCandidate{ID: typeid.MustGenerate("pgcandidate").String(), Epoch: current.Epoch}
		candidate.Key = GroupSnapshotKey(current.Group, current.ID, candidate.ID)
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_group_snapshot_candidates(org_id,group_id,generation,id,epoch,vault_key) VALUES($1,$2,$3,$4,$5,$6)`, current.Group.OrgID, current.Group.GroupID, current.Group.Generation, candidate.ID, candidate.Epoch, candidate.Key)
		return err
	})
	if err != nil {
		return GroupSnapshotCandidate{}, err
	}
	return candidate, nil
}
func (s *PlacementGroupStore) PublishSnapshot(ctx context.Context, state GroupSnapshotState, candidate GroupSnapshotCandidate) error {
	if !validGroupSnapshotCandidate(state, candidate) || candidate.Identity == "" || candidate.Version != 1 {
		return ErrGroupSnapshot
	}
	return s.snapshotTx(ctx, state.Group, func(tx driver.Tx, _ security.Principal) error {
		current, err := readGroupSnapshotState(ctx, tx, state.Group)
		if err != nil {
			return err
		}
		if !sameSnapshotEpoch(current, state) {
			return ErrGroupSnapshot
		}
		if current.Winner != nil {
			if *current.Winner != candidate {
				return ErrGroupSnapshot
			}
			return nil
		}
		found := false
		for _, issued := range current.Candidates {
			if issued.ID == candidate.ID && issued.Epoch == candidate.Epoch && issued.Key == candidate.Key {
				found = true
				break
			}
		}
		if !found {
			return ErrGroupSnapshot
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_group_executions SET snapshot_candidate=$4,snapshot_identity=$5,snapshot_version=$6 WHERE org_id=$1 AND group_id=$2 AND generation=$3 AND epoch=$7 AND snapshot_candidate IS NULL AND cancelled_at IS NULL`, current.Group.OrgID, current.Group.GroupID, current.Group.Generation, candidate.ID, candidate.Identity, candidate.Version, current.Epoch)
		return err
	})
}
