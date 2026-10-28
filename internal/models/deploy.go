package models

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
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

// Reach says how the services of one environment reach the model. Until
// the gateway, a model runs in one environment, and only that one's
// services link to it.
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
}

// DeployResult is the deployment a deploy recorded, and the plan it made.
type DeployResult struct {
	Deployment *Deployment `json:"deployment"`
	PlanID     string      `json:"plan_id,omitempty"`
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
	// Repin is true for MoveToPin's edit.
	Repin bool
	err   error
}

func (e *PlanAfterEditError) Error() string {
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
	s.checkName(ctx, p, projectID, env, e, in.Resource, pv, problem)

	t, ok := s.resolveTarget(ctx, p, in.Target, problem)
	if !ok {
		return pv, nil
	}
	pv.Target.Provider, pv.TargetName = t.provider, t.name
	q, fit, ok := s.pickQuantization(e, in.Quantization, t, problem)
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
	if !slices.Contains(k.Targets, t.provider) {
		problem(ProblemTarget, "%s doesn't run on a %s target.", k.Title, t.provider)
		return pv, nil
	}
	pv.Kind, pv.Version = kind, k.Releases[0].Version

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
	default:
		// A CPU engine holds no GPU: its cost is part of the server's.
		pv.Cost = HourlyCost(gpus.GPUTarget{}, 0, "")
	}

	pv.spec = engines.Spec{
		Entry: e, Quantization: q, ServedName: repoOf(e), MaxModelLen: pv.MaxModelLen, GPUs: count,
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
// it. A model runs in one environment, so the same entry deployed in
// another environment gets a new name: the default, or when that's taken,
// the default and the environment's name.
func (s *Service) checkName(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, e registry.Entry,
	asked string, pv *DeployPreview, problem func(code, format string, args ...any),
) {
	name := strings.TrimSpace(asked)
	given := name != ""
	if !given {
		name = DefaultResource(e)
	}
	pv.Resource = name
	if !resourceName.MatchString(name) {
		problem(ProblemName, "Name the model with lowercase letters, digits and hyphens, starting with a letter, at most 40 characters. %q isn't one.", name)
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
	for _, other := range slices.Sorted(maps.Keys(m.Resources)) {
		r := m.Resources[other]
		if recipes.IsModelKind(r.Kind) && r.Settings["entry"] == e.ID && enabledIn(r, env.Name) {
			problem(ProblemExists, "%s already runs %s in %s.", other, e.Title, env.Name)
			return
		}
	}
	if !taken(m, name) {
		return
	}
	if !given {
		alt := strings.TrimRight(name[:min(len(name), maxResourceName-len(env.Name)-1)], "-") + "-" + env.Name
		if resourceName.MatchString(alt) && !taken(m, alt) {
			pv.Resource = alt
			return
		}
	}
	problem(ProblemNameTaken, "%s is already in this project's manifest. Pick another name.", pv.Resource)
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

// resolveTarget finds the target the deploy asked for.
func (s *Service) resolveTarget(ctx context.Context, p security.Principal, choice TargetChoice, problem func(code, format string, args ...any)) (deployTarget, bool) {
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
// fits the target. A configuration a qualification run failed never fits.
func (s *Service) pickQuantization(e registry.Entry, asked string, t deployTarget, problem func(code, format string, args ...any)) (registry.Quantization, registry.Fit, bool) {
	candidates := e.Quantizations
	if asked != "" {
		q, ok := e.Quantization(asked)
		if !ok {
			problem(ProblemQuantization, "%s has no quantization %s.", e.Title, asked)
			return registry.Quantization{}, registry.Fit{}, false
		}
		candidates = []registry.Quantization{q}
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
	rev, err := s.addResource(ctx, p, projectID, env, pv)
	if err != nil {
		// Nothing points at the row yet, so it goes.
		if derr := s.d.Store.Delete(context.WithoutCancel(ctx), p.OrgID, projectID, env.ID, pv.Resource); derr != nil {
			err = errors.Join(err, fmt.Errorf("and its deployment row stays: %w", derr))
		}
		return nil, err
	}
	return s.planRow(ctx, p, projectID, env, rev, row)
}

// addResource makes a deploy's one manifest edit: the resource, running
// in env on the preview's target and nowhere else.
func (s *Service) addResource(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, pv *DeployPreview) (*manifests.Revision, error) {
	var gpu *manifest.GPU
	if pv.GPU != nil {
		gpu = &manifest.GPU{Kind: pv.GPU.Kind, Count: pv.GPU.Count}
	}
	rev, _, err := s.d.Manifests.Edit(ctx, p, projectID, manifests.EditInput{AddResource: &manifests.NewResource{
		Name: pv.Resource, Kind: pv.Kind, Provider: pv.Target.Provider, Target: pv.Target.ConnectionID, Version: pv.Version,
		Settings: engines.Settings(pv.spec), GPU: gpu, EnabledIn: env.Name,
	}})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, p, projectID, "ai.model.deploy", map[string]any{
		"entry": pv.entry.ID, "quantization": pv.quant.ID, "target": pv.Target.ConnectionID, "resource": pv.Resource,
		"environment": env.Name, "kind": pv.Kind, "manifest_revision": rev.Revision,
	})
	return rev, nil
}

// planRow plans env from rev and records the plan on row. When planning
// fails, the row says how to deploy it from the project, and the error is
// a PlanAfterEditError.
func (s *Service) planRow(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, rev *manifests.Revision, row *Deployment) (*DeployResult, error) {
	plan, err := s.d.Plans.Create(ctx, p, projectID, planning.CreatePlanInput{Environment: env.Name, Catalog: rev.Manifest.CatalogOnly()})
	if err != nil {
		perr := &PlanAfterEditError{Resource: row.Resource, Environment: env.Name, err: err}
		row.Message = fmt.Sprintf("The manifest has %s now. Plan %s from the project to deploy it.", row.Resource, env.Name)
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
