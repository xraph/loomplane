package deployments

import (
	"context"
	"fmt"
	"strconv"

	"github.com/xraph/loomplane/internal/notifications"
)

// KindEnvironmentFallback is the notice kind for an environment that fell
// back to a separate provider project.
const KindEnvironmentFallback = "environment.fallback"

// FallbackDB is the database FallbackNotice reads admins from and queues
// notices on. Loomplane's Postgres pool is one.
type FallbackDB interface {
	notifications.Execer
	notifications.Querier
}

// FallbackNotice tells a workspace's admins that an environment deploys to a
// separate provider project.
type FallbackNotice struct {
	DB FallbackDB
}

// NotifyFallback implements FallbackNotifier.
func (f *FallbackNotice) NotifyFallback(ctx context.Context, pe *ProviderEnvironment, environment string) error {
	admins, err := notifications.Admins(ctx, f.DB, pe.OrgID)
	if err != nil {
		return fmt.Errorf("list the admins of %s: %w", pe.OrgID, err)
	}
	// The creation time keeps a fallback after a reset a notice of its own.
	subject := pe.OrgID + ":" + pe.EnvironmentID + ":" + pe.Provider + ":" + pe.Scope + ":" + strconv.FormatInt(pe.CreatedAt.Unix(), 10)
	return notifications.Enqueue(ctx, f.DB, notifications.Notice{
		Kind:       KindEnvironmentFallback,
		OrgID:      pe.OrgID,
		ProjectID:  pe.ProjectID,
		SubjectID:  subject,
		Recipients: admins,
		Title:      environment + " deploys to a separate " + providerName(pe.Provider) + " project",
		Body:       pe.Reason,
		Path:       "/projects/" + pe.ProjectID,
	})
}
