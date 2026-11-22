package deployments

import (
	"encoding/json"
	"errors"

	"github.com/xraph/loomplane/internal/manageddb"
	"github.com/xraph/loomplane/internal/planning"
)

// Only an approved update to an owned binding can resize an existing resource.
func reviewedResize(job *Job) (bool, error) {
	cfg := job.Action.Resource
	if cfg == nil || job.Binding == nil || job.Action.Op != planning.Update {
		return false, nil
	}
	var old planning.ResourceConfig
	if len(job.Binding.Config) == 0 && len(cfg.Settings) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(job.Binding.Config, &old); err != nil {
		return false, err
	}

	for _, f := range manageddb.CapacityFields(cfg.Provider, cfg.Kind) {
		before, after := old.Settings[f.Name], cfg.Settings[f.Name]
		if before == "" {
			before = f.Default
		}
		if after == "" {
			after = f.Default
		}
		if before != after {
			if job.Connection == nil || job.Connection.ID != job.Binding.ConnectionID || (job.Action.ConnectionID != "" && job.Action.ConnectionID != job.Binding.ConnectionID) || cfg.ExistingResource != nil || old.ExistingResource != nil || old.Provider != cfg.Provider || old.Kind != cfg.Kind || old.Region != cfg.Region {
				return false, errors.New("resource placement changed since capacity was reviewed")
			}
			return true, nil
		}
	}
	return false, nil
}
