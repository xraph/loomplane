package cloudinfra

import (
	"context"
	"fmt"

	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/security"
)

// ServerBuilderSetting identifies an explicit operator choice, including turning builds off.
type ServerBuilderSetting struct {
	Dedicated *bool `json:"dedicated"`
}

type serverBuilderStore interface {
	setServerBuilder(context.Context, *Server, bool) error
}

func (s *Service) SetServerBuilder(ctx context.Context, p security.Principal, cloud, id string, in ServerBuilderSetting) error {
	if _, err := s.authorizeChange(ctx, p, cloud); err != nil {
		return err
	}
	if err := s.Authz.Require(ctx, p, security.CloudAccountManage, ""); err != nil {
		return err
	}
	if in.Dedicated == nil {
		return fmt.Errorf("%w: choose whether this server accepts source builds", ErrInvalid)
	}
	v, err := s.Store.GetServer(ctx, p.OrgID, cloud, id)
	if err != nil {
		return err
	}
	store, ok := s.Store.(serverBuilderStore)
	if !ok {
		return ErrConflict
	}
	if err := store.setServerBuilder(ctx, v, *in.Dedicated); err != nil {
		return err
	}
	if s.Audit != nil {
		s.Audit.Record(ctx, projects.AuditEvent{Action: "cloud_server.set_builder", Severity: "info", Principal: p, Attributes: map[string]any{"cloud_id": cloud, "server_id": id, "connection_id": v.RuntimeConnectionID, "dedicated": *in.Dedicated}})
	}
	return nil
}

func (s *PgStore) setServerBuilder(ctx context.Context, v *Server, dedicated bool) error {
	if v.RuntimeConnectionID == "" {
		return fmt.Errorf("%w: this server needs a runtime connection", ErrConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// Pool edits lock the connection before the server. Pin both records so a
	// stale screen cannot classify a replacement machine or a pool member.
	var daemon, connectionStatus, workers string
	if err = tx.QueryRow(ctx, `SELECT coalesce(details->>'docker_id',''),status,coalesce(details->>'build_workers','')
		FROM loomplane_cloud_connections WHERE org_id=$1 AND id=$2 AND cloud='server' AND method<>'node' FOR UPDATE`, v.OrgID, v.RuntimeConnectionID).Scan(&daemon, &connectionStatus, &workers); err != nil {
		return storeError(err)
	}
	var status, verified string
	if err = tx.QueryRow(ctx, `SELECT status,coalesce(body->'health'->>'docker_id','') FROM loomplane_cloud_servers
		WHERE org_id=$1 AND cloud_id=$2 AND id=$3 AND runtime_connection_id=$4 AND status NOT IN ('deleted','disconnected') FOR SHARE`, v.OrgID, v.CloudID, v.ID, v.RuntimeConnectionID).Scan(&status, &verified); err != nil {
		return storeError(err)
	}
	mode := "shared"
	if dedicated {
		if status != "ready" || connectionStatus != "ready" || daemon == "" || verified != daemon {
			return fmt.Errorf("%w: check the server and its Docker connection before enabling source builds", ErrConflict)
		}
		var pooled bool
		if poolErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_server_pool_members m JOIN loomplane_cloud_servers s ON s.id=m.server_id
			WHERE s.status NOT IN ('deleted','disconnected') AND (s.runtime_connection_id=$1 OR s.body->'health'->>'docker_id'=$2))`, v.RuntimeConnectionID, daemon).Scan(&pooled); poolErr != nil {
			return poolErr
		}
		if pooled {
			return fmt.Errorf("%w: a shared pool member cannot be a dedicated builder", ErrConflict)
		}
		mode = "dedicated"
	}
	if workers != mode {
		// A generation change fences stale credential checks and replacements.
		_, err = tx.Exec(ctx, `UPDATE loomplane_cloud_connections SET details=jsonb_set(details,'{build_workers}',to_jsonb($3::text)),generation=generation+1,updated_at=clock_timestamp() WHERE org_id=$1 AND id=$2`, v.OrgID, v.RuntimeConnectionID, mode)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
