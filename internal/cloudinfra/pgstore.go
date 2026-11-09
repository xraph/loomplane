package cloudinfra

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/driver"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/internal/cloudprofiles"
)

type PgStore struct{ db *pgdriver.PgDB }

func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

func storeError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23503" || pg.Code == "23514") {
		return ErrConflict
	}
	return err
}
func scanOperation(row pgx.Row) (*Operation, error) {
	var raw, principal []byte
	if checkErr := row.Scan(&raw, &principal); checkErr != nil {
		return nil, storeError(checkErr)
	}
	var o Operation
	if checkErr := json.Unmarshal(raw, &o); checkErr != nil {
		return nil, checkErr
	}
	if checkErr := json.Unmarshal(principal, &o.Principal); checkErr != nil {
		return nil, checkErr
	}
	return &o, nil
}
func (s *PgStore) CreatePreview(ctx context.Context, o *Operation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return checkErr
	}
	policy, err := capacityPolicyTx(ctx, tx, o.OrgID, o.CloudID)
	if err != nil {
		return err
	}
	if policy.Budget != nil && (o.Input.Kind == "create" || o.Input.Kind == "resize" || o.Input.Kind == "adopt") {
		reviewMoney, priceErr := serverBudgetMoney(ctx, tx, o)
		if priceErr != nil {
			return priceErr
		}
		o.BudgetReview, err = scopedBudgetReviewTx(ctx, tx, o.OrgID, o.CloudID, o.Provider, o.AccountID, o.ID, reviewMoney, policy, nil)
		if err != nil {
			return err
		}
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	p, err := json.Marshal(o.Principal)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO loomplane_infrastructure_operations(id,org_id,cloud_id,server_id,status,body,principal) VALUES($1,$2,$3,$4,$5,$6,$7)`, o.ID, o.OrgID, o.CloudID, o.ServerID, o.Status, raw, p)
	if err != nil {
		return storeError(err)
	}
	return tx.Commit()
}
func (s *PgStore) GetOperation(ctx context.Context, org, cloud, id string) (*Operation, error) {
	return scanOperation(s.db.QueryRow(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id))
}
func (s *PgStore) ListOperations(ctx context.Context, org, cloud string) ([]*Operation, error) {
	return s.operations(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 ORDER BY updated_at DESC,id LIMIT 100`, org, cloud)
}

// OpenCreates lists a cloud's create operations that were approved and
// haven't finished: their server is coming, or can be retried, and has a
// name. A preview isn't one, nor a create that succeeded or was
// cancelled. Unlike ListOperations it has no limit, so an old open create
// is never missed.
func (s *PgStore) OpenCreates(ctx context.Context, org, cloud string) ([]*Operation, error) {
	return s.operations(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations
		WHERE org_id=$1 AND cloud_id=$2 AND status NOT IN ('preview','succeeded','cancelled') AND body->'input'->>'kind'='create'
		ORDER BY updated_at,id`, org, cloud)
}
func (s *PgStore) Pending(ctx context.Context) ([]*Operation, error) {
	return s.operations(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE status IN ('pending','running','waiting') ORDER BY updated_at,id LIMIT 100`)
}
func (s *PgStore) operations(ctx context.Context, query string, args ...any) ([]*Operation, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Operation{}
	for rows.Next() {
		o, scanErr := scanOperation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func rollback(tx driver.Tx) { _ = tx.Rollback() } //nolint:errcheck // rollback is a no-op after commit

func (s *PgStore) Accept(ctx context.Context, o *Operation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := s.acceptOperationTx(ctx, tx, o); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PgStore) acceptOperationTx(ctx context.Context, tx driver.Tx, o *Operation) error {
	if checkErr := lockInfrastructureScope(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	if checkErr := s.reserveOperationCapacity(ctx, tx, o); checkErr != nil {
		return checkErr
	}
	if o.Input.Kind != "create" && o.Input.Kind != "adopt" {
		var revision int64
		if checkErr := tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND status NOT IN ('deleted','disconnected') FOR UPDATE`, o.OrgID, o.CloudID, o.ServerID).Scan(&revision); checkErr != nil {
			return storeError(checkErr)
		}
		if revision != o.Input.ServerRevision {
			return ErrConflict
		}
	}
	if o.Input.Kind == "adopt" {
		if _, claimErr := claimAdoption(ctx, tx, o); claimErr != nil {
			return claimErr
		}
	}

	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_infrastructure_operations SET status=$4,body=$5,updated_at=now() WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND status='preview' AND body->'money_estimate' IS NOT DISTINCT FROM $5::jsonb->'money_estimate' AND body->'budget_review' IS NOT DISTINCT FROM $5::jsonb->'budget_review'`, o.OrgID, o.CloudID, o.ID, o.Status, raw)
	if err != nil {
		return storeError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *PgStore) SaveOperation(ctx context.Context, o *Operation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if checkErr := lockCapacityCloud(ctx, tx, o.OrgID, o.CloudID); checkErr != nil {
		return checkErr
	}
	if o.Attempted && (o.Input.Kind == "create" || o.Input.Kind == "resize") {
		saved, readErr := scanOperation(tx.QueryRow(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, o.OrgID, o.CloudID, o.ID))
		if readErr != nil {
			return readErr
		}
		if !saved.Attempted {
			policy, checkErr := capacityPolicyTx(ctx, tx, saved.OrgID, saved.CloudID)
			if checkErr != nil {
				return checkErr
			}
			if policy.Budget != nil {
				money, e := serverBudgetMoney(ctx, tx, saved)
				if e != nil {
					return e
				}
				current, e := scopedBudgetReviewTx(ctx, tx, saved.OrgID, saved.CloudID, saved.Provider, saved.AccountID, saved.ID, money, policy, nil)
				if e != nil {
					return e
				}
				if budgetErr := checkBudgetReview(policy, saved.BudgetReview, current, saved.Input.AcknowledgeUnknownCosts); budgetErr != nil {
					return budgetErr
				}
			}
			if checkErr := s.reserveOperationCapacity(ctx, tx, saved); checkErr != nil {
				return checkErr
			}
			if checkErr := reserveLegacyAttempt(ctx, tx, saved); checkErr != nil {
				return checkErr
			}
		}
	}
	previous := o.StateRevision
	committed := false
	defer func() {
		if !committed {
			o.StateRevision = previous
		}
	}()
	o.StateRevision++
	raw, err := json.Marshal(o)
	if err != nil {
		o.StateRevision = previous
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_infrastructure_operations SET status=$4,body=$5,updated_at=now() WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND (status<>'preview' OR $4='cancelled') AND coalesce((body->>'state_revision')::bigint,0)=$6 AND body->'money_estimate' IS NOT DISTINCT FROM $5::jsonb->'money_estimate' AND body->'budget_review' IS NOT DISTINCT FROM $5::jsonb->'budget_review'`, o.OrgID, o.CloudID, o.ID, o.Status, raw, previous)
	if err != nil {
		o.StateRevision = previous
		return storeError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		o.StateRevision = previous
		return ErrConflict
	}
	if err := recordCapacityCleanup(ctx, tx, o); err != nil {
		return err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return commitErr
	}
	committed = true
	return nil
}

func (s *PgStore) GetServer(ctx context.Context, org, cloud, id string) (*Server, error) {
	var raw []byte
	if checkErr := s.db.QueryRow(ctx, `SELECT body FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id).Scan(&raw); checkErr != nil {
		return nil, storeError(checkErr)
	}
	var v Server
	if checkErr := json.Unmarshal(raw, &v); checkErr != nil {
		return nil, checkErr
	}
	return &v, nil
}
func (s *PgStore) ListServers(ctx context.Context, org, cloud string) ([]*Server, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND status NOT IN ('deleted','disconnected') ORDER BY id`, org, cloud)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Server{}
	for rows.Next() {
		var raw []byte
		var v Server
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &v); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}

// RuntimeServers lists an organization's servers that run a connection,
// across its Clouds, leaving out deleted and disconnected ones. GPU targets
// read the price of the server behind each server connection from them.
func (s *PgStore) RuntimeServers(ctx context.Context, org string) ([]*Server, error) {
	rows, err := s.db.Query(ctx, `SELECT body FROM loomplane_cloud_servers WHERE org_id=$1 AND runtime_connection_id<>'' AND status NOT IN ('deleted','disconnected') ORDER BY id`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Server{}
	for rows.Next() {
		var raw []byte
		var v Server
		if checkErr := rows.Scan(&raw); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := json.Unmarshal(raw, &v); checkErr != nil {
			return nil, checkErr
		}
		out = append(out, &v)
	}
	return out, rows.Err()
}
func (s *PgStore) SaveServer(ctx context.Context, v *Server) error {
	previous := v.StateRevision
	v.StateRevision++
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO loomplane_cloud_servers(id,org_id,cloud_id,connection_id,runtime_connection_id,provider,account_id,resource_id,ownership,revision,status,body) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(id) DO UPDATE SET runtime_connection_id=excluded.runtime_connection_id,resource_id=excluded.resource_id,revision=excluded.revision,status=excluded.status,body=excluded.body WHERE loomplane_cloud_servers.org_id=excluded.org_id AND loomplane_cloud_servers.cloud_id=excluded.cloud_id AND loomplane_cloud_servers.connection_id=excluded.connection_id AND loomplane_cloud_servers.ownership=excluded.ownership AND loomplane_cloud_servers.account_id=excluded.account_id AND (loomplane_cloud_servers.resource_id='' OR loomplane_cloud_servers.resource_id=excluded.resource_id) AND coalesce((loomplane_cloud_servers.body->>'state_revision')::bigint,0)=$13`, v.ID, v.OrgID, v.CloudID, v.ConnectionID, v.RuntimeConnectionID, v.Provider, v.AccountID, v.ResourceID, v.Ownership, v.Revision, v.Status, raw, previous)
	if err != nil {
		v.StateRevision = previous
		return storeError(err)
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		v.StateRevision = previous
		return ErrConflict
	}
	return nil
}
func (s *PgStore) WithOperationLock(ctx context.Context, id string, run func() error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var acquired bool
	if checkErr := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "cloud-infrastructure/"+id).Scan(&acquired); checkErr != nil {
		return checkErr
	}
	if !acquired {
		return ErrBusy
	}
	return run()
}

func (s *PgStore) OperationByID(ctx context.Context, org, id string) (*Operation, error) {
	return scanOperation(s.db.QueryRow(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE org_id=$1 AND id=$2`, org, id))
}
func (s *PgStore) ServerInUse(ctx context.Context, v *Server) (bool, error) {
	var used bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_connection_uses WHERE org_id=$1 AND connection_id=$2 AND kind NOT IN ('cloud_server','infrastructure_operation'))`, v.OrgID, v.RuntimeConnectionID).Scan(&used)
	return used, err
}

// RetireServer waits for any direct apply on the server's daemon to finish
// before it starts, and refuses while anything still uses the server.
func (s *PgStore) RetireServer(ctx context.Context, v *Server) error {
	// Read the daemon before the transaction: the wait below can last as
	// long as a deploy, and a table lock held through it would block DDL.
	daemon, err := s.directApplyDaemon(ctx, v.OrgID, v.RuntimeConnectionID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if waitErr := waitForDirectApply(ctx, tx, daemon); waitErr != nil {
		return waitErr
	}
	if checkErr := lockCapacityCloud(ctx, tx, v.OrgID, v.CloudID); checkErr != nil {
		return checkErr
	}
	if v.RuntimeConnectionID != "" {
		var current string
		if checkErr := tx.QueryRow(ctx, `SELECT CASE WHEN cloud='server' THEN coalesce(details->>'docker_id','') ELSE '' END FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 FOR UPDATE`, v.OrgID, v.RuntimeConnectionID).Scan(&current); checkErr != nil {
			return storeError(checkErr)
		}
		// The connection moved to another daemon since the wait above.
		if current != daemon {
			return ErrConflict
		}
		var used bool
		if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_connection_uses WHERE org_id=$1 AND connection_id=$2 AND kind NOT IN ('cloud_server','infrastructure_operation'))`, v.OrgID, v.RuntimeConnectionID).Scan(&used); checkErr != nil {
			return checkErr
		}
		if used {
			return ErrConflict
		}
	}
	previous := v.StateRevision
	v.StateRevision++
	v.Status = "retiring"
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE loomplane_cloud_servers SET status='retiring',body=$3 WHERE org_id=$1 AND id=$2 AND coalesce((body->>'state_revision')::bigint,0)=$4`, v.OrgID, v.ID, raw, previous)
	if err != nil {
		return err
	}
	n, err := tag.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		v.StateRevision = previous
		return ErrConflict
	}
	return tx.Commit()
}

// directApplyDaemon returns the docker_id behind connection, or "" when it
// isn't a verified server connection.
func (s *PgStore) directApplyDaemon(ctx context.Context, org, connection string) (string, error) {
	if connection == "" {
		return "", nil
	}
	var daemon string
	err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT details->>'docker_id' FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND cloud='server'),'')`, org, connection).Scan(&daemon)
	return daemon, err
}

// waitForDirectApply waits until no direct server apply runs on daemon, and
// keeps others off it until tx ends. Call it first in tx, holding nothing
// else and before the organization lock: a direct apply holds
// direct-apply/<docker_id> through its effects and takes the organization
// lock inside them, so waiting for it under that lock could close a cycle
// Postgres can't see.
func waitForDirectApply(ctx context.Context, tx driver.Tx, daemon string) error {
	if daemon == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('direct-apply/'||$1,0))`, daemon)
	return err
}

// refuseDuringDirectApply returns ErrDirectApplyRunning while a direct
// server apply runs on the daemon behind connection. It never waits, so
// callers can use it under the organization lock.
func refuseDuringDirectApply(ctx context.Context, tx driver.Tx, org, connection string) error {
	var free bool
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT pg_try_advisory_xact_lock(hashtextextended('direct-apply/'||(details->>'docker_id'),0)) FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND cloud='server' AND coalesce(details->>'docker_id','')<>''),true)`, org, connection).Scan(&free); err != nil {
		return err
	}
	if !free {
		return ErrDirectApplyRunning
	}
	return nil
}

func lockInfrastructureScope(ctx context.Context, tx driver.Tx, o *Operation) error {
	if checkErr := cloudprofiles.LockOrganization(ctx, tx, o.OrgID); checkErr != nil {
		return checkErr
	}
	var cloudRevision int64
	if checkErr := tx.QueryRow(ctx, `SELECT revision FROM loomplane_cloud_profiles WHERE org_id=$1 AND id=$2 FOR UPDATE`, o.OrgID, o.CloudID).Scan(&cloudRevision); checkErr != nil {
		return storeError(checkErr)
	}
	if cloudRevision != o.Input.CloudRevision {
		return ErrConflict
	}
	var connectionRevision time.Time
	var connectionGeneration int64
	if checkErr := tx.QueryRow(ctx, `SELECT updated_at,generation FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND (status='ready' OR $3='disconnect') FOR UPDATE`, o.OrgID, o.ConnectionID, o.Input.Kind).Scan(&connectionRevision, &connectionGeneration); checkErr != nil {
		return storeError(checkErr)
	}
	if (o.ConnectionGeneration == 0 && !connectionRevision.Equal(o.ConnectionRevision)) || (o.ConnectionGeneration != 0 && connectionGeneration != o.ConnectionGeneration) {
		return ErrConflict
	}
	// Adoption and disconnects run on a server's own connection. Some callers
	// already hold the organization lock here, so refuse rather than wait.
	return refuseDuringDirectApply(ctx, tx, o.OrgID, o.ConnectionID)
}

func claimAdoption(ctx context.Context, tx driver.Tx, o *Operation) (*Server, error) {
	var used bool
	if checkErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_connection_uses WHERE org_id=$1 AND connection_id=$2 AND kind NOT IN ('cloud_server','infrastructure_operation'))`, o.OrgID, o.ConnectionID).Scan(&used); checkErr != nil {
		return nil, checkErr
	}
	if used {
		return nil, ErrConflict
	}
	v := &Server{CreationOperationID: o.ID, ID: o.ServerID, OrgID: o.OrgID, CloudID: o.CloudID, Name: o.Input.Name, Ownership: o.Ownership, Provider: o.Provider, ConnectionID: o.ConnectionID, RuntimeConnectionID: o.ConnectionID, AccountID: o.AccountID, ResourceID: o.ResourceID, Revision: 1, StateRevision: 1, Status: "provisioning", CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
	var existing []byte
	lookupErr := tx.QueryRow(ctx, `SELECT body FROM loomplane_cloud_servers WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, o.OrgID, o.CloudID, o.ServerID).Scan(&existing)
	if lookupErr == nil {
		var old Server
		if err := json.Unmarshal(existing, &old); err != nil {
			return nil, err
		}
		if old.CreationOperationID != o.ID || old.ConnectionID != o.ConnectionID || old.RuntimeConnectionID != "" || old.Provider != o.Provider || old.AccountID != o.AccountID || old.ResourceID != o.ResourceID || old.Ownership != "adopted" || old.Status != "provisioning" {
			return nil, ErrConflict
		}
		old.RuntimeConnectionID = o.ConnectionID
		old.StateRevision++
		v = &old
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return nil, lookupErr
	}
	body, marshalErr := json.Marshal(v)
	if marshalErr != nil {
		return nil, marshalErr
	}
	// The direct-apply try-lock in lockInfrastructureScope keeps this claim
	// out of a running direct apply. The connection row lock only meets a
	// direct apply's short admission check, which commits before effects.
	if lookupErr == nil {
		if _, updateErr := tx.Exec(ctx, `UPDATE loomplane_cloud_servers SET runtime_connection_id=$3,body=$4 WHERE org_id=$1 AND id=$2`, v.OrgID, v.ID, v.RuntimeConnectionID, body); updateErr != nil {
			return nil, storeError(updateErr)
		}
		return v, nil
	}
	if _, insertErr := tx.Exec(ctx, `INSERT INTO loomplane_cloud_servers(id,org_id,cloud_id,connection_id,runtime_connection_id,provider,account_id,resource_id,ownership,revision,status,body) VALUES($1,$2,$3,$4,$4,$5,$6,$7,'adopted',1,'provisioning',$8)`, v.ID, v.OrgID, v.CloudID, v.ConnectionID, v.Provider, v.AccountID, v.ResourceID, body); insertErr != nil {
		return nil, storeError(insertErr)
	}
	return v, nil
}

// ClaimAdoption recovers accepted operations written before transactional claims.
func (s *PgStore) ClaimAdoption(ctx context.Context, org, cloud, id string) (*Server, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	o, err := scanOperation(tx.QueryRow(ctx, `SELECT body,principal FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3`, org, cloud, id))
	if err != nil {
		return nil, err
	}
	if o.Input.Kind != "adopt" || o.Ownership != "adopted" {
		return nil, ErrConflict
	}
	if scopeErr := lockInfrastructureScope(ctx, tx, o); scopeErr != nil {
		return nil, scopeErr
	}
	if checkErr := s.reserveOperationCapacity(ctx, tx, o); checkErr != nil {
		return nil, checkErr
	}
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM loomplane_infrastructure_operations WHERE org_id=$1 AND cloud_id=$2 AND id=$3 FOR UPDATE`, org, cloud, id).Scan(&status); err != nil {
		return nil, storeError(err)
	}
	if status != "pending" && status != "running" && status != "waiting" {
		return nil, ErrConflict
	}
	v, err := claimAdoption(ctx, tx, o)
	if err != nil {
		return nil, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return v, nil
}
