package cloudinfra

import (
	"context"
	"reflect"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// CapacityTemplatePin records the provider connection used to check fit.
type CapacityTemplatePin struct {
	ConnectionGeneration int64  `json:"connection_generation"`
	AccountID            string `json:"account_id"`
}

// ValidateCapacityTemplate returns fit and connection pins without exposing
// provider catalogs or granting infrastructure management permission.
func (s *Service) ValidateCapacityTemplate(ctx context.Context, p security.Principal, d CapacityDemand, template PoolExpansionPolicy) (*CapacityTemplatePin, error) {
	if p.OrgID == "" || p.UserID == "" || p.OrgID != d.OrgID || p.ReadOnly || s.Authz == nil {
		return nil, security.ErrForbidden
	}
	if err := s.Authz.Require(ctx, p, security.DeploymentApprove, d.ProjectID); err != nil {
		return nil, err
	}
	st, ok := s.Store.(*PgStore)
	if !ok || s.Connections == nil || s.Adapters == nil {
		return nil, ErrInvalid
	}
	var allowed bool
	if err := st.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM loomplane_cloud_targets t JOIN loomplane_cloud_project_grants g ON g.org_id=t.org_id AND g.cloud_id=t.cloud_id AND g.project_id=$2 AND NOT g.revoked JOIN loomplane_environments e ON e.org_id=t.org_id AND e.project_id=g.project_id AND e.id=$3 WHERE t.org_id=$1 AND t.id=$4 AND t.cloud_id=$5 AND EXISTS(SELECT 1 FROM loomplane_server_pools pool WHERE pool.org_id=t.org_id AND pool.cloud_id=t.cloud_id AND pool.id=$6) AND (cardinality(g.target_ids)=0 OR t.id=ANY(g.target_ids)))`, d.OrgID, d.ProjectID, d.EnvironmentID, d.TargetID, d.CloudID, d.PoolID).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, security.ErrForbidden
	}
	policy, err := st.GetCapacityPolicy(ctx, d.OrgID, d.CloudID)
	if err != nil {
		return nil, err
	}
	current, found := expansionPool(policy, d.PoolID)
	current.MinServers, current.MaxServers, current.Limits = 0, 0, nil
	if !found || !policy.Enabled || policy.Revision != d.PolicyRevision || !reflect.DeepEqual(current, template) {
		return nil, ErrConflict
	}
	conn, raw, err := s.Connections.Credential(ctx, d.OrgID, template.ConnectionID)
	if err != nil {
		return nil, err
	}
	credential, err := Credential(conn, raw)
	if err != nil {
		return nil, err
	}
	adapter, err := s.Adapters.Server(conn.Cloud, provider.ServerProvisioning)
	if err != nil {
		return nil, err
	}
	catalog, ok := adapter.(provider.ServerOptionsAdapter)
	if !ok {
		return nil, provider.ErrInfrastructureUnsupported
	}
	options, err := catalog.ServerOptions(ctx, credential)
	if err != nil {
		return nil, err
	}
	if _, err = SelectExpansionSize(options, template, d); err != nil {
		return nil, err
	}
	if conn.Generation < 1 || credential.AccountID == "" {
		return nil, ErrConflict
	}
	return &CapacityTemplatePin{ConnectionGeneration: conn.Generation, AccountID: credential.AccountID}, nil
}
