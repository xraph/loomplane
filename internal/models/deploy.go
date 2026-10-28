package models

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/gpus"
	"github.com/xraph/loomplane/internal/manifests"
	"github.com/xraph/loomplane/internal/models/engines"
	"github.com/xraph/loomplane/internal/models/hfhub"
	"github.com/xraph/loomplane/internal/models/registry"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// Sources of a TargetChoice.
const (
	TargetConnection = "connection"  // a server or cluster the workspace has
	TargetServerSize = "server_size" // a GPU server Loomplane creates first
)

// TargetChoice is where a model is deployed: a server or cluster the
// workspace has, with the GPUs it asks for there, or a GPU server size to
// create first.
type TargetChoice struct {
	Source string `json:"source"` // TargetConnection or TargetServerSize
	// ConnectionID is the server or cluster, or for a server size the
	// provider account that creates it.
	ConnectionID string `json:"connection_id"`
	// Provider is "server" or "kubernetes" for a connection. Preview fills
	// it in from the connection; what's sent is ignored.
	Provider string `json:"provider,omitempty"`
	// GPU is the GPU kind to run on, nil for a CPU engine. Its Count may
	// raise the GPU count the model needs, never lower it.
	GPU    *provider.GPURequest `json:"gpu,omitempty"`
	Region string               `json:"region,omitempty"`  // server size only
	SizeID string               `json:"size_id,omitempty"` // server size only
}

// Reach says how the services of one environment reach the model. Each
// environment the model runs in runs its own copy, on its own target, and
// a service links only to its own environment's copy.
type Reach struct {
	EnvironmentID string `json:"environment_id"`
	Environment   string `json:"environment"`
	Mode          string `json:"mode"` // ReachHome
	Message       string `json:"message,omitempty"`
}

// ReachHome is the environment the model runs in.
const ReachHome = "home"

// Problem is why a deploy can't go ahead, as a code and a sentence the
// page shows as it is.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Problem codes.
const (
	ProblemNoFit        = "model-no-fit"
	ProblemContext      = "model-context"
	ProblemGated        = "model-gated"
	ProblemNameTaken    = "model-name-taken"
	ProblemName         = "model-name"
	ProblemExists       = "model-exists"
	ProblemTarget       = "model-target"
	ProblemQuantization = "model-quantization"
	ProblemBudget       = "model-budget"
	ProblemNoManifest   = "model-no-manifest"
	ProblemEngine       = "model-engine"
	// ProblemNoCloud: the environment's cloud doesn't have the provider
	// account a GPU server would be created with.
	ProblemNoCloud = "model-no-cloud"
	// ProblemServerAdmin: only an admin creates GPU servers.
	ProblemServerAdmin = "model-server-needs-admin"
	// ProblemServerReview: the GPU server's review is missing, expired or
	// for another server.
	ProblemServerReview = "model-server-review"
	// ProblemCapacity: the cloud's capacity policy refuses the server.
	ProblemCapacity = "model-capacity"
)

// DeployPreview is what a deploy would do, and why it can't when it
// can't. Preview writes nothing.
type DeployPreview struct {
	Entry        EntrySummary `json:"entry"`
	Quantization string       `json:"quantization"`
	// Kind is the model kind the resource gets, and Version its release.
	Kind    string       `json:"kind"`
	Version string       `json:"version"`
	Target  TargetChoice `json:"target"`
	// TargetName is the server or cluster's name, for "Runs on CPU. Its
	// cost is part of <server>."
	TargetName string               `json:"target_name"`
	GPU        *provider.GPURequest `json:"gpu,omitempty"`
	Fit        registry.Fit         `json:"fit"`
	// MaxModelLen is the context the model is served at.
	MaxModelLen int    `json:"max_model_len"`
	Resource    string `json:"resource"`
	// AddsTo is the manifest resource this deploy adds the environment
	// to, the same model already running in other environments, or "" for
	// a new resource.
	AddsTo string `json:"adds_to,omitempty"`
	// Cost is what its GPUs cost per hour. Source "cpu" is never free: the
	// model's cost is part of TargetName's.
	Cost         Cost      `json:"cost"`
	Environments []Reach   `json:"environments"`
	Gated        bool      `json:"gated"`
	TokenReady   bool      `json:"token_ready"`
	Problems     []Problem `json:"problems"`
	// CanDeploy is true when there are no problems and the caller may
	// write this environment's models.
	CanDeploy bool `json:"can_deploy"`
	// Server is the review of the GPU server a server size target creates
	// first, with its hourly price and GPUs. Deploy approves it: send its
	// cloud_id, id and review back as DeployInput.Server.
	//
	// The contract for a server size, for the API and the deploy dialog:
	// a plain Preview prices the server from the size's list price, writes
	// nothing and sets ServerReviewNeeded. The "Review server" step sends
	// the same input with review_server true; that Preview asks the
	// provider, records the review operation and returns it here, priced
	// from the provider, with CanDeploy. Deploy then sends the input with
	// server set from it, within five minutes, since cloudinfra accepts a
	// review only while its capacity reading is fresh.
	Server *cloudinfra.Operation `json:"server,omitempty"`
	// ServerReviewNeeded is true for a server size previewed without a
	// review: CanDeploy waits for the review.
	ServerReviewNeeded bool `json:"server_review_needed,omitempty"`

	env       *projects.Environment
	entry     registry.Entry
	quant     registry.Quantization
	spec      engines.Spec
	budgetUSD string
	// joinKind is the kind of the resource the deploy adds the environment
	// to, and servedName its served_name setting.
	joinKind, servedName string
}

// DeployResult is the deployment a deploy recorded, and the plan it made.
type DeployResult struct {
	Deployment *Deployment `json:"deployment"`
	PlanID     string      `json:"plan_id,omitempty"`
	// Message says what the change leaves behind, such as the weights a
	// removed model keeps on its server.
	Message string `json:"message,omitempty"`
}

// ProblemError is a deploy refused for a Problem. It matches ErrInvalid,
// and its text is the problem's sentence.
type ProblemError struct{ Problem Problem }

func (e *ProblemError) Error() string { return e.Problem.Message }
func (e *ProblemError) Unwrap() error { return ErrInvalid }

// PlanAfterEditError is a deploy whose manifest edit went through and
// whose plan didn't. The deployment row stays, and the manifest has the
// resource, so planning the environment from the project deploys it.
type PlanAfterEditError struct {
	Resource, Environment string
	// Repin is true for MoveToPin's edit, Move for Move's and Removal for
	// Remove's.
	Repin, Move, Removal bool
	err                  error
}

func (e *PlanAfterEditError) Error() string {
	switch {
	case e.Removal:
		return fmt.Sprintf("The manifest no longer has %s. Plan %s from the project to remove it. Planning failed: %v", e.Resource, e.Environment, e.err)
	case e.Move:
		return fmt.Sprintf("The manifest has %s on its new target now. Plan %s from the project to move it. Planning failed: %v", e.Resource, e.Environment, e.err)
	}
	if e.Repin {
		return fmt.Sprintf("The manifest has %s on the registry's new pin now. Plan %s from the project to update it. Planning failed: %v", e.Resource, e.Environment, e.err)
	}
	return fmt.Sprintf("The manifest has %s now. Plan %s from the project to deploy it. Planning failed: %v", e.Resource, e.Environment, e.err)
}

func (e *PlanAfterEditError) Unwrap() error { return e.err }

// Limits of a deploy.
const (
	// maxResourceName is the longest DefaultResource.
	maxResourceName = 40
	// maxBudgetUSD is the largest monthly budget, in dollars.
	maxBudgetUSD = 100000
	// minContext is the shortest context a resource can ask for: the
	// max_model_len setting has three digits or more.
	minContext = 100
)

// resourceName is a manifest resource name.
var resourceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

var nonLabel = regexp.MustCompile(`[^a-z0-9]+`)

// DefaultResource is the resource name a deploy of e gets when none is
// given: its repo's name as a DNS label, "llama-3-1-8b-instruct", at most
// 40 characters, starting with a letter.
func DefaultResource(e registry.Entry) string {
	repo := repoOf(e)
	_, name, _ := strings.Cut(repo, "/")
	name = strings.Trim(nonLabel.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		name = "model-" + name
	}
	if len(name) > maxResourceName {
		name = name[:maxResourceName]
	}
	return strings.TrimRight(name, "-")
}

// DefaultAlias is the name a gateway gives e by default: its repo's name,
// lowercased, "llama-3.1-8b-instruct".
func DefaultAlias(e registry.Entry) string {
	_, name, _ := strings.Cut(repoOf(e), "/")
	return strings.ToLower(name)
}

// Preview says what deploying in to the environment would do: the
// quantization, GPUs, context, kind, cost and resource name, and every
// problem that stops it. ai:read; it writes nothing.
func (s *Service) Preview(ctx context.Context, p security.Principal, projectID, environmentID string, in DeployInput) (*DeployPreview, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	in.Moving = nil
	pv, err := s.preview(ctx, p, projectID, env, in, false)
	if err != nil {
		return nil, err
	}
	// Preview only reads, so it asks without repairing anything.
	allowed, err := s.d.Authz.Allowed(ctx, p, writeNeed(env))
	if err != nil {
		return nil, err
	}
	pv.CanDeploy = len(pv.Problems) == 0 && allowed && !pv.ServerReviewNeeded
	return pv, nil
}

// deployTarget is the place a preview resolved: a connection, and for a GPU the
// target row with its kind. For a server size, size is true and gpu is the
// size's row.
type deployTarget struct {
	name, provider string
	gpu            *gpus.GPUTarget // nil on CPU
	size           bool
}

// preview works the deploy out. It returns an error only when it can't
// answer at all; everything that stops the deploy is a Problem. deploying
// is true for Deploy, which needs the GPU server a server size creates
// approved in in.Server.
func (s *Service) preview(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, in DeployInput, deploying bool) (*DeployPreview, error) {
	if in.MaxModelLen < 0 {
		return nil, fmt.Errorf("%w: max_model_len can't be negative", ErrInvalid)
	}
	v, err := s.lookup(ctx, p.OrgID, strings.TrimSpace(in.EntryID))
	if err != nil {
		return nil, err
	}
	e := v.entry
	pv := &DeployPreview{
		Entry: v.summary, Target: in.Target, Gated: e.Gated, Problems: []Problem{}, env: env, entry: e,
		Environments: []Reach{{EnvironmentID: env.ID, Environment: env.Name, Mode: ReachHome,
			Message: "Runs in " + env.Name + ". Services there link to it and get MODEL_URL and MODEL_API_KEY."}},
	}
	problem := func(code, format string, args ...any) {
		pv.Problems = append(pv.Problems, Problem{Code: code, Message: fmt.Sprintf(format, args...)})
	}

	s.checkBudget(pv, in.MonthlyBudgetUSD, problem)
	if in.Moving != nil {
		// A move keeps the resource it moves.
		pv.Resource = in.Resource
	} else {
		s.checkName(ctx, p, projectID, env, e, in.Resource, in.self, pv, problem)
	}

	t, ok := s.resolveTarget(ctx, p, in.Target, in.Moving, problem)
	if !ok {
		return pv, nil
	}
	pv.Target.Provider, pv.TargetName = t.provider, t.name
	q, fit, ok := s.pickQuantization(e, in.Quantization, t, pv.joinKind, problem)
	if !ok {
		return pv, nil
	}
	pv.quant, pv.Quantization, pv.Fit = q, q.ID, fit

	onGPU := t.gpu != nil
	kind, _ := recipes.ModelKindFor(string(q.Engine), onGPU)
	k, found := recipes.Default().Kind(kind)
	if !found || len(k.Releases) == 0 {
		problem(ProblemEngine, "Loomplane has no model kind that runs %s %s here.", e.Title, q.ID)
		return pv, nil
	}
	if pv.joinKind != "" && kind != pv.joinKind {
		// Decision 2: one resource has one kind in every environment.
		problem(ProblemEngine, "%s", otherKind(pv.AddsTo, pv.joinKind, kind, t.name))
		return pv, nil
	}
	if !slices.Contains(k.Targets, t.provider) {
		problem(ProblemTarget, "%s doesn't run on a %s target.", k.Title, t.provider)
		return pv, nil
	}
	pv.Kind, pv.Version = kind, k.Releases[0].Version
	if in.Moving != nil && in.Moving.Kind != "" && in.Moving.Kind != kind {
		problem(ProblemEngine, "%s runs as a %s, and on %s it would run as a %s. Remove it and deploy it there instead.", in.Resource, in.Moving.Kind, t.name, kind)
		return pv, nil
	}

	need, count := fit.Need, 0
	if onGPU {
		count = need.GPUs
		if asked := in.Target.GPU.Count; asked > count {
			if asked > t.gpu.Free {
				problem(ProblemTarget, "%s has %d free %s GPUs, not %d.", t.name, t.gpu.Free, gpuTitle(t.gpu.Accelerator.Kind), asked)
				return pv, nil
			}
			more, reason, fits := registry.Estimate(e, q, t.gpu.Accelerator.Kind, asked)
			if !fits {
				problem(ProblemNoFit, "%s", reason)
				return pv, nil
			}
			need, count = more, asked
		}
		pv.GPU = &provider.GPURequest{Kind: t.gpu.Accelerator.Kind, Count: count}
	}
	pv.MaxModelLen = need.MaxModelLen
	if in.MaxModelLen > 0 {
		on := "CPU"
		if onGPU {
			on = gpuTitle(t.gpu.Accelerator.Kind)
		}
		switch {
		case in.MaxModelLen > need.MaxModelLen:
			problem(ProblemContext, "At most %d tokens fit on %s.", need.MaxModelLen, on)
		case in.MaxModelLen < minContext:
			problem(ProblemContext, "Ask for a context of at least %d tokens.", minContext)
		default:
			pv.MaxModelLen = in.MaxModelLen
		}
	}

	switch {
	case t.size:
		// The server is created for the model, so its whole price is the
		// model's. The review below prices it again from the provider.
		pv.Cost = serverCost(t.gpu.Accelerator, t.gpu.HourlyUSD, t.gpu.HourlyPriceKnown)
	case onGPU:
		pv.Cost = HourlyCost(*t.gpu, count, "")
		if m := in.Moving; m != nil && m.ServerCloudID != "" && m.ConnectionID == t.gpu.ConnectionID {
			// A move on the server Loomplane created for the model keeps all
			// of it when nothing else holds a GPU there. t.Free already
			// counts the GPUs the model gives back.
			if c, whole := wholeServerCost(*t.gpu, 0, ""); whole {
				pv.Cost = c
			}
		}
	default:
		// A CPU engine holds no GPU: its cost is part of the server's.
		pv.Cost = HourlyCost(gpus.GPUTarget{}, 0, "")
	}

	served := repoOf(e)
	if pv.servedName != "" {
		// Callers use one model name in every environment.
		served = pv.servedName
	}
	pv.spec = engines.Spec{
		Entry: e, Quantization: q, ServedName: served, MaxModelLen: pv.MaxModelLen, GPUs: count,
		OnGPU: kind == recipes.ModelLlamaCPPGPUKind,
	}
	if _, err := engines.Args(pv.spec); err != nil {
		problem(ProblemEngine, "%s can't run as asked: %v.", e.Title, err)
	}
	if e.Gated {
		s.checkGated(ctx, p.OrgID, e, q, pv, problem)
	}
	if t.size && len(pv.Problems) == 0 {
		s.reviewServer(ctx, p, projectID, env, in, deploying, pv, problem)
	}
	return pv, nil
}

// checkBudget refuses a budget that isn't an amount above 0 and at most
// 100000 dollars.
func (s *Service) checkBudget(pv *DeployPreview, budget string, problem func(code, format string, args ...any)) {
	budget = strings.TrimSpace(budget)
	if budget == "" {
		return
	}
	units, err := budgetAmount.parse(budget)
	if err != nil || units <= 0 || units > maxBudgetUSD*unitsPerUSD {
		problem(ProblemBudget, "A monthly budget is an amount in dollars above 0 and at most %d, like 250.00.", maxBudgetUSD)
		return
	}
	pv.budgetUSD = budget
}

// checkName picks the resource name and checks the manifest has room for
// it (chooseName). When the same model already runs in other
// environments, the deploy adds env to that resource: pv.AddsTo names it,
// and the preview lists every environment it will run in.
//
// The project's deployment rows count too: a model waiting for a GPU
// server Loomplane is creating has no manifest resource until the server
// is up. self is the row a waiting deploy carries on, which doesn't count.
func (s *Service) checkName(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, e registry.Entry,
	asked, self string, pv *DeployPreview, problem func(code, format string, args ...any),
) {
	name := strings.TrimSpace(asked)
	pv.Resource = name
	if name == "" {
		pv.Resource = DefaultResource(e)
	}
	if !resourceName.MatchString(pv.Resource) {
		problem(ProblemName, "Name the model with lowercase letters, digits and hyphens, starting with a letter, at most 40 characters. %q isn't one.", pv.Resource)
		return
	}
	if s.d.Manifests == nil {
		problem(ProblemNoManifest, "Loomplane can't read this project's manifest here.")
		return
	}
	rev, err := s.d.Manifests.Current(ctx, p, projectID)
	if errors.Is(err, manifests.ErrNotFound) {
		problem(ProblemNoManifest, "This project has no manifest yet. Save one, then deploy the model.")
		return
	}
	if err != nil || rev.Manifest == nil {
		problem(ProblemNoManifest, "Loomplane couldn't read this project's manifest. Try again.")
		return
	}
	m := rev.Manifest
	rows, ok := s.projectRows(ctx, p.OrgID, projectID, self)
	if !ok {
		problem(ProblemNoManifest, "Loomplane couldn't read this project's models. Try again.")
		return
	}
	chosen, join, refused := chooseName(m, rows, e, env, name)
	pv.Resource = chosen
	if refused != nil {
		pv.Problems = append(pv.Problems, *refused)
		return
	}
	if !join {
		return
	}
	r := m.Resources[chosen]
	pv.AddsTo, pv.joinKind, pv.servedName = chosen, r.Kind, r.Settings["served_name"]
	pv.Environments = s.copiesOf(ctx, p.OrgID, projectID, env, chosen, r)
}

// copiesOf is how each environment reaches the resource name, r, once env
// runs it too: env first, then the environments r already runs in, by
// name.
func (s *Service) copiesOf(ctx context.Context, orgID, projectID string, env *projects.Environment, name string, r manifest.Resource) []Reach {
	out := []Reach{{EnvironmentID: env.ID, Environment: env.Name, Mode: ReachHome,
		Message: fmt.Sprintf("Adds %s to %s. Services in %s link to this copy.", env.Name, name, env.Name)}}
	if s.d.Projects == nil {
		return out
	}
	envs, err := s.d.Projects.ListEnvironments(ctx, orgID, projectID)
	if err != nil {
		return out
	}
	slices.SortFunc(envs, func(a, b *projects.Environment) int { return strings.Compare(a.Name, b.Name) })
	for _, other := range envs {
		if other.ID == env.ID || !enabledIn(r, other.Name) {
			continue
		}
		out = append(out, Reach{EnvironmentID: other.ID, Environment: other.Name, Mode: ReachHome,
			Message: fmt.Sprintf("Runs in %s on its own target. Services in %s link to that copy.", other.Name, other.Name)})
	}
	return out
}

// chooseName is the resource a deploy of e to env runs as, and whether
// that resource is already in m, running e in other environments (join).
// asked is the name the deploy asked for, "" for none. rows are the
// project's deployment rows, the deploy's own left out. In order:
//
//  1. e already running in env, under any name, or a row for e in env,
//     is ProblemExists.
//  2. A model resource of e named asked is joined. A service, resource or
//     gateway of that name, or a row of that name for another entry, is
//     ProblemNameTaken; otherwise asked is a new resource. A row of e in
//     another environment is the same model, so it doesn't block it.
//  3. With no name asked, a model resource of e in m is joined:
//     DefaultResource(e) first, else the first by name. Otherwise
//     DefaultResource(e) when it's free, else the first free of
//     "<default>-2", "<default>-3" and so on.
func chooseName(m *manifest.Manifest, rows []*Deployment, e registry.Entry, env *projects.Environment, asked string) (name string, join bool, p *Problem) {
	names := slices.Sorted(maps.Keys(m.Resources))
	for _, other := range names {
		if r := m.Resources[other]; modelOf(r, e.ID, env.Name, m.Defaults) && enabledIn(r, env.Name) {
			return other, false, &Problem{Code: ProblemExists, Message: fmt.Sprintf("%s already runs %s in %s.", other, e.Title, env.Name)}
		}
	}
	rows = slices.Clone(rows)
	slices.SortFunc(rows, func(a, b *Deployment) int { return strings.Compare(a.Resource, b.Resource) })
	for _, d := range rows {
		if d.EnvironmentID == env.ID && d.EntryID == e.ID {
			return d.Resource, false, &Problem{Code: ProblemExists, Message: fmt.Sprintf("%s is already deploying %s in %s.", d.Resource, e.Title, env.Name)}
		}
	}
	ours := func(name string) bool {
		r, ok := m.Resources[name]
		return ok && modelOf(r, e.ID, env.Name, m.Defaults)
	}
	clash := func(name string) *Problem {
		if taken(m, name) {
			return &Problem{Code: ProblemNameTaken, Message: name + " is already in this project's manifest. Pick another name."}
		}
		if slices.ContainsFunc(rows, func(d *Deployment) bool { return d.Resource == name && d.EntryID != e.ID }) {
			return &Problem{Code: ProblemNameTaken, Message: name + " is already a model in this project. Pick another name."}
		}
		return nil
	}
	if asked != "" {
		if ours(asked) {
			return asked, true, nil
		}
		return asked, false, clash(asked)
	}
	def := DefaultResource(e)
	if ours(def) {
		return def, true, nil
	}
	if i := slices.IndexFunc(names, ours); i >= 0 {
		return names[i], true, nil
	}
	if clash(def) == nil {
		return def, false, nil
	}
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		candidate := strings.TrimRight(def[:min(len(def), maxResourceName-len(suffix))], "-") + suffix
		if clash(candidate) == nil {
			return candidate, false, nil
		}
	}
}

// modelOf reports whether r is a model resource of the entry entryID, read
// as env runs it.
func modelOf(r manifest.Resource, entryID, env string, d manifest.Defaults) bool {
	return recipes.IsModelKind(r.Kind) && r.For(env, d).Settings["entry"] == entryID
}

// otherKind says a model can't run as kind on target, since resource runs
// as was in its other environments (Decision 2).
func otherKind(resource, was, kind, target string) string {
	return fmt.Sprintf("%s runs as %s in its other environments, and on %s it would run as %s. Pick a target it runs on as %s, or deploy it as a separately named model.",
		resource, kindTitle(was), target, kindTitle(kind), kindTitle(was))
}

// kindTitle is a model kind's title, "vLLM model", or its name.
func kindTitle(kind string) string {
	if k, ok := recipes.Default().Kind(kind); ok {
		return k.Title
	}
	return kind
}

// projectRows is the project's deployment rows, all but self's. ok is
// false when they can't be read.
func (s *Service) projectRows(ctx context.Context, orgID, projectID, self string) ([]*Deployment, bool) {
	if s.d.Store == nil {
		return nil, true
	}
	rows, err := s.d.Store.ListProject(ctx, orgID, projectID)
	if err != nil {
		return nil, false
	}
	return slices.DeleteFunc(rows, func(d *Deployment) bool { return self != "" && d.ID == self }), true
}

// taken reports whether a service, resource or gateway of m has name.
func taken(m *manifest.Manifest, name string) bool {
	_, service := m.Services[name]
	_, resource := m.Resources[name]
	_, gateway := m.Gateways[name]
	return service || resource || gateway
}

// enabledIn reports whether an opt-in resource runs in env.
func enabledIn(r manifest.Resource, env string) bool {
	on := r.Environments[env].Enabled
	return on != nil && *on
}

// resolveTarget finds the target the deploy asked for. A move onto the
// GPUs its model already holds gets them back as free: the runtimes stop
// the old engine before the new one starts (Docker counts the old
// container's devices as free, Kubernetes recreates a GPU pod).
func (s *Service) resolveTarget(ctx context.Context, p security.Principal, choice TargetChoice, moving *MovingFrom,
	problem func(code, format string, args ...any),
) (deployTarget, bool) {
	switch choice.Source {
	case TargetConnection:
	case TargetServerSize:
		return s.sizeTarget(ctx, p, choice, problem)
	default:
		problem(ProblemTarget, "Pick a server or cluster to deploy the model on.")
		return deployTarget{}, false
	}
	if choice.GPU == nil {
		return s.cpuTarget(ctx, p, choice.ConnectionID, problem)
	}
	if s.d.GPUs == nil {
		problem(ProblemTarget, "%s", FitsUnknown)
		return deployTarget{}, false
	}
	list, err := s.d.GPUs.Targets(ctx, p, gpus.GPUTargetQuery{Count: 1})
	if err != nil {
		problem(ProblemTarget, "%s", FitsUnknown)
		return deployTarget{}, false
	}
	for i := range list.Targets {
		t := list.Targets[i]
		if t.Source != gpus.SourceConnection || t.ConnectionID != choice.ConnectionID || t.Accelerator.Kind != choice.GPU.Kind {
			continue
		}
		if t.Cloud != connections.Server && t.Cloud != connections.Kubernetes {
			break
		}
		if moving != nil && moving.ConnectionID == t.ConnectionID && moving.GPUKind == t.Accelerator.Kind && moving.GPUs > 0 {
			t.Free = min(t.Free+moving.GPUs, t.Accelerator.Count)
		}
		if t.Free < 1 {
			msg := t.Message
			if msg == "" {
				msg = fmt.Sprintf("%s has no free %s GPU.", t.Name, gpuTitle(t.Accelerator.Kind))
			}
			problem(ProblemTarget, "%s", msg)
			return deployTarget{}, false
		}
		return deployTarget{name: t.Name, provider: t.Cloud, gpu: &t}, true
	}
	problem(ProblemTarget, "This workspace has no server or cluster with a %s GPU by that ID.", gpuTitle(choice.GPU.Kind))
	return deployTarget{}, false
}

// cpuTarget finds a ready server or cluster to run a CPU engine on.
func (s *Service) cpuTarget(ctx context.Context, p security.Principal, id string, problem func(code, format string, args ...any)) (deployTarget, bool) {
	if s.d.Connections == nil {
		problem(ProblemTarget, "%s", FitsUnknown)
		return deployTarget{}, false
	}
	conns, err := s.d.Connections.List(ctx, p)
	if err != nil {
		problem(ProblemTarget, "Couldn't read your servers and clusters.")
		return deployTarget{}, false
	}
	for _, c := range conns {
		if c.ID != id || (c.Cloud != connections.Server && c.Cloud != connections.Kubernetes) {
			continue
		}
		if c.Status != connections.StatusReady {
			problem(ProblemTarget, "%s isn't ready. Check it in Connections, then try again.", c.Name)
			return deployTarget{}, false
		}
		return deployTarget{name: c.Name, provider: c.Cloud}, true
	}
	problem(ProblemTarget, "This workspace has no server or cluster by that ID.")
	return deployTarget{}, false
}

// pickQuantization is the one asked for, or the first of the entry's that
// fits the target. With kind, the quantizations that run as kind on t come
// first, then the rest, so a model joining a resource of that kind keeps
// it where it can, and the kind check names the mismatch where it can't.
// A configuration a qualification run failed never fits.
func (s *Service) pickQuantization(e registry.Entry, asked string, t deployTarget, kind string, problem func(code, format string, args ...any)) (registry.Quantization, registry.Fit, bool) {
	candidates := e.Quantizations
	switch {
	case asked != "":
		q, ok := e.Quantization(asked)
		if !ok {
			problem(ProblemQuantization, "%s has no quantization %s.", e.Title, asked)
			return registry.Quantization{}, registry.Fit{}, false
		}
		candidates = []registry.Quantization{q}
	case kind != "":
		ordered := make([]registry.Quantization, 0, len(candidates))
		var rest []registry.Quantization
		for _, q := range candidates {
			if k, _ := recipes.ModelKindFor(string(q.Engine), t.gpu != nil); k == kind {
				ordered = append(ordered, q)
			} else {
				rest = append(rest, q)
			}
		}
		candidates = slices.Concat(ordered, rest)
	}
	var reason string
	for _, q := range candidates {
		fit := s.fitOnTarget(e, q, t)
		if fit.Fits() {
			return q, fit, true
		}
		reason = fit.Reason
	}
	if reason == "" {
		reason = "It doesn't fit here."
	}
	problem(ProblemNoFit, "%s", reason)
	return registry.Quantization{}, registry.Fit{}, false
}

// fitOnTarget is q's fit on t: on its GPU kind with up to its free GPUs,
// or on CPU. llama.cpp on CPU on Kubernetes also needs its weights to fit
// the fixed weight cache the CPU kinds get there (Ruling M20).
func (s *Service) fitOnTarget(e registry.Entry, q registry.Quantization, t deployTarget) registry.Fit {
	if t.gpu != nil {
		return s.d.Registry.FitOn(e, q, t.gpu.Accelerator.Kind, t.gpu.Free, engines.Current())
	}
	if t.provider == connections.Kubernetes {
		if reason := CPUCacheProblem(q); reason != "" {
			need, _, _ := registry.CPUFits(e, q)
			return registry.Fit{GPUKind: registry.CPUKind, Need: need, Status: registry.StatusNoFit, Reason: reason}
		}
	}
	return s.d.Registry.FitOn(e, q, registry.CPUKind, 0, engines.Current())
}

// checkGated says whether the workspace's token can download the gated
// weights, without ever putting the token in a problem.
func (s *Service) checkGated(ctx context.Context, orgID string, e registry.Entry, q registry.Quantization, pv *DeployPreview, problem func(code, format string, args ...any)) {
	token, err := s.token(ctx, orgID)
	if err != nil {
		problem(ProblemGated, "%v.", err)
		return
	}
	if token == "" {
		problem(ProblemGated, "%s.", (&GatedError{Title: e.Title}).Error())
		return
	}
	if s.d.Hub == nil {
		problem(ProblemGated, "Loomplane can't check your Hugging Face access here.")
		return
	}
	repo, revision := weightsOf(e, q)
	if q.File != "" {
		err = s.d.Hub.AccessFile(ctx, repo, revision, q.File, token)
	} else {
		err = s.d.Hub.Access(ctx, repo, revision, token)
	}
	switch {
	case errors.Is(err, hfhub.ErrGated):
		problem(ProblemGated, "Your Hugging Face account hasn't accepted the license for %s. Accept it at %s/%s, then try again.", repo, hfhub.DefaultURL, repo)
	case err != nil:
		problem(ProblemGated, "Couldn't check your Hugging Face access to %s. Try again.", repo)
	default:
		pv.TokenReady = true
	}
}

// weightsOf is where q's weights download from: a custom entry's ID stands
// for its repo, as engines renders it.
func weightsOf(e registry.Entry, q registry.Quantization) (repo, revision string) {
	repo, revision = q.WeightsRepo(e)
	if q.Repo == "" {
		repo = repoOf(e)
	}
	return repo, revision
}

// gpuTitle is a GPU kind's title, "NVIDIA L4", or its ID.
func gpuTitle(kind string) string {
	if k, ok := provider.LookupAcceleratorKind(kind); ok {
		return k.Title
	}
	return kind
}

// Deploy adds the model to the project's manifest, running in the
// environment on the target, and plans the environment. It needs WriteNeed
// of the environment, checked before anything is written, and refuses with
// the first problem Preview would show. A model pasted from Hugging Face is
// stored for the workspace first. A gated model's row is written under the
// workspace's token lock, so a token delete can't miss it.
//
// When the plan fails after the manifest edit, the row stays, with a
// message, and the error is a PlanAfterEditError.
//
// A server size target creates the GPU server first: in.Server approves
// the review Preview made, and the deployment waits for the server, with
// no manifest edit yet. Reconcile carries it on once the server is up.
func (s *Service) Deploy(ctx context.Context, p security.Principal, projectID, environmentID string, in DeployInput) (*DeployResult, error) {
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	if aerr := s.d.Authz.Require(ctx, p, writeNeed(env), ""); aerr != nil {
		return nil, aerr
	}
	if s.d.Store == nil || s.d.Manifests == nil || s.d.Plans == nil {
		return nil, errors.New("models: deploying isn't configured")
	}
	in.Moving = nil
	pv, err := s.preview(ctx, p, projectID, env, in, true)
	if err != nil {
		return nil, err
	}
	if len(pv.Problems) > 0 {
		return nil, &ProblemError{Problem: pv.Problems[0]}
	}
	e, q := pv.entry, pv.quant
	if strings.HasPrefix(e.ID, customPrefix) {
		if s.d.Custom == nil {
			return nil, errors.New("models: custom models aren't configured")
		}
		if err = s.d.Custom.PutCustom(ctx, p.OrgID, e); err != nil {
			return nil, fmt.Errorf("store %s for the workspace: %w", e.ID, err)
		}
	}

	if pv.Server != nil {
		return s.deployServerFirst(ctx, p, projectID, env, in, pv)
	}

	row := &Deployment{
		OrgID: p.OrgID, ProjectID: projectID, EnvironmentID: env.ID, Resource: pv.Resource, EntryID: e.ID,
		Quantization: q.ID, Engine: string(q.Engine), MonthlyBudgetUSD: pv.budgetUSD, Phase: PhasePlanned,
		ServerName: pv.TargetName, RequestedBy: p,
	}
	if cerr := s.createRow(ctx, p.OrgID, e, row); cerr != nil {
		return nil, cerr
	}
	rev, err := s.placeResource(ctx, p, projectID, env, pv)
	if err != nil {
		// Nothing points at the row yet, so it goes.
		if derr := s.d.Store.Delete(context.WithoutCancel(ctx), p.OrgID, projectID, env.ID, pv.Resource); derr != nil {
			err = errors.Join(err, fmt.Errorf("and its deployment row stays: %w", derr))
		}
		return nil, err
	}
	return s.planRow(ctx, p, projectID, env, rev, row, false)
}

// placeResource makes a deploy's one manifest edit. A new resource is
// added running in env on the preview's target and nowhere else. A model
// resource of the entry not running in env yet gets env added: its own
// target, and the provider, GPU and settings where they differ
// (placementUpdate). The manifest is read at the edit, not taken from the
// preview, so a deploy of the same model to another environment that got
// there first is joined. One that lands between that read and the save
// (manifests.ErrConflict, or an add refused because the name is there
// now) is read again and decided once more.
func (s *Service) placeResource(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, pv *DeployPreview) (*manifests.Revision, error) {
	for retried := false; ; retried = true {
		cur, err := s.d.Manifests.Current(ctx, p, projectID)
		if err != nil {
			return nil, err
		}
		if cur.Manifest == nil {
			return nil, errors.New("models: the project's manifest doesn't parse")
		}
		in, join, refused := placementEdit(cur.Manifest, env.Name, pv)
		if refused != nil {
			return nil, &ProblemError{Problem: *refused}
		}
		rev, _, err := s.d.Manifests.Edit(ctx, p, projectID, in)
		if err == nil {
			attrs := map[string]any{
				"entry": pv.entry.ID, "quantization": pv.quant.ID, "target": pv.Target.ConnectionID, "resource": pv.Resource,
				"environment": env.Name, "kind": pv.Kind, "manifest_revision": rev.Revision,
			}
			if join {
				attrs["adds_environment"] = true
			}
			s.audit(ctx, p, projectID, "ai.model.deploy", attrs)
			return rev, nil
		}
		if retried || !s.raced(ctx, p, projectID, in, err) {
			return nil, err
		}
	}
}

// raced reports whether in failed because another save got there first:
// a conflict, or an add of a name the manifest has now.
func (s *Service) raced(ctx context.Context, p security.Principal, projectID string, in manifests.EditInput, err error) bool {
	if errors.Is(err, manifests.ErrConflict) {
		return true
	}
	if in.AddResource == nil {
		return false
	}
	cur, cerr := s.d.Manifests.Current(ctx, p, projectID)
	if cerr != nil || cur.Manifest == nil {
		return false
	}
	_, there := cur.Manifest.Resources[in.AddResource.Name]
	return there
}

// placementEdit is the edit that puts pv's model in env, given the
// manifest m as it is now, and whether it adds env to a resource already
// there (join).
func placementEdit(m *manifest.Manifest, env string, pv *DeployPreview) (in manifests.EditInput, join bool, p *Problem) {
	r, ok := m.Resources[pv.Resource]
	switch {
	case !ok:
		var gpu *manifest.GPU
		if pv.GPU != nil {
			gpu = &manifest.GPU{Kind: pv.GPU.Kind, Count: pv.GPU.Count}
		}
		return manifests.EditInput{AddResource: &manifests.NewResource{
			Name: pv.Resource, Kind: pv.Kind, Provider: pv.Target.Provider, Target: pv.Target.ConnectionID, Version: pv.Version,
			Settings: engines.Settings(pv.spec), GPU: gpu, EnabledIn: env,
		}}, false, nil
	case !modelOf(r, pv.entry.ID, env, m.Defaults):
		return in, false, &Problem{Code: ProblemNameTaken, Message: pv.Resource + " is already in this project's manifest. Pick another name."}
	case enabledIn(r, env):
		return in, false, &Problem{Code: ProblemExists, Message: fmt.Sprintf("%s already runs %s in %s.", pv.Resource, pv.entry.Title, env)}
	case r.Kind != pv.Kind:
		return in, false, &Problem{Code: ProblemEngine, Message: otherKind(pv.Resource, r.Kind, pv.Kind, pv.TargetName)}
	}
	return manifests.EditInput{UpdateResource: placementUpdate(r, env, m.Defaults, pv)}, true, nil
}

// placementUpdate turns raw on in env, on the preview's target, writing
// under env only what env doesn't already read: the provider, the GPU and
// each setting where pv differs from raw as env runs it. entry and
// served_name are never written per environment, so the model has one
// name everywhere.
func placementUpdate(raw manifest.Resource, env string, d manifest.Defaults, pv *DeployPreview) *manifests.ResourceUpdateInput {
	on, target := true, pv.Target.ConnectionID
	u := &manifests.ResourceUpdateInput{Resource: pv.Resource, Environment: env, Enabled: &on, Target: &target}
	view := raw.For(env, d)
	if cloud := pv.Target.Provider; cloud != view.Provider {
		u.Provider = &cloud
	}
	if pv.GPU != nil {
		gpu := manifest.GPU{Kind: pv.GPU.Kind, Count: pv.GPU.Count}
		if view.GPU == nil || *view.GPU != gpu {
			u.GPU = &gpu
		}
	}
	settings := engines.Settings(pv.spec)
	for _, key := range slices.Sorted(maps.Keys(settings)) {
		if key == "entry" || key == "served_name" || settings[key] == view.Settings[key] {
			continue
		}
		if u.Settings == nil {
			u.Settings = map[string]string{}
		}
		u.Settings[key] = settings[key]
	}
	return u
}

// planRow plans env from rev and records the plan on row. When planning
// fails, the row says how to deploy it from the project, and the error is
// a PlanAfterEditError.
//
// move is true for Move's edit: the row then says how to finish the move.
func (s *Service) planRow(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, rev *manifests.Revision, row *Deployment, move bool) (*DeployResult, error) {
	plan, err := s.d.Plans.Create(ctx, p, projectID, planning.CreatePlanInput{Environment: env.Name, Catalog: rev.Manifest.CatalogOnly()})
	if err != nil {
		perr := &PlanAfterEditError{Resource: row.Resource, Environment: env.Name, Move: move, err: err}
		row.Message = fmt.Sprintf("The manifest has %s now. Plan %s from the project to deploy it.", row.Resource, env.Name)
		if move {
			row.Message = fmt.Sprintf("The manifest has %s on its new target now. Plan %s from the project to move it.", row.Resource, env.Name)
		}
		if uerr := s.d.Store.Update(context.WithoutCancel(ctx), row); uerr != nil {
			return &DeployResult{Deployment: row}, errors.Join(perr, uerr)
		}
		return &DeployResult{Deployment: row}, perr
	}
	row.PlanID = plan.ID
	if err = s.d.Store.Update(ctx, row); err != nil {
		return &DeployResult{Deployment: row, PlanID: plan.ID}, fmt.Errorf("the plan %s is made, but its deployment row wasn't updated: %w", plan.ID, err)
	}
	return &DeployResult{Deployment: row, PlanID: plan.ID}, nil
}

// createRow writes the deployment row: for a gated model under the
// workspace's token lock, refusing when the token is gone by then.
func (s *Service) createRow(ctx context.Context, orgID string, e registry.Entry, row *Deployment) error {
	create := func(ctx context.Context) error {
		err := s.d.Store.Create(ctx, row)
		if errors.Is(err, ErrConflict) {
			return &ProblemError{Problem: Problem{Code: ProblemNameTaken,
				Message: row.Resource + " is already a model in this environment. Pick another name."}}
		}
		return err
	}
	if !e.Gated {
		return create(ctx)
	}
	if s.d.TokenGuard == nil {
		return errors.New("models: the Hugging Face token lock isn't configured")
	}
	return s.d.TokenGuard.WithHuggingFaceToken(ctx, orgID, func(ctx context.Context, revision int64) error {
		if revision == 0 {
			return &ProblemError{Problem: Problem{Code: ProblemGated, Message: (&GatedError{Title: e.Title}).Error() + "."}}
		}
		return create(ctx)
	})
}

// audit records a change, when there's an Auditor.
func (s *Service) audit(ctx context.Context, p security.Principal, projectID, action string, attrs map[string]any) {
	if s.d.Audit == nil {
		return
	}
	s.d.Audit.Record(ctx, projects.AuditEvent{Action: action, Severity: "info", ProjectID: projectID, Principal: p, Attributes: attrs})
}
