package deployments

import (
	"encoding/json"
	"fmt"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/pkg/provider"
)

func managedDatabaseReview(cfg *planning.ResourceConfig, conn *connections.Connection, target string) error {
	if cfg == nil || cfg.Managed == nil {
		return nil
	}
	profile, err := manageddb.Postgres(target, cfg.Version)
	if err != nil || cfg.Kind != "postgres" || cfg.Provider != target || cfg.Managed.Profile != *profile {
		return fmt.Errorf("managed database review changed: request a fresh reviewed profile")
	}
	cloud := connections.AWS
	if target == "cloud-sql" {
		cloud = connections.GCP
	}
	if conn == nil || conn.ID != cfg.Managed.ConnectionID || conn.Cloud != cloud || conn.Status != connections.StatusReady || cfg.Region == "" || (cloud == connections.AWS && cfg.Region != conn.Details["region"]) {
		return fmt.Errorf("managed database review changed: check the selected connection and region")
	}
	identity := connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)
	if identity == "" || cfg.Managed.AccountIdentity != identity {
		return fmt.Errorf("managed database review changed: check the cloud account identity")
	}
	return nil
}

func managedDatabaseBindingReview(b *planning.Binding, conn *connections.Connection, target string) (*planning.ResourceConfig, error) {
	var cfg planning.ResourceConfig
	if b == nil || len(b.Config) == 0 {
		return &cfg, nil
	}
	if err := json.Unmarshal(b.Config, &cfg); err != nil {
		return nil, fmt.Errorf("managed database review changed: the stored configuration cannot be read")
	}
	if err := managedDatabaseReview(&cfg, conn, target); err != nil {
		return nil, err
	}
	if cfg.Managed != nil && (b.Provider != target || b.ConnectionID != cfg.Managed.ConnectionID || b.Region != cfg.Region) {
		return nil, fmt.Errorf("managed database review changed: the binding no longer matches its account and region")
	}
	return &cfg, nil
}

func managedDatabaseRequest(job *Job, target string) (provider.ManagedDatabaseProfile, error) {
	defaults, err := manageddb.Defaults(target)
	if err != nil {
		return defaults, err
	}
	if job.Replaces != nil {
		replaced, decodeErr := managedDatabaseBindingReview(job.Replaces, job.Connection, target)
		if decodeErr != nil {
			return defaults, decodeErr
		}
		if replaced.Managed != nil {
			return defaults, fmt.Errorf("managed database review changed: replacing a persistent database requires a tested restore into a fresh service")
		}
	}
	cfg := job.Action.Resource
	if reviewErr := managedDatabaseReview(cfg, job.Connection, target); reviewErr != nil {
		return defaults, reviewErr
	}
	old, err := managedDatabaseBindingReview(job.Binding, job.Connection, target)
	if err != nil {
		return defaults, err
	}
	if old.Managed != nil && (cfg.Managed == nil || *old.Managed != *cfg.Managed || cfg.Region != old.Region || cfg.Kind != old.Kind || cfg.Version != old.Version) {
		return defaults, fmt.Errorf("managed database review changed: moving or upgrading requires a tested restore into a fresh service")
	}
	if cfg.Managed != nil {
		if job.Action.Region != cfg.Region || job.Action.Provider != target {
			return defaults, fmt.Errorf("managed database review changed: action placement differs from its profile")
		}
		return cfg.Managed.Profile, nil
	}
	return defaults, nil
}
