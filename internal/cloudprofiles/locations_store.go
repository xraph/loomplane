package cloudprofiles

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/xraph/loomplane/internal/connections"
)

var _ LocationStore = (*PgStore)(nil)

// SaveLocation serializes metadata and target/resource membership under the organization lock.
func (s *PgStore) SaveLocation(ctx context.Context, l *CloudLocation, cloudRevision, previous int64) error {
	if l.Revision != previous+1 || previous < 0 || cloudRevision < 1 {
		return ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	if lockErr := LockOrganization(ctx, tx, l.OrgID); lockErr != nil {
		return lockErr
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_profiles WHERE org_id=$1 AND id=$2 FOR UPDATE`, l.OrgID, l.CloudID).Scan(&revision); err != nil {
		return storeError(err)
	}
	if revision != cloudRevision {
		return ErrConflict
	}
	var cloud, account, status string
	var details map[string]string
	if err = tx.QueryRow(ctx, `SELECT cloud,account,status,details FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 FOR SHARE`, l.OrgID, l.ConnectionID).Scan(&cloud, &account, &status, &details); err != nil {
		return storeError(err)
	}
	if status != connections.StatusReady || connections.AccountIdentity(cloud, account, details) != l.AccountID {
		return ErrInvalid
	}
	body, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if previous == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_managed_resources(id,org_id,cloud_id,kind,provider,account_id,connection_id,external_id,incarnation,ownership,status,revision,body) VALUES($1,$2,$3,'location',$4,$5,$6,$1,$1,'external','ready',1,$7::jsonb || jsonb_build_object('kind','location','ownership','external','external_id',$1::text,'incarnation',$1::text,'status','ready','schema_version',1,'created_at',now(),'updated_at',now()))`, l.ID, l.OrgID, l.CloudID, l.Provider, l.AccountID, l.ConnectionID, body)
		if err != nil {
			return storeError(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO loomplane_cloud_locations(id,org_id,cloud_id,name,provider,account_id,connection_id,region,revision,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, l.ID, l.OrgID, l.CloudID, l.Name, l.Provider, l.AccountID, l.ConnectionID, l.Region, l.Revision, body)
	} else {
		old, readErr := scanLocation(tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_locations WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, l.OrgID, l.CloudID, l.ID))
		if readErr != nil {
			return readErr
		}
		if old.Revision != previous || !sameLocationPlacement(*old, *l) {
			return ErrConflict
		}
		if old.ConnectionID != l.ConnectionID {
			var used bool
			if queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_targets WHERE org_id=$1 AND location_id=$2) OR EXISTS(SELECT 1 FROM loomplane_resource_relations WHERE org_id=$1 AND (source_id=$2 OR destination_id=$2))`, l.OrgID, l.ID).Scan(&used); queryErr != nil {
				return queryErr
			}
			if used {
				return ErrInUse
			}
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_cloud_locations SET name=$4,connection_id=$5,revision=$6,body=$7 WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, l.OrgID, l.CloudID, l.ID, l.Name, l.ConnectionID, l.Revision, body)
		if err != nil {
			return storeError(err)
		}
		_, err = tx.Exec(ctx, `UPDATE loomplane_managed_resources SET connection_id=$3,revision=revision+1,body=body || $4::jsonb || jsonb_build_object('revision',revision+1,'updated_at',now()) WHERE org_id=$1 AND id=$2`, l.OrgID, l.ID, l.ConnectionID, body)
	}
	if err != nil {
		return storeError(err)
	}
	_, err = tx.Exec(ctx, `UPDATE loomplane_cloud_profiles SET revision=revision+1,updated_at=now() WHERE org_id=$1 AND id=$2`, l.OrgID, l.CloudID)
	if err != nil {
		return storeError(err)
	}
	return tx.Commit()
}

func scanLocation(row pgx.Row) (*CloudLocation, error) {
	var body []byte
	if err := row.Scan(&body); err != nil {
		return nil, storeError(err)
	}
	var l CloudLocation
	if err := json.Unmarshal(body, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

const locationGrant = `($2='' OR EXISTS(SELECT 1 FROM loomplane_cloud_project_grants g JOIN loomplane_cloud_targets t ON t.org_id=g.org_id AND t.cloud_id=g.cloud_id AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)) WHERE g.org_id=l.org_id AND g.cloud_id=l.cloud_id AND g.project_id=$2 AND NOT g.revoked AND t.location_id=l.id))`

func (s *PgStore) GetLocation(ctx context.Context, org, project, cloud, id string) (*CloudLocation, error) {
	location, err := scanLocation(s.db.QueryRow(ctx, `SELECT body FROM loomplane_cloud_locations l WHERE org_id=$1 AND `+locationGrant+` AND cloud_id=$3 AND id=$4`, org, project, cloud, id))
	if err == nil && project != "" {
		v := projectLocation(*location)
		location = &v
	}
	return location, err
}

func (s *PgStore) ListLocations(ctx context.Context, org, project, cloud string) ([]CloudLocation, error) {
	if _, err := s.GetProfile(ctx, org, project, cloud); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_cloud_locations l WHERE org_id=$1 AND `+locationGrant+` AND cloud_id=$3 ORDER BY lower(name),id`, org, project, cloud)
	if err != nil {
		return nil, storeError(err)
	}
	defer rows.Close()
	out := []CloudLocation{}
	for rows.Next() {
		l, scanErr := scanLocation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if project != "" {
			v := projectLocation(*l)
			l = &v
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *PgStore) DeleteLocation(ctx context.Context, org, cloud, id string, revision, cloudRevision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackSync(tx)
	if lockErr := LockOrganization(ctx, tx, org); lockErr != nil {
		return lockErr
	}
	if cloudRevision < 1 {
		return ErrConflict
	}
	var currentCloud int64
	if queryErr := tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_profiles WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, cloud).Scan(&currentCloud); queryErr != nil {
		return storeError(queryErr)
	}
	if currentCloud != cloudRevision {
		return ErrConflict
	}
	var current int64
	if err = tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_locations WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, org, cloud, id).Scan(&current); err != nil {
		return storeError(err)
	}
	if current != revision {
		return ErrConflict
	}
	var used bool
	if queryErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_targets WHERE org_id=$1 AND location_id=$2) OR EXISTS(SELECT 1 FROM loomplane_resource_relations WHERE org_id=$1 AND (source_id=$2 OR destination_id=$2))`, org, id).Scan(&used); queryErr != nil {
		return queryErr
	}
	if used {
		return ErrInUse
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_cloud_locations WHERE org_id=$1 AND id=$2`, org, id); err != nil {
		return deleteError(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM loomplane_managed_resources WHERE org_id=$1 AND id=$2 AND kind='location'`, org, id); err != nil {
		return deleteError(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE loomplane_cloud_profiles SET revision=revision+1,updated_at=now() WHERE org_id=$1 AND id=$2`, org, cloud); err != nil {
		return storeError(err)
	}
	return tx.Commit()
}
