package models

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

// Errors browsing returns. Test with errors.Is.
var (
	ErrEntryNotFound = errors.New("no such model")
	ErrInvalidQuery  = errors.New("invalid model query")
)

// FitsUnknown is what the page says when it couldn't read the workspace's
// GPU targets, so no entry says whether it fits here.
const FitsUnknown = "Couldn't check your GPUs."

// licenseOnHub is the note on a license the registry doesn't know.
const licenseOnHub = "Read the license on Hugging Face before you deploy."

// Hub reads models from Hugging Face. *hfhub.Client is the real one.
type Hub interface {
	Model(ctx context.Context, repo, revision, token string) (*hfhub.ModelInfo, error)
	RawFile(ctx context.Context, repo, revision, path, token string, limit int64) ([]byte, error)
	Access(ctx context.Context, repo, revision, token string) error
	AccessFile(ctx context.Context, repo, revision, path, token string) error
}

// GPUTargets lists where GPUs can run. *gpus.Service is the real one.
type GPUTargets interface {
	Targets(ctx context.Context, p security.Principal, q gpus.GPUTargetQuery) (*gpus.GPUTargetList, error)
}

// ConnectionLister lists the workspace's connections, to tell whether a
// CPU model has a server or cluster to run on. *connections.Service is the
// real one.
type ConnectionLister interface {
	List(ctx context.Context, p security.Principal) ([]*connections.Connection, error)
}

// Authorizer decides whether a principal may act. *security.Authorizer is
// the real one. Require is for acting, and may repair a member's roles;
// Allowed only answers, for what a page offers, and never repairs.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
	Allowed(ctx context.Context, p security.Principal, perm security.Permission) (bool, error)
}

// Deps are what the models service reads.
type Deps struct {
	// Registry is the model registry; nil is registry.Default().
	Registry *registry.Registry
	Hub      Hub
	// Settings reads the workspace's Hugging Face token, for gated repos.
	// It's the only way the service gets one.
	Settings    Tokens // *aisettings.Service
	Custom      CustomStore
	GPUs        GPUTargets
	Connections ConnectionLister
	Authz       Authorizer
	Now         func() time.Time

	// What deploying needs. Browsing works without them.

	// Store keeps the model deployments.
	Store Store
	// Manifests and Plans edit a project's manifest and plan an
	// environment, as the principal who deploys.
	Manifests Manifests
	Plans     Planner
	Projects  Projects
	// TokenGuard is *aisettings.Service, beside Settings: a gated model's
	// row is written under the workspace's token lock.
	TokenGuard TokenGuard
	// Audit may be nil.
	Audit Auditor

	// What creating a GPU server first needs. Without them a server size
	// target is a problem.

	// Servers previews, approves and reads GPU servers, as the principal
	// who deploys.
	Servers Servers
	// Clouds says which cloud an environment deploys to.
	Clouds Clouds
	// Waiting lists the rows the reconcile cron carries on, across
	// organizations. Only Reconcile reads it.
	Waiting WaitingLister
	// Principals rechecks an API key behind a stored principal before the
	// cron acts as it. Nil refuses every key.
	Principals security.PrincipalVerifier
	// Warn logs what the cron does that someone should know about, such as
	// a GPU server left behind by a deleted environment. It may be nil.
	Warn func(msg string, err error)

	// What a model's status and endpoints read.

	// Deployed reads what deployments left behind: state, bindings, the
	// guard's address and the model's key.
	Deployed Deployed
	// Gateways is told when a project's models change. Nil is noGateways
	// until SetGateways binds one.
	Gateways Gateways

	// What the accrual cron reads and sends.

	// Prices reads what an organization's connected GPUs cost, with no
	// principal. Without it no GPU model's cost is known to the cron.
	Prices GPUPrices
	// Budget queues the budget notices admins get. Nil sends none.
	Budget BudgetNotices
}

// Service is the model registry as a workspace sees it: what it can
// browse, where each model fits, and models pasted from Hugging Face.
type Service struct {
	d Deps

	catalogOnce sync.Once
	catalog     []entryView

	cards     *ttlCache[cardKey, *Card]
	inspected *ttlCache[customKey, registry.Entry]

	gatewaysMu sync.RWMutex
	gateways   Gateways
}

// How long a model card and an inspected repo are kept.
const (
	cardTTL    = 24 * time.Hour
	inspectTTL = time.Hour
	cacheItems = 512
)

// NewService returns the models service.
func NewService(d Deps) *Service {
	if d.Registry == nil {
		d.Registry = registry.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	s := &Service{
		d:         d,
		cards:     newTTLCache[cardKey, *Card](cardTTL, cacheItems, d.Now),
		inspected: newTTLCache[customKey, registry.Entry](inspectTTL, cacheItems, d.Now),
	}
	s.SetGateways(d.Gateways)
	return s
}

// Size buckets of RegistryQuery.Size, by parameter count.
const (
	SizeTiny   = "tiny"   // under 4B
	SizeSmall  = "small"  // 4B to 15B
	SizeMedium = "medium" // 15B to 40B
	SizeLarge  = "large"  // 40B to 100B
	SizeHuge   = "huge"   // over 100B
)

// RegistryQuery filters the registry. Every field left empty matches all.
type RegistryQuery struct {
	Q          string `query:"q"`
	Task       string `query:"task"`       // one registry.Task
	Size       string `query:"size"`       // one of the Size constants
	License    string `query:"license"`    // a license ID
	Commercial bool   `query:"commercial"` // only commercially usable
	Fits       bool   `query:"fits"`       // fits a GPU or CPU target this workspace has or can create
	Tested     bool   `query:"tested"`
}

// EntrySummary is one model as the registry lists it.
type EntrySummary struct {
	ID            string               `json:"id"`
	Title         string               `json:"title"`
	Publisher     string               `json:"publisher"`
	Summary       string               `json:"summary"`
	Family        string               `json:"family"`
	Tasks         []registry.Task      `json:"tasks"`
	Parameters    int64                `json:"parameters"`
	ContextLength int                  `json:"context_length"`
	License       registry.LicenseInfo `json:"license"`
	Gated         bool                 `json:"gated"`
	// Verification is the best across its configurations: tested, then
	// estimated. A model pasted from Hugging Face is always untested.
	Verification registry.Status `json:"verification"`
	// TestedOn are the GPU kinds (titles, "CPU" for CPU) a qualification run
	// served it on, with the engine Loomplane ships.
	TestedOn []string `json:"tested_on"`
	// MinGPUMemoryGiB is the smallest across its vLLM quantizations; 0 when
	// it has none.
	MinGPUMemoryGiB int  `json:"min_gpu_memory_gib"`
	CPU             bool `json:"cpu"` // has a quantization that runs on CPU
	// FitsHere is set when it was asked for and the workspace's targets
	// could be read: whether it fits a GPU or CPU target the workspace has
	// or can create.
	FitsHere *bool `json:"fits_here,omitempty"`
	// QueryPrefix is what an embedding model wants in front of each query,
	// such as "query: ". Empty when it wants none, or isn't an embedding
	// model.
	QueryPrefix string `json:"query_prefix,omitempty"`
}

// RegistryList is the registry as one query sees it.
type RegistryList struct {
	Entries []EntrySummary `json:"entries"`
	// Counts are entries by task, for the filter chips: every filter
	// applies except the task.
	Counts map[string]int `json:"counts"`
	// FitsNotice is FitsUnknown when fits were asked for and couldn't be
	// checked. The list is then not filtered by fit.
	FitsNotice string `json:"fits_notice,omitempty"`
}

// QuantizationView is one quantization and where it fits.
type QuantizationView struct {
	registry.Quantization
	// Fits has one per P1 NVIDIA kind for the GPU engines, smallest count
	// up to 8, and "cpu" for the CPU engines.
	Fits []registry.Fit `json:"fits"`
}

// EntryDetail is one model with everything the model page shows.
type EntryDetail struct {
	EntrySummary
	Revision       string             `json:"revision"`
	Quantizations  []QuantizationView `json:"quantizations"`
	HuggingFaceURL string             `json:"hugging_face_url"`
	FitsNotice     string             `json:"fits_notice,omitempty"`
}

// entryView is an entry with what never changes for it: its summary
// (without FitsHere) and its quantizations' fits.
type entryView struct {
	entry   registry.Entry
	summary EntrySummary
	quants  []QuantizationView
}

// views works out the registry's entries once; the registry is built into
// the binary and its fits depend on nothing else.
func (s *Service) views() []entryView {
	s.catalogOnce.Do(func() {
		for _, e := range s.d.Registry.Entries() {
			s.catalog = append(s.catalog, s.view(e))
		}
	})
	return s.catalog
}

// Registry lists the registry's entries that match q. ai:read.
func (s *Service) Registry(ctx context.Context, p security.Principal, q RegistryQuery) (*RegistryList, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	if err := q.check(); err != nil {
		return nil, err
	}
	out := &RegistryList{Entries: []EntrySummary{}, Counts: map[string]int{}}
	var where *places
	if q.Fits {
		var err error
		if where, err = s.places(ctx, p); err != nil {
			out.FitsNotice = FitsUnknown
		}
	}
	for _, v := range s.views() {
		sum := v.summary
		if where != nil {
			fits := s.fitsHere(v, where)
			sum.FitsHere = &fits
		}
		if !q.matches(sum) {
			continue
		}
		for _, t := range sum.Tasks {
			out.Counts[string(t)]++
		}
		if q.Task == "" || slices.Contains(sum.Tasks, registry.Task(q.Task)) {
			out.Entries = append(out.Entries, sum)
		}
	}
	return out, nil
}

// check refuses a task or size the registry doesn't have.
func (q RegistryQuery) check() error {
	switch registry.Task(q.Task) {
	case "", registry.TaskChat, registry.TaskReasoning, registry.TaskCode, registry.TaskVision,
		registry.TaskEmbedding, registry.TaskRerank, registry.TaskSpeech:
	default:
		return fmt.Errorf("%w: task %q isn't one Loomplane serves", ErrInvalidQuery, q.Task)
	}
	switch q.Size {
	case "", SizeTiny, SizeSmall, SizeMedium, SizeLarge, SizeHuge:
	default:
		return fmt.Errorf("%w: size is tiny, small, medium, large or huge, not %q", ErrInvalidQuery, q.Size)
	}
	return nil
}

// matches applies every filter but the task. A fit filter applies only
// when the fit is known.
func (q RegistryQuery) matches(sum EntrySummary) bool {
	if text := strings.ToLower(strings.TrimSpace(q.Q)); text != "" {
		found := false
		for _, field := range []string{sum.ID, sum.Title, sum.Publisher, sum.Family} {
			if strings.Contains(strings.ToLower(field), text) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	switch {
	case q.Size != "" && sizeOf(sum.Parameters) != q.Size,
		q.License != "" && sum.License.ID != q.License,
		q.Commercial && !sum.License.Commercial,
		q.Tested && sum.Verification != registry.StatusTested,
		q.Fits && sum.FitsHere != nil && !*sum.FitsHere:
		return false
	}
	return true
}

// sizeOf buckets a parameter count.
func sizeOf(params int64) string {
	const billion = 1_000_000_000
	switch {
	case params < 4*billion:
		return SizeTiny
	case params < 15*billion:
		return SizeSmall
	case params < 40*billion:
		return SizeMedium
	case params <= 100*billion:
		return SizeLarge
	default:
		return SizeHuge
	}
}

// Entry returns one model: a registry entry, or with an "hf:" ID a model
// pasted from Hugging Face that the workspace deployed or inspected in the
// last hour. ai:read.
func (s *Service) Entry(ctx context.Context, p security.Principal, id string) (*EntryDetail, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	v, err := s.lookup(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	return s.detail(ctx, p, v), nil
}

// lookup finds a registry entry or one of the workspace's custom entries.
func (s *Service) lookup(ctx context.Context, orgID, id string) (entryView, error) {
	if strings.HasPrefix(id, customPrefix) {
		e, err := s.customEntry(ctx, orgID, id)
		if err != nil {
			return entryView{}, err
		}
		return s.view(e), nil
	}
	for _, v := range s.views() {
		if v.entry.ID == id {
			return v, nil
		}
	}
	return entryView{}, fmt.Errorf("%w: the model registry has no %s", ErrEntryNotFound, id)
}

// detail adds where the model fits this workspace to its view.
func (s *Service) detail(ctx context.Context, p security.Principal, v entryView) *EntryDetail {
	d := &EntryDetail{
		EntrySummary:   v.summary,
		Revision:       v.entry.Revision,
		Quantizations:  v.quants,
		HuggingFaceURL: hfhub.DefaultURL + "/" + repoOf(v.entry),
	}
	where, err := s.places(ctx, p)
	if err != nil {
		d.FitsNotice = FitsUnknown
		return d
	}
	fits := s.fitsHere(v, where)
	d.FitsHere = &fits
	return d
}

// repoOf is the Hugging Face repo of an entry: its ID, without "hf:" and
// "@<sha12>" for a custom one.
func repoOf(e registry.Entry) string {
	if rest, ok := strings.CutPrefix(e.ID, customPrefix); ok {
		repo, _, _ := strings.Cut(rest, "@")
		return repo
	}
	return e.ID
}

// customPrefix starts a custom entry's ID, as engines.CustomEntryID writes
// it.
const customPrefix = "hf:"

// view works out an entry's summary and fits.
func (s *Service) view(e registry.Entry) entryView {
	_, listed := s.d.Registry.Entry(e.ID)
	v := entryView{entry: e, summary: EntrySummary{
		ID: e.ID, Title: e.Title, Publisher: e.Publisher, Summary: e.Summary, Family: e.Family,
		Tasks: slices.Clone(e.Tasks), Parameters: e.Parameters, ContextLength: e.ContextLength,
		License: licenseOf(e), Gated: e.Gated, Verification: registry.StatusEstimated, TestedOn: []string{},
	}}
	if e.Embedding != nil {
		v.summary.QueryPrefix = e.Embedding.QueryPrefix
	}
	if !listed {
		v.summary.Verification = registry.StatusUntested
	}
	tested := map[string]bool{}
	for _, q := range e.Quantizations {
		qv := QuantizationView{Quantization: q, Fits: s.fitsOf(e, q)}
		for _, f := range qv.Fits {
			if listed && f.Status == registry.StatusTested {
				v.summary.Verification = registry.StatusTested
				tested[f.GPUKind] = true
			}
		}
		if registry.IsGPU(q) {
			need := q.MinGPUMemoryGiB
			if need <= 0 {
				need = registry.MinGPUMemoryGiB(e, q)
			}
			if v.summary.MinGPUMemoryGiB == 0 || need < v.summary.MinGPUMemoryGiB {
				v.summary.MinGPUMemoryGiB = need
			}
		}
		if runsOnCPU(q) {
			v.summary.CPU = true
		}
		v.quants = append(v.quants, qv)
	}
	for _, k := range nvidiaKinds() {
		if tested[k.ID] {
			v.summary.TestedOn = append(v.summary.TestedOn, k.Title)
		}
	}
	if tested[registry.CPUKind] {
		v.summary.TestedOn = append(v.summary.TestedOn, "CPU")
	}
	return v
}

// licenseOf describes an entry's license. One the registry doesn't know,
// as a pasted repo's may be, is never commercial and says to read it.
func licenseOf(e registry.Entry) registry.LicenseInfo {
	for _, l := range registry.Licenses() {
		if l.ID == e.License.ID {
			return l
		}
	}
	id := e.License.ID
	if id == "" {
		id = "unknown"
	}
	return registry.LicenseInfo{ID: id, Title: id, URL: hfhub.DefaultURL + "/" + repoOf(e), Note: licenseOnHub}
}

// maxGPUs is the most GPUs a fit may take on one kind.
const maxGPUs = 8

// fitsOf is q's fit on every P1 NVIDIA kind when its engine runs on a GPU,
// and on CPU when it runs there.
func (s *Service) fitsOf(e registry.Entry, q registry.Quantization) []registry.Fit {
	var out []registry.Fit
	if q.Engine != registry.EngineTEI {
		for _, k := range nvidiaKinds() {
			out = append(out, s.d.Registry.FitOn(e, q, k.ID, maxGPUs, engines.Current()))
		}
	}
	if runsOnCPU(q) {
		out = append(out, s.cpuFit(e, q))
	}
	return out
}

// cpuFit is q's fit on CPU, which for llama.cpp also needs its weights to
// fit the fixed cache the CPU kinds get on Kubernetes.
func (s *Service) cpuFit(e registry.Entry, q registry.Quantization) registry.Fit {
	if reason := CPUCacheProblem(q); reason != "" {
		need, _, _ := registry.CPUFits(e, q)
		return registry.Fit{GPUKind: registry.CPUKind, Need: need, Status: registry.StatusNoFit, Reason: reason}
	}
	return s.d.Registry.FitOn(e, q, registry.CPUKind, 0, engines.Current())
}

// runsOnCPU reports whether q's engine runs on CPU: TEI, and llama.cpp.
func runsOnCPU(q registry.Quantization) bool {
	return q.Engine == registry.EngineTEI || q.Engine == registry.EngineLlamaCPP
}

// nvidiaKinds are P1's NVIDIA GPU kinds, in its order.
func nvidiaKinds() []provider.AcceleratorKind {
	var out []provider.AcceleratorKind
	for _, k := range provider.AcceleratorKinds() {
		if k.Vendor == provider.VendorNVIDIA {
			out = append(out, k)
		}
	}
	return out
}

// places is where the workspace can run a model: the GPU target rows that
// can take one GPU now, and whether it has a ready server or cluster for
// CPU engines.
type places struct {
	gpus []gpus.GPUTarget
	cpu  bool
}

// places reads the workspace's targets once for a request. Targets needs
// infrastructure:read and cloud_account:read, so a member without them
// gets an error, and the page says it couldn't check.
func (s *Service) places(ctx context.Context, p security.Principal) (*places, error) {
	if s.d.GPUs == nil || s.d.Connections == nil {
		return nil, errors.New("models: GPU targets aren't configured")
	}
	list, err := s.d.GPUs.Targets(ctx, p, gpus.GPUTargetQuery{Count: 1})
	if err != nil {
		return nil, err
	}
	conns, err := s.d.Connections.List(ctx, p)
	if err != nil {
		return nil, err
	}
	where := &places{}
	for _, t := range list.Targets {
		if t.Fits && t.Accelerator.Kind != "" && t.Accelerator.Count > 0 {
			where.gpus = append(where.gpus, t)
		}
	}
	for _, c := range conns {
		if (c.Cloud == connections.Server || c.Cloud == connections.Kubernetes) && c.Status == connections.StatusReady {
			where.cpu = true
			break
		}
	}
	return where, nil
}

// fitsHere reports whether any quantization fits any target the
// workspace has or can create: on a GPU row's kind with up to its count of
// GPUs, or on CPU when there's a ready server or cluster. A fit a
// qualification run failed never counts.
func (s *Service) fitsHere(v entryView, where *places) bool {
	for _, q := range v.entry.Quantizations {
		if where.cpu && runsOnCPU(q) && s.cpuFit(v.entry, q).Fits() {
			return true
		}
		if q.Engine == registry.EngineTEI {
			continue
		}
		for _, t := range where.gpus {
			if s.d.Registry.FitOn(v.entry, q, t.Accelerator.Kind, t.Accelerator.Count, engines.Current()).Fits() {
				return true
			}
		}
	}
	return false
}

// Card is an entry's model card, the README at its pinned revision.
type Card struct {
	Markdown  string `json:"markdown"`
	SourceURL string `json:"source_url"`
	// Note says why Markdown is empty: the card is too long to show, or
	// the repo has none.
	Note string `json:"note,omitempty"`
}

// maxCardBytes is the longest model card shown.
const maxCardBytes = 256 << 10

type cardKey struct{ org, id string }

// Card returns an entry's README at its pinned revision, without its front
// matter. Cards are kept for a day per workspace. ai:read.
func (s *Service) Card(ctx context.Context, p security.Principal, id string) (*Card, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	key := cardKey{p.OrgID, id}
	if c, ok := s.cards.get(key); ok {
		return c, nil
	}
	v, err := s.lookup(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if s.d.Hub == nil {
		return nil, errors.New("models: Hugging Face isn't configured")
	}
	repo, revision := repoOf(v.entry), v.entry.Revision
	card := &Card{SourceURL: hfhub.DefaultURL + "/" + repo + "/blob/" + revision + "/README.md"}
	body, err := s.d.Hub.RawFile(ctx, repo, revision, "README.md", "", maxCardBytes)
	// A custom entry is marked gated when it took the workspace's token to
	// read, and a private repo answers an anonymous request as if it had no
	// card. Ask again with the token.
	private := strings.HasPrefix(v.entry.ID, customPrefix) && v.entry.Gated && errors.Is(err, hfhub.ErrNotFound)
	tokenRetry := false
	if errors.Is(err, hfhub.ErrGated) || private {
		// Some gated repos keep even the card behind the license.
		token, terr := s.token(ctx, p.OrgID)
		if terr != nil {
			return nil, terr
		}
		switch {
		case token != "":
			body, err = s.d.Hub.RawFile(ctx, repo, revision, "README.md", token, maxCardBytes)
		case private:
			// The card may be there for a token added later, so this
			// answer isn't kept.
			tokenRetry = true
		default:
			return nil, &GatedError{Title: v.entry.Title}
		}
	}
	switch {
	case errors.Is(err, hfhub.ErrNotFound):
		card.Note = repo + " has no model card."
		if tokenRetry {
			return card, nil
		}
	case errors.Is(err, hfhub.ErrTooLarge):
		card.Note = "This model card is longer than Loomplane shows. Read it on Hugging Face."
	case errors.Is(err, hfhub.ErrGated):
		return nil, gatedAccessError(repo)
	case err != nil:
		return nil, fmt.Errorf("read %s's model card: %w", repo, err)
	default:
		card.Markdown = stripFrontMatter(string(body))
	}
	s.cards.put(key, card)
	return card, nil
}

// stripFrontMatter drops a README's leading YAML block, between "---"
// lines, which Hugging Face reads and doesn't show.
func stripFrontMatter(md string) string {
	md = strings.TrimPrefix(md, "\ufeff")
	first, rest, ok := strings.Cut(md, "\n")
	if !ok || strings.TrimSpace(first) != "---" {
		return md
	}
	for {
		line, after, more := strings.Cut(rest, "\n")
		if strings.TrimSpace(line) == "---" {
			return strings.TrimLeft(after, "\r\n")
		}
		if !more {
			// No closing line: it wasn't front matter.
			return md
		}
		rest = after
	}
}

// token reads the workspace's Hugging Face token through AI settings, ""
// when none is set. Nothing else gives the service one.
func (s *Service) token(ctx context.Context, orgID string) (string, error) {
	if s.d.Settings == nil {
		return "", nil
	}
	value, _, err := s.d.Settings.HuggingFaceTokenValue(ctx, orgID)
	switch {
	case errors.Is(err, errNoToken):
		return "", nil
	case err != nil:
		// The error comes from reading the value, so it could quote it.
		return "", errors.New("couldn't read the workspace's Hugging Face token. Try again")
	}
	return value, nil
}

// ttlCache keeps up to limit values for ttl each. When it's full, expired
// values go first, then the one closest to expiring.
type ttlCache[K comparable, V any] struct {
	mu    sync.Mutex
	ttl   time.Duration
	limit int
	now   func() time.Time
	items map[K]ttlItem[V]
}

type ttlItem[V any] struct {
	value   V
	expires time.Time
}

func newTTLCache[K comparable, V any](ttl time.Duration, maxItems int, now func() time.Time) *ttlCache[K, V] {
	return &ttlCache[K, V]{ttl: ttl, limit: maxItems, now: now, items: map[K]ttlItem[V]{}}
}

func (c *ttlCache[K, V]) get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it, ok := c.items[k]
	if !ok || !c.now().Before(it.expires) {
		delete(c.items, k)
		var zero V
		return zero, false
	}
	return it.value, true
}

func (c *ttlCache[K, V]) put(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if _, ok := c.items[k]; !ok && len(c.items) >= c.limit {
		var soonest K
		var at time.Time
		for key, it := range c.items {
			if !now.Before(it.expires) {
				delete(c.items, key)
				continue
			}
			if at.IsZero() || it.expires.Before(at) {
				soonest, at = key, it.expires
			}
		}
		if len(c.items) >= c.limit {
			delete(c.items, soonest)
		}
	}
	c.items[k] = ttlItem[V]{value: v, expires: now.Add(c.ttl)}
}
