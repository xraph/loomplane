package agentknowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/nexus/pipeline"
	"github.com/xraph/weave"
	"github.com/xraph/weave/document"
	weaveengine "github.com/xraph/weave/engine"
	"github.com/xraph/weave/id"
)

// docsLock is the advisory lock IngestDocs holds, so replicas that start
// together index the docs one at a time and embed each page once.
const docsLock = "agent-knowledge-docs"

// The source keys of the docs collection.
const (
	docsPrefix   = "docs/"
	schemaPrefix = "schema/"
	schemaKey    = schemaPrefix + "loomplane.json"
)

// IngestDocs indexes the docs bundle and the manifest schema in the docs
// collection, under DocsTenant and billed to the system tenant, then
// deletes every page the bundle no longer has. A page whose content is
// unchanged is not embedded again, so a reboot of the same release embeds
// nothing. A page another run is still on counts in no field of the
// result. It does nothing while knowledge is unavailable.
//
// An embedding model that returns vectors of another size turns knowledge
// off with the reason, and the error is returned.
func (s *Service) IngestDocs(ctx context.Context) (DocsResult, error) {
	if s.engine() == nil {
		return DocsResult{}, nil
	}
	var res DocsResult
	err := s.d.Lock(ctx, docsLock, func(ctx context.Context) error {
		var err error
		res, err = s.ingestDocs(ctx)
		return err
	})
	return res, err
}

func (s *Service) ingestDocs(ctx context.Context) (DocsResult, error) {
	var res DocsResult
	eng := s.engine()
	if eng == nil {
		// Knowledge turned off while this waited for the lock.
		return res, nil
	}
	bundle, err := s.d.Docs()
	if err != nil {
		return res, err
	}
	system, err := s.d.System(ctx)
	if err != nil {
		return res, fmt.Errorf("agentknowledge: the docs' nexus tenant: %w", err)
	}
	ctx = docsContext(ctx, system)
	col, err := s.ensureCollection(ctx, eng, DocsCollection, DocsTenant)
	if err != nil {
		return res, err
	}

	inputs := make([]*weaveengine.IngestInput, 0, len(bundle.Pages)+1)
	for _, p := range bundle.Pages {
		inputs = append(inputs, pageInput(col.ID, p))
	}
	if len(s.d.Schema) > 0 {
		inputs = append(inputs, schemaInput(col.ID, s.d.Schema))
	}
	// A page that fails doesn't stop the others, nor the pruning of pages
	// the bundle dropped. The errors come back joined, so the job retries.
	var errs []error
	pending := 0
	keep := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		keep[in.SourceKey] = true
		out, ierr := s.ingest(ctx, eng, in)
		if ierr != nil {
			errs = append(errs, fmt.Errorf("agentknowledge: index %s: %w", in.SourceKey, ierr))
			if !s.Status().Available {
				// Vectors of the wrong size: every other page would cost
				// an embed to fail the same way.
				return res, errors.Join(errs...)
			}
			continue
		}
		switch out {
		case OutcomeIndexed:
			res.Indexed++
		case OutcomeUnchanged:
			res.Unchanged++
		case OutcomePending:
			pending++
		}
	}
	if pending > 0 {
		s.warn("agentknowledge: some docs pages are still being indexed by another run; recovery indexes them if that run died",
			fmt.Errorf("%d pages pending", pending))
	}

	docs, err := eng.ListDocuments(ctx, &document.ListFilter{CollectionID: col.ID})
	if err != nil {
		errs = append(errs, fmt.Errorf("agentknowledge: list the docs: %w", err))
		return res, errors.Join(errs...)
	}
	for _, d := range docs {
		key := d.SourceKey
		if keep[key] || (!strings.HasPrefix(key, docsPrefix) && !strings.HasPrefix(key, schemaPrefix)) {
			continue
		}
		keep[key] = true // a source with several versions goes once
		if derr := eng.DeleteBySource(ctx, col.ID, key); derr != nil {
			errs = append(errs, fmt.Errorf("agentknowledge: remove %s: %w", key, derr))
			continue
		}
		res.Removed++
	}
	return res, errors.Join(errs...)
}

// pageInput is a docs page as weave ingests it.
func pageInput(colID id.CollectionID, p DocPage) *weaveengine.IngestInput {
	key := docsPrefix + p.Path
	if p.Path == "" {
		key = docsPrefix + "index"
	}
	parts := []string{"# " + p.Title}
	if p.Description != "" {
		parts = append(parts, p.Description)
	}
	if p.Text != "" {
		parts = append(parts, p.Text)
	}
	return &weaveengine.IngestInput{
		CollectionID: colID, SourceKey: key, Title: p.Title, Source: p.URL,
		Content:  strings.Join(parts, "\n\n"),
		Metadata: map[string]string{"kind": KindDocs, "title": p.Title, "url": p.URL},
	}
}

// schemaTitle and schemaURL describe the manifest schema in the docs
// collection; the URL is the reference page for loomplane.yaml.
const (
	schemaTitle = "loomplane.yaml JSON Schema"
	schemaURL   = DocsBaseURL + "/reference/loomplane-yaml"
)

// schemaInput is the manifest's JSON Schema as weave ingests it.
func schemaInput(colID id.CollectionID, schema []byte) *weaveengine.IngestInput {
	content := "# " + schemaTitle + "\n\nThe JSON Schema that loomplane.yaml is checked against in editors.\n\n```json\n" +
		strings.TrimSpace(string(schema)) + "\n```"
	return &weaveengine.IngestInput{
		CollectionID: colID, SourceKey: schemaKey, Title: schemaTitle, Source: schemaURL,
		Content:  content,
		Metadata: map[string]string{"kind": KindDocs, "title": schemaTitle, "url": schemaURL},
	}
}

// docsContext is ctx in the docs' weave tenant, billed to the system's
// nexus tenant. A nexus tenant on ctx wins over weave's tenant mapper.
func docsContext(ctx context.Context, system string) context.Context {
	return pipeline.WithTenantID(weave.WithTenant(ctx, DocsTenant), system)
}
