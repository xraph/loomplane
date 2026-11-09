package agentknowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/knowledge"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/security"
)

// ProjectAuthorizer checks a permission on one project;
// *security.Authorizer is the real one, which counts a team's grant on
// the project.
type ProjectAuthorizer interface {
	AllowedOn(ctx context.Context, p security.Principal, perm security.Permission, projectID string) (bool, error)
}

// Readable is chunks less those the run on ctx may not read. A report's
// chunk, or any chunk about one project, needs deployment:read on its
// project for the run's principal (the agent, with the person it acts
// for), the same check that shows a report anywhere else. A report's
// chunk that names no project needs deployment:read on the whole
// workspace, which reaches every project. A run scoped to one project
// reads only that project's such chunks, whatever else the person may
// read. Every such chunk is dropped when ctx carries no agent principal
// or authz is nil. The docs and workspace-wide documents pass. The error
// joins the checks that failed; their chunks are dropped too.
func Readable(ctx context.Context, authz ProjectAuthorizer, chunks []knowledge.ScoredChunk) ([]knowledge.ScoredChunk, error) {
	call, callErr := agentops.CallFrom(cortex.Invocation{Subject: cortex.Subject{
		Scope: cortex.ScopeFromContext(ctx), Principal: cortex.PrincipalFromContext(ctx),
	}})
	allowed := map[string]bool{}
	var errs []error
	out := make([]knowledge.ScoredChunk, 0, len(chunks))
	for _, c := range chunks {
		projectID, gated := chunkProject(c.Metadata)
		if !gated {
			out = append(out, c)
			continue
		}
		if callErr != nil || authz == nil || (call.Scope.ProjectID != "" && projectID != call.Scope.ProjectID) {
			continue
		}
		ok, seen := allowed[projectID]
		if !seen {
			var err error
			// With no project, "" checks at the workspace's root.
			ok, err = authz.AllowedOn(ctx, call.Principal, security.DeploymentRead, projectID)
			if err != nil {
				errs = append(errs, fmt.Errorf("agentknowledge: check %s on %q: %w", security.DeploymentRead.Name(), projectID, err))
				ok = false
			}
			allowed[projectID] = ok
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, errors.Join(errs...)
}

// chunkProject is the project a chunk is about, and whether reading it
// depends on that project: it's a report's, or it names a project.
func chunkProject(md map[string]string) (string, bool) {
	projectID := strings.TrimSpace(md["project_id"])
	report := md["kind"] == KindReport || strings.HasPrefix(md["source_key"], ReportSourcePrefix)
	return projectID, report || projectID != ""
}
