package recovery

import "github.com/xraph/loomplane/internal/planning"

func supportsRecovery(cfg *planning.ResourceConfig) bool {
	return cfg.Catalog != nil && cfg.Catalog.Engine() != "" && cfg.Catalog.Engine() == cfg.Kind && (cfg.Provider == "server" || cfg.Provider == "kubernetes" && cfg.Kind == "postgres")
}
