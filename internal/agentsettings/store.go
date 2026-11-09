package agentsettings

import "context"

// Store keeps the agent's columns of the workspace AI settings row, and
// only those. Every write bumps the row's revision, which the workspace's
// own settings share.
type Store interface {
	// Get returns the agent's settings. A workspace with no row yet gets
	// the agent off, on the server's key, at Revision 0.
	Get(ctx context.Context, orgID string) (*Settings, error)
	// ByNexusTenant returns the settings of the workspace whose nexus
	// tenant is tenantID, or ErrNotFound.
	ByNexusTenant(ctx context.Context, tenantID string) (*Settings, error)
	// Save writes s's agent columns and UpdatedBy when the stored revision
	// is expectRevision (0 for a workspace with no row yet), and ErrStale
	// otherwise. It leaves the budget, embeddings and Hugging Face columns
	// alone. On success s holds the stored row.
	Save(ctx context.Context, s *Settings, expectRevision int64) error
	// ListEnabled returns the IDs of the workspaces whose agent is on,
	// sorted.
	ListEnabled(ctx context.Context) ([]string, error)
}
