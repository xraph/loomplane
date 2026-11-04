package agentknowledge

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/knowledge"
	"github.com/xraph/weave/chunker"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/security"
)

// A search returns chunks, and weave gives a chunk only its own metadata,
// never its document's. So Ingest puts the keys Readable decides on, the
// document's kind and project, on the context, and Chunker writes them on
// every chunk it makes. Content that is already indexed answers Unchanged
// before it is chunked, as before.

type chunkMetadataKey struct{}

// withChunkMetadata is ctx with md for Chunker to write on every chunk.
func withChunkMetadata(ctx context.Context, md map[string]string) context.Context {
	return context.WithValue(ctx, chunkMetadataKey{}, md)
}

// Chunker is inner with the metadata Ingest put on the context written on
// every chunk, over any key of inner's own. Weave's engine must use it
// for Readable to know whose a report's chunk is: without it a report's
// chunk has no project, and Readable drops it.
func Chunker(inner chunker.Chunker) chunker.Chunker { return stampingChunker{inner: inner} }

type stampingChunker struct{ inner chunker.Chunker }

func (c stampingChunker) Chunk(ctx context.Context, text string, opts *chunker.Options) ([]chunker.ChunkResult, error) {
	out, err := c.inner.Chunk(ctx, text, opts)
	if err != nil {
		return nil, err
	}
	md, ok := ctx.Value(chunkMetadataKey{}).(map[string]string)
	if !ok || len(md) == 0 {
		return out, nil
	}
	for i := range out {
		stamped := make(map[string]string, len(out[i].Metadata)+len(md))
		maps.Copy(stamped, out[i].Metadata)
		maps.Copy(stamped, md)
		out[i].Metadata = stamped
	}
	return out, nil
}

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
// chunk with no project is dropped, and so is every such chunk when ctx
// carries no agent principal or authz is nil. The docs and workspace-wide
// documents pass. The error joins the checks that failed; their chunks
// are dropped too.
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
		if projectID == "" || callErr != nil || authz == nil {
			continue
		}
		ok, seen := allowed[projectID]
		if !seen {
			var err error
			ok, err = authz.AllowedOn(ctx, call.Principal, security.DeploymentRead, projectID)
			if err != nil {
				errs = append(errs, fmt.Errorf("agentknowledge: check %s on %s: %w", security.DeploymentRead.Name(), projectID, err))
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
