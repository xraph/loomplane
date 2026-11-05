package agentknowledge

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/xraph/weave"
	"github.com/xraph/weave/collection"
	"github.com/xraph/weave/document"
	weaveengine "github.com/xraph/weave/engine"
	"github.com/xraph/weave/id"

	"github.com/xraph/loomplane/internal/agentscope"
	"github.com/xraph/loomplane/internal/agentsettings"
	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/security"
)

// Where knowledge lives in weave. Every call carries a tenant: a
// workspace's own ID for its collection, DocsTenant for the docs, which
// every workspace may read and none may write.
const (
	// DocsTenant is the weave tenant of the shared docs. It is never a
	// workspace ID: those start "aorg_".
	DocsTenant = "loomplane-docs"
	// DocsCollection holds Loomplane's docs and the manifest schema.
	DocsCollection = "loomplane-docs"
	// WorkspaceCollection is the collection of each workspace's own
	// documents, in the workspace's tenant.
	WorkspaceCollection = "workspace"
	// App is the weave app ID on every collection.
	App = "loomplane"
)

// The kinds of document knowledge holds, in each document's "kind".
const (
	KindDocs    = "docs"
	KindReport  = "report"
	KindRunbook = "runbook"
)

// SystemOrg is the workspace-like name the docs' nexus tenant is made
// from ("loomplane-system"). Indexing the docs is billed to it, under
// ai.knowledge.docs_budget_usd, never to a workspace.
const SystemOrg = "system"

// stalledAfter is how long an ingest may go quiet before Recover fails
// its document, unless Deps.StalledAfter says otherwise.
const stalledAfter = 15 * time.Minute

// reasonDimensionMismatch is why knowledge turns off when the embedding
// model returns vectors of another size than configured.
const reasonDimensionMismatch = "The embedding model (ai.knowledge.embedding_model %s) returned vectors of a different size than ai.knowledge.dimensions (%d)."

// ErrInvalid is a document Ingest or Remove can't take: no workspace (or
// the docs' tenant), no source key, or a kind that isn't a report or a
// runbook.
var ErrInvalid = errors.New("invalid knowledge document")

// Document is one of a workspace's own documents, for its agent to search.
type Document struct {
	// SourceKey is unique in the workspace, for example "report/arep_123".
	// Ingesting the same key again replaces the document.
	SourceKey string
	Kind      string // KindReport or KindRunbook
	Title     string
	// Content is markdown the caller has already scanned on its way out.
	Content string
	// URL is the dashboard path to cite, for example
	// "/agent/reports/arep_123".
	URL       string
	ProjectID string
	Metadata  map[string]string
	// Untrusted marks a document whose facts came from outside Loomplane
	// or from the model, such as an agent report. It is stored as
	// metadata "trust": "untrusted".
	Untrusted bool
}

// TrustUntrusted is the "trust" metadata of an Untrusted document.
const TrustUntrusted = "untrusted"

// Outcome is what Ingest did with a document.
type Outcome string

// The outcomes of Ingest.
const (
	OutcomeIndexed   Outcome = "indexed"
	OutcomeUnchanged Outcome = "unchanged"
	// OutcomeSkipped: knowledge is off for the server or the workspace.
	OutcomeSkipped Outcome = "skipped"
	// OutcomePending: another ingest of the same content is still on it.
	// Nothing is indexed yet. If that ingest died, Recover fails its
	// document once it has been quiet for the stall window, and the
	// content can be ingested again.
	OutcomePending Outcome = "pending"
)

// DocsResult counts what IngestDocs did.
type DocsResult struct{ Indexed, Unchanged, Removed int }

// Engine is the part of weave's engine knowledge uses; *weaveengine.Engine
// is the real one.
type Engine interface {
	CreateCollection(ctx context.Context, col *collection.Collection) error
	GetCollectionByName(ctx context.Context, name string) (*collection.Collection, error)
	Ingest(ctx context.Context, in *weaveengine.IngestInput) (*weaveengine.IngestResult, error)
	DeleteBySource(ctx context.Context, colID id.CollectionID, sourceKey string) error
	ListDocuments(ctx context.Context, f *document.ListFilter) ([]*document.Document, error)
	RecoverStalled(ctx context.Context, stalledAfter time.Duration) (weaveengine.RecoveryResult, error)
}

var _ Engine = (*weaveengine.Engine)(nil)

// Settings is the part of the agent's settings knowledge reads;
// *agentsettings.Service is the real one.
type Settings interface {
	Effective(ctx context.Context, orgID string) (*agentsettings.Effective, error)
	CheckBudget(ctx context.Context, orgID string) error
	SetKnowledge(st agentsettings.KnowledgeStatus)
}

// Deps are what the Service is built from.
type Deps struct {
	// Engine returns weave's engine. A nil func, or a nil engine, means
	// knowledge isn't configured on this server.
	Engine   func() Engine
	Settings Settings
	Binder   *agentscope.Binder
	// System returns the nexus tenant indexing the docs is billed to
	// ("loomplane-system"), made on first use with
	// ai.knowledge.docs_budget_usd.
	System func(ctx context.Context) (string, error)
	Config config.AIKnowledgeConfig
	// Lock runs fn holding the advisory lock called name (pglock.For).
	Lock func(ctx context.Context, name string, fn func(context.Context) error) error
	// Docs returns the docs bundle (EmbeddedDocs).
	Docs func() (Bundle, error)
	// Schema is the manifest's JSON Schema (manifest.Schema), indexed as
	// source "schema/loomplane.json".
	Schema []byte
	// StalledAfter is how long an ingest may go quiet before Recover fails
	// its document. 0 is 15 minutes, weave's own default.
	StalledAfter time.Duration
	Warn         func(msg string, err error)
}

// Service indexes Loomplane's docs for every workspace and each
// workspace's own documents for its agent alone.
//
// Knowledge holds documents, never live state: deployments, plans and
// variables are read through the agent's operations when it needs them.
type Service struct {
	d Deps

	mu     sync.RWMutex
	status agentsettings.KnowledgeStatus
}

// NewService returns the service. Until SetStatus says otherwise,
// knowledge is unavailable and every call is skipped.
func NewService(d Deps) *Service {
	return &Service{d: d}
}

// SetStatus records whether knowledge is available on this server, and
// tells the agent's settings.
func (s *Service) SetStatus(st agentsettings.KnowledgeStatus) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
	if s.d.Settings != nil {
		s.d.Settings.SetKnowledge(st)
	}
}

// Status is whether knowledge is available on this server, and why not.
func (s *Service) Status() agentsettings.KnowledgeStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// engine is weave's engine while knowledge is available, or nil.
func (s *Service) engine() Engine {
	if !s.Status().Available || s.d.Engine == nil {
		return nil
	}
	return s.d.Engine()
}

// Ingest indexes one of orgID's documents in the workspace's collection,
// billed to the workspace. It is skipped while knowledge is off for the
// server or the workspace, and refused once the workspace has spent its
// budget.
func (s *Service) Ingest(ctx context.Context, orgID string, d Document) (Outcome, error) {
	if err := checkWorkspace(orgID); err != nil {
		return "", err
	}
	if strings.TrimSpace(d.SourceKey) == "" {
		return "", fmt.Errorf("%w: it has no source key", ErrInvalid)
	}
	if d.Kind != KindReport && d.Kind != KindRunbook {
		return "", fmt.Errorf("%w: kind %q isn't %s or %s", ErrInvalid, d.Kind, KindReport, KindRunbook)
	}
	eng, on, err := s.workspaceEngine(ctx, orgID)
	if err != nil || !on {
		return OutcomeSkipped, err
	}
	if budgetErr := s.d.Settings.CheckBudget(ctx, orgID); budgetErr != nil {
		return "", budgetErr
	}
	ctx, err = s.bind(ctx, orgID)
	if err != nil {
		return "", err
	}
	col, err := s.ensureCollection(ctx, eng, WorkspaceCollection, orgID)
	if err != nil {
		return "", err
	}
	// Weave copies a document's metadata onto each of its chunks, which
	// is what a search hands back, so the kind and project set here are
	// what agentknowledge.Readable decides on. They're set after the
	// caller's own keys, so a caller can't name another project.
	md := make(map[string]string, len(d.Metadata)+5)
	maps.Copy(md, d.Metadata)
	md["kind"], md["title"], md["url"], md["project_id"] = d.Kind, d.Title, d.URL, d.ProjectID
	if d.Untrusted {
		md["trust"] = TrustUntrusted
	}
	return s.ingest(ctx, eng, &weaveengine.IngestInput{
		CollectionID: col.ID, SourceKey: d.SourceKey, Title: d.Title, Source: d.URL,
		Content: d.Content, Metadata: md,
	})
}

// Remove deletes one of orgID's documents. A workspace with no documents
// yet has nothing to remove. It is skipped while knowledge is off for the
// server or the workspace.
func (s *Service) Remove(ctx context.Context, orgID, sourceKey string) error {
	if err := checkWorkspace(orgID); err != nil {
		return err
	}
	if strings.TrimSpace(sourceKey) == "" {
		return fmt.Errorf("%w: it has no source key", ErrInvalid)
	}
	eng, on, err := s.workspaceEngine(ctx, orgID)
	if err != nil || !on {
		return err
	}
	ctx, err = s.bind(ctx, orgID)
	if err != nil {
		return err
	}
	col, err := eng.GetCollectionByName(ctx, WorkspaceCollection)
	if errors.Is(err, weave.ErrCollectionNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agentknowledge: find the workspace's collection: %w", err)
	}
	return eng.DeleteBySource(ctx, col.ID, sourceKey)
}

// Recover fails the documents whose ingest went quiet for 15 minutes, in
// every tenant, so their source can be indexed again. It does nothing
// while knowledge is unavailable.
//
// A failed document may be a docs page whose run died with it: the next
// run, started within the stall window, found the page still processing
// and left it. So when the sweep failed anything, Recover indexes the docs
// again. Every page that is ready answers Unchanged before any embed, so
// only the pages that aren't are embedded.
func (s *Service) Recover(ctx context.Context) error {
	eng := s.engine()
	if eng == nil {
		return nil
	}
	after := s.d.StalledAfter
	if after <= 0 {
		after = stalledAfter
	}
	res, err := eng.RecoverStalled(ctx, after)
	if err != nil || res.Failed == 0 {
		return err
	}
	_, err = s.IngestDocs(ctx)
	return err
}

// checkWorkspace refuses an org ID that can't be a workspace's: none, or
// the docs' own tenant, so no workspace path ever writes the docs.
func checkWorkspace(orgID string) error {
	if strings.TrimSpace(orgID) == "" || orgID == DocsTenant {
		return fmt.Errorf("%w: %q isn't a workspace", ErrInvalid, orgID)
	}
	return nil
}

// workspaceEngine is weave's engine when knowledge is on for the server
// and for orgID. on is false, with no error, when it isn't, the agent
// being off in the workspace included.
func (s *Service) workspaceEngine(ctx context.Context, orgID string) (eng Engine, on bool, err error) {
	eng = s.engine()
	if eng == nil {
		return nil, false, nil
	}
	eff, err := s.d.Settings.Effective(ctx, orgID)
	if errors.Is(err, agentsettings.ErrDisabled) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !eff.Knowledge {
		return nil, false, nil
	}
	return eng, true, nil
}

// bind puts orgID's scope on ctx for every library, as the workspace's
// agent: weave's tenant and nexus's are the workspace's own.
func (s *Service) bind(ctx context.Context, orgID string) (context.Context, error) {
	return s.d.Binder.Bind(ctx, security.AgentPrincipal(orgID, nil), agentscope.Scope{OrgID: orgID})
}

// ensureCollection finds the collection called name in ctx's tenant, or
// makes it with the server's embedding model.
func (s *Service) ensureCollection(ctx context.Context, eng Engine, name, tenant string) (*collection.Collection, error) {
	col, err := eng.GetCollectionByName(ctx, name)
	if err == nil {
		return col, nil
	}
	if !errors.Is(err, weave.ErrCollectionNotFound) {
		return nil, fmt.Errorf("agentknowledge: find collection %s: %w", name, err)
	}
	col = &collection.Collection{
		Name: name, TenantID: tenant, AppID: App,
		EmbeddingModel: s.d.Config.EmbeddingModel, EmbeddingDims: s.d.Config.Dimensions,
	}
	err = eng.CreateCollection(ctx, col)
	if errors.Is(err, weave.ErrCollectionAlreadyExists) {
		// Another replica made it first.
		return eng.GetCollectionByName(ctx, name)
	}
	if err != nil {
		return nil, fmt.Errorf("agentknowledge: create collection %s: %w", name, err)
	}
	return col, nil
}

// ingest upserts in by its source key. weave.ErrDuplicateDocument (the
// same content already indexed) is unchanged. Vectors of the wrong size
// turn knowledge off.
func (s *Service) ingest(ctx context.Context, eng Engine, in *weaveengine.IngestInput) (Outcome, error) {
	res, err := eng.Ingest(ctx, in)
	switch {
	case errors.Is(err, weave.ErrDuplicateDocument):
		return OutcomeUnchanged, nil
	case errors.Is(err, weave.ErrDimensionMismatch):
		s.SetStatus(agentsettings.KnowledgeStatus{
			Configured: true, Available: false,
			Reason: fmt.Sprintf(reasonDimensionMismatch, s.d.Config.EmbeddingModel, s.d.Config.Dimensions),
		})
		s.warn("agentknowledge: the embedding model returned vectors of another size; knowledge is off", err)
		return "", err
	case err != nil:
		return "", err
	case res != nil && res.Unchanged:
		return OutcomeUnchanged, nil
	case res != nil && res.State != document.StateReady:
		// Another ingest of the same content is still on it, or was until
		// its process died. Not done: Recover sees to the second case.
		return OutcomePending, nil
	}
	return OutcomeIndexed, nil
}

func (s *Service) warn(msg string, err error) {
	if s.d.Warn != nil {
		s.d.Warn(msg, err)
	}
}
