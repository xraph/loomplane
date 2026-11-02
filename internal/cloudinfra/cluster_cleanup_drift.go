package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xraph/grove/driver"

	"github.com/xraph/loomplane/internal/clouds/kubernetes"
	"github.com/xraph/loomplane/pkg/provider"
)

type cleanupObserverKey struct{}

// ObserveClusterCapacity attaches this only after its current actor, management
// reads and physical account/cluster identity have been checked.
type cleanupObserver struct {
	org, cloud, resource, ca string
	revision                 int64
	ref                      provider.ClusterRef
}

func checkCleanupAdmission(ctx context.Context, tx driver.Tx, in ClusterWorkloadInput) error {
	var reason string
	if err := tx.QueryRow(ctx, `SELECT loomplane_managed_cleanup_admission_block($1,$2,$3)`, in.OrgID, in.CloudID, in.ResourceID).Scan(&reason); err != nil {
		return err
	}
	if reason != "" {
		return &CapacityAdmissionError{Block: CapacityBlock{Reason: reason}}
	}
	return nil
}

type cleanupDriftSeen struct {
	key      cleanupResidualKey
	at       time.Time
	revision int64
}

func saveCleanupDrift(ctx context.Context, tx driver.Tx, current *ManagedResource, snapshot ClusterCapacitySnapshot, inventory *ClusterCleanupInventory) error {
	rows, err := tx.Query(ctx, `SELECT body FROM loomplane_managed_cleanup_tombstones WHERE org_id=$1 AND cloud_id=$2 AND resource_id=$3 FOR SHARE`, current.OrgID, current.CloudID, current.ID)
	if err != nil {
		return err
	}
	var tombstones []ClusterCleanupTombstone
	for rows.Next() {
		var raw []byte
		if scanErr := rows.Scan(&raw); scanErr != nil {
			rows.Close()
			return scanErr
		}
		var t ClusterCleanupTombstone
		if decodeErr := json.Unmarshal(raw, &t); decodeErr != nil {
			rows.Close()
			return decodeErr
		}
		tombstones = append(tombstones, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(tombstones) == 0 {
		return nil
	}
	index := indexCleanupInventory(inventory)
	authority, authorized := ctx.Value(cleanupObserverKey{}).(cleanupObserver)
	authorized = authorized && authority.org == current.OrgID && authority.cloud == current.CloudID && authority.resource == current.ID && authority.revision == snapshot.ObservationRevision && authority.ca == current.Observation.CAHash && authority.ref == snapshot.Ref
	now := time.Now().UTC()
	observedAt := snapshot.CheckedAt
	if inventory != nil && !inventory.CheckedAt.IsZero() {
		observedAt = inventory.CheckedAt
	}

	for i := range tombstones {
		t := &tombstones[i]
		active, readErr := activeCleanupResiduals(ctx, tx, t)
		if readErr != nil {
			return readErr
		}
		keys := make([]cleanupResidualKey, 0, len(active))
		for _, row := range active {
			keys = append(keys, row.key)
		}
		matches := index.match(t, keys)
		fresh := !observedAt.IsZero() && !observedAt.After(now) && now.Sub(observedAt) <= ClusterCapacityFreshness
		later := observedAt.After(t.ObservedAt) && snapshot.ObservationRevision > t.InventoryBarrier
		same := t.Ref == snapshot.Ref && t.ClusterCA == current.Observation.CAHash
		qualified := authorized && same && later && fresh && inventory != nil && !inventory.CheckedAt.IsZero() && matches.complete
		if qualified {
			var settled bool
			if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances WHERE org_id=$1 AND release_id=$2 AND (outcome IS NULL OR resolved_at>=$3 OR issued_at>=$3))`, t.OrgID, t.ReleaseID, observedAt).Scan(&settled); err != nil {
				return err
			}
			qualified = settled
		}
		if qualified && matches.needsServingProof(t, false) {
			verified, proofErr := qualifiedCleanupServing(ctx, tx, t, matches)
			if proofErr != nil {
				return proofErr
			}
			qualified = verified
		}
		if authorized && same && fresh && later {
			for key := range matches.residuals {
				if err := recordCleanupDrift(ctx, tx, t, key, snapshot.ObservationRevision, observedAt, true); err != nil {
					return err
				}
			}
		}
		if qualified {
			for _, row := range active {
				if !matches.residuals[row.key] && snapshot.ObservationRevision > row.revision && observedAt.After(row.at) {
					if err := recordCleanupDrift(ctx, tx, t, row.key, snapshot.ObservationRevision, observedAt, false); err != nil {
						return err
					}
				}
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO loomplane_managed_cleanup_coverage(org_id,release_id,observation_revision,checked_at,qualified) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(org_id,release_id) DO UPDATE SET observation_revision=excluded.observation_revision,checked_at=excluded.checked_at,qualified=excluded.qualified WHERE loomplane_managed_cleanup_coverage.observation_revision<excluded.observation_revision`, t.OrgID, t.ReleaseID, snapshot.ObservationRevision, observedAt, qualified); err != nil {
			return err
		}
	}
	return nil
}

func activeCleanupResiduals(ctx context.Context, tx driver.Tx, t *ClusterCleanupTombstone) ([]cleanupDriftSeen, error) {
	rows, err := tx.Query(ctx, `SELECT resource,uid,checked_at,observation_revision FROM loomplane_managed_cleanup_current_drift WHERE org_id=$1 AND release_id=$2 AND present`, t.OrgID, t.ReleaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cleanupDriftSeen
	for rows.Next() {
		var row cleanupDriftSeen
		if err := rows.Scan(&row.key.Resource, &row.key.UID, &row.at, &row.revision); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func recordCleanupDrift(ctx context.Context, tx driver.Tx, t *ClusterCleanupTombstone, key cleanupResidualKey, revision int64, observedAt time.Time, present bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO loomplane_managed_cleanup_drift_events(org_id,release_id,resource,uid,observation_revision,checked_at,present) VALUES($1,$2,$3,$4,$5,$6,$7)`, t.OrgID, t.ReleaseID, key.Resource, key.UID, revision, observedAt, present)
	return err
}

func qualifiedCleanupServing(ctx context.Context, tx driver.Tx, t *ClusterCleanupTombstone, m cleanupInventoryMatch) (bool, error) {
	if len(m.surviving) != 1 {
		return false, nil
	}
	d := m.surviving[0]
	if d.Generation <= t.OriginalGeneration[d.UID] || d.Deleting || d.ReleaseID == "" || d.ReleaseID == t.ReleaseID {
		return false, nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT c.body FROM loomplane_workload_controllers c
 JOIN loomplane_managed_workload_authority a ON a.org_id=c.org_id AND a.release_id=c.body->>'release_id' AND a.revoked_at IS NULL
 JOIN loomplane_bindings b ON b.org_id=c.org_id AND b.connection_id=c.connection_id AND b.external_id=c.instance_id AND b.plan_id=c.body->>'plan_id'
 WHERE c.org_id=$1 AND c.connection_id=$2 AND c.instance_id=$3 AND c.body->>'deployment_uid'=$4 AND c.body->>'release_id'=$5 AND c.body->>'deployment_generation'=$6::bigint::text
 AND a.body->>'RuntimeRevision'=c.body->>'runtime_revision' AND b.project_id=$7 AND b.environment_id=$8 AND b.subject='service' AND b.name=$9
 AND EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=a.org_id AND w.release_id=a.release_id AND w.epoch=a.epoch AND w.cleanup_grant_id IS NULL AND w.scope=a.body AND w.request->>'Group'='apps' AND w.request->>'Resource'='deployments' AND w.request->>'Method' IN ('POST','PUT') AND w.outcome->>'State'='accepted' AND w.outcome->>'UID'=$4 AND w.outcome->>'Generation'=$6::bigint::text)
 FOR SHARE OF c,a,b`, t.OrgID, t.ConnectionID, t.InstanceID, d.UID, d.ReleaseID, d.Generation, t.ProjectID, t.EnvironmentID, t.Service).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var r kubernetes.ControllerReceipt
	if json.Unmarshal(raw, &r) != nil || !r.Valid() || r.OrgID != t.OrgID || r.ProjectID != t.ProjectID || r.EnvironmentID != t.EnvironmentID || r.ServiceID != t.Service || r.ConnectionID != t.ConnectionID || r.InstanceID != t.InstanceID || r.Namespace != t.Namespace || r.NamespaceUID != t.NamespaceUID || r.ClusterCA != t.ClusterCA {
		return false, nil
	}
	if checkErr := r.CheckServingUpdate(ctx, tx); checkErr != nil {
		if errors.Is(checkErr, kubernetes.ErrManagedAuthority) {
			return false, nil
		}
		return false, checkErr
	}
	for _, h := range m.servingHPAs {
		if h.Deleting || h.DeploymentName != t.DeploymentName {
			return false, nil
		}
		var accepted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_managed_workload_issuances w WHERE w.org_id=$1 AND w.release_id=$2 AND w.cleanup_grant_id IS NULL AND EXISTS(SELECT 1 FROM loomplane_managed_workload_authority a WHERE a.org_id=w.org_id AND a.release_id=w.release_id AND a.epoch=w.epoch AND a.body=w.scope AND a.revoked_at IS NULL) AND w.request->>'Resource'='horizontalpodautoscalers' AND w.request->>'Method' IN ('POST','PUT') AND w.request->>'Name'=$3 AND w.outcome->>'State'='accepted' AND w.outcome->>'UID'=$4 AND w.outcome->>'Generation'=$5::bigint::text)`, t.OrgID, r.ReleaseID, h.Name, h.UID, h.Generation).Scan(&accepted); err != nil {
			return false, err
		}
		if !accepted {
			return false, nil
		}
	}
	return true, nil
}

// QualifyCleanupInventory verifies release-scoped absence. The caller still
// needs current cleanup authority and a strictly newer observation barrier.
func QualifyCleanupInventory(ctx context.Context, tx driver.Tx, t *ClusterCleanupTombstone, inventory *ClusterCleanupInventory) (bool, error) {
	match := indexCleanupInventory(inventory).match(t, nil)
	if !match.complete || len(match.residuals) > 0 {
		return false, nil
	}
	if match.needsServingProof(t, true) {
		return qualifiedCleanupServing(ctx, tx, t, match)
	}
	return true, nil
}
