package models

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/catalogstate"
	"github.com/xraph/loomplane/internal/cloudinfra"
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

// States of a model, as Status says them: the row's phase while it waits
// for a server or needs attention, else its catalog state.
const (
	StateWaitingForServer = PhaseWaitingForServer
	StateNeedsAttention   = PhaseNeedsAttention
	StatePlanned          = catalogstate.Planned
	StateDeploying        = catalogstate.Deploying
	StateReady            = catalogstate.Ready
	StateFailed           = catalogstate.Failed
	StateAbsent           = catalogstate.Absent
)

// LinkedService is a service that needs the model, in one environment,
// and whether it reaches it there.
type LinkedService struct {
	Environment string `json:"environment"`
	Service     string `json:"service"`
	Reachable   bool   `json:"reachable"`
	// Message says why it can't reach the model, and what to change.
	Message string `json:"message,omitempty"`
}

// Status is one model deployment as its page shows it: what runs, where,
// how it's doing, what it costs and who links to it.
type Status struct {
	// Deployment is the row Loomplane keeps, nil for a model added to the
	// manifest by hand that no deploy recorded.
	Deployment *Deployment `json:"deployment"`
	// ProjectID and Resource name the model for every row, a hand-added
	// one too: its project, and its resource in that project's manifest.
	ProjectID string `json:"project_id"`
	Resource  string `json:"resource"`
	State     string `json:"state"` // one of the State constants
	Message   string `json:"message,omitempty"`
	// Hint says what to try after a failed start, and whether the version
	// deployed before still serves.
	Hint string `json:"hint,omitempty"`
	// PlanID is the open plan that creates or changes it, if any.
	PlanID        string       `json:"plan_id,omitempty"`
	Entry         EntrySummary `json:"entry"`
	Kind          string       `json:"kind"`
	EngineVersion string       `json:"engine_version"`
	// Verification is the entry's fit on the GPUs it runs on: tested,
	// estimated or untested, or why it doesn't fit.
	Verification   registry.Fit         `json:"verification"`
	GPU            *provider.GPURequest `json:"gpu,omitempty"`
	ConnectionID   string               `json:"connection_id"`
	ConnectionName string               `json:"connection_name"`
	MaxModelLen    int                  `json:"max_model_len"`
	// InternalURL is the guard's address, what linked services get as
	// MODEL_URL. It isn't secret; the key is, and isn't here.
	InternalURL  string          `json:"internal_url,omitempty"`
	Environments []Reach         `json:"environments"`
	Linked       []LinkedService `json:"linked"`
	// Cost is what its GPUs cost per hour. Source "cpu" isn't free: the
	// model's cost is part of its server's, ConnectionName.
	Cost Cost `json:"cost"`
	// MoveCost is what the GPU server a model moves onto costs per hour,
	// while the move waits for that server; nil otherwise. The model keeps
	// running where it is until the move finishes, so once the new server
	// is up both bill: Cost and MoveCost together. An unknown price stays
	// unknown, never "0.00".
	MoveCost *Cost `json:"move_cost,omitempty"`
	// MonthToDateUSD is what it cost this month so far, four places.
	MonthToDateUSD string `json:"month_to_date_usd"`
	// ProjectedUSD is month to date plus the hourly price for the hours
	// left in the month, two places. Empty when the price is unknown, or
	// when the model runs on CPU and its cost is its server's.
	ProjectedUSD string `json:"projected_usd"`
	// UpdateAvailable is true when the registry pins other weights for its
	// entry now. Pin says what moving to the pin does; MoveToPin does it.
	UpdateAvailable bool       `json:"update_available"`
	Pin             *PinStatus `json:"pin,omitempty"`
	// CanEdit is true when the caller may change the environment's models.
	CanEdit bool `json:"can_edit"`
}

// UpdateInput changes what Loomplane keeps about a model, never what
// runs. A field left out keeps what is there.
type UpdateInput struct {
	MonthlyBudgetUSD  *string `json:"monthly_budget_usd,omitempty"`  // "" clears
	HourlyUSDOverride *string `json:"hourly_usd_override,omitempty"` // "" clears
	// Revision is the deployment's revision as read; a newer one is
	// ErrStale.
	Revision int64 `json:"revision"`
}

// MoveInput is where a model moves: a server or cluster you have, or a GPU
// server size Loomplane creates first, with the same review contract as a
// deploy (DeployPreview.Server).
type MoveInput struct {
	Target       TargetChoice    `json:"target"`
	Quantization string          `json:"quantization,omitempty"`  // "" picks the best that fits the new target
	MaxModelLen  int             `json:"max_model_len,omitempty"` // 0 is the largest that fits, at most 32768
	ReviewServer bool            `json:"review_server,omitempty"`
	Server       *ServerApproval `json:"server,omitempty"`
}

// Hints after a failed start.
const (
	hintMemory      = "The model didn't fit in GPU memory at %d tokens. Try a shorter context or a smaller quantization."
	hintMemoryNoLen = "The model didn't fit in GPU memory. Try a shorter context or a smaller quantization."
	hintStillServes = "The version deployed before is still running, so linked services keep working while you fix this."
)

// List lists every model the caller can read, across the workspace's
// projects: each deployment row, and each model a manifest runs in an
// environment that no deploy recorded (one added by hand), with the same
// cost rules as Status. ai:read. A project whose manifest the caller
// can't read is left out, and so is a row whose environment is gone.
func (s *Service) List(ctx context.Context, p security.Principal) ([]*Status, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	if s.d.Store == nil || s.d.Projects == nil {
		return []*Status{}, nil
	}
	projectList, err := s.d.Projects.ListProjects(ctx, p.OrgID)
	if err != nil {
		return nil, err
	}
	out := []*Status{}
	for _, project := range projectList {
		statuses, perr := s.listProject(ctx, p, project.ID)
		if errors.Is(perr, security.ErrForbidden) {
			continue
		}
		if perr != nil {
			return nil, fmt.Errorf("project %s: %w", project.Slug, perr)
		}
		out = append(out, statuses...)
	}
	return out, nil
}

// listProject is List for one project: its rows, then the models its
// manifest runs that have none.
func (s *Service) listProject(ctx context.Context, p security.Principal, projectID string) ([]*Status, error) {
	m, err := s.manifestOf(ctx, p, projectID)
	if err != nil {
		return nil, err
	}
	envs, err := s.d.Projects.ListEnvironments(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	rows, err := s.d.Store.ListProject(ctx, p.OrgID, projectID)
	if err != nil {
		return nil, err
	}
	byID := map[string]*projects.Environment{}
	for _, env := range envs {
		byID[env.ID] = env
	}
	out := []*Status{}
	recorded := map[string]bool{}
	for _, d := range rows {
		env := byID[d.EnvironmentID]
		if env == nil {
			continue
		}
		recorded[d.EnvironmentID+"/"+d.Resource] = true
		st, serr := s.status(ctx, p, projectID, env, d.Resource, d)
		if errors.Is(serr, ErrNotFound) {
			continue
		}
		if serr != nil {
			return nil, fmt.Errorf("model %s: %w", d.Resource, serr)
		}
		out = append(out, st)
	}
	if m == nil {
		return out, nil
	}
	for _, env := range envs {
		for _, name := range slices.Sorted(maps.Keys(m.Resources)) {
			r := m.Resources[name]
			if !recipes.IsModelKind(r.Kind) || !enabledIn(r, env.Name) || recorded[env.ID+"/"+name] {
				continue
			}
			st, serr := s.status(ctx, p, projectID, env, name, nil)
			if serr != nil {
				return nil, fmt.Errorf("model %s: %w", name, serr)
			}
			out = append(out, st)
		}
	}
	return out, nil
}

// Status says how one model in the environment is. ai:read.
func (s *Service) Status(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*Status, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	row, err := s.rowOf(ctx, p.OrgID, projectID, env.ID, resource)
	if err != nil {
		return nil, err
	}
	return s.status(ctx, p, projectID, env, resource, row)
}

// rowOf reads a deployment row, nil when there's none.
func (s *Service) rowOf(ctx context.Context, orgID, projectID, environmentID, resource string) (*Deployment, error) {
	if s.d.Store == nil {
		return nil, nil
	}
	d, err := s.d.Store.Get(ctx, orgID, projectID, environmentID, resource)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return d, err
}

// manifestOf reads the project's current manifest as p, nil when it has
// none.
func (s *Service) manifestOf(ctx context.Context, p security.Principal, projectID string) (*manifest.Manifest, error) {
	if s.d.Manifests == nil {
		return nil, nil
	}
	rev, err := s.d.Manifests.Current(ctx, p, projectID)
	if errors.Is(err, manifests.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rev.Manifest, nil
}

// status works a model's status out. The caller has checked p may read
// it. row may be nil, for a model the manifest has and no deploy recorded.
func (s *Service) status(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, resource string, row *Deployment) (*Status, error) {
	if s.d.Deployed == nil {
		return nil, errors.New("models: deployment facts aren't configured")
	}
	m, err := s.manifestOf(ctx, p, projectID)
	if err != nil {
		return nil, err
	}
	// raw is the resource as the manifest has it, and r the copy env
	// runs: its own target, GPU and settings.
	var raw manifest.Resource
	var r *manifest.Resource
	if m != nil {
		if res, ok := m.Resources[resource]; ok && recipes.IsModelKind(res.Kind) {
			raw = res
			if enabledIn(res, env.Name) {
				here := res.In(env.Name, m.Defaults)
				r = &here
			}
		}
	}
	if r == nil && row == nil {
		return nil, fmt.Errorf("%w: %s has no model %s", ErrNotFound, env.Name, resource)
	}
	now := s.d.Now()
	rows, _ := s.projectRows(ctx, p.OrgID, projectID, "")
	st := &Status{
		Deployment: row, ProjectID: projectID, Resource: resource,
		Environments: copiesOf(raw, s.environmentsOf(ctx, p.OrgID, projectID, env), env, waitingIn(rows, resource)),
		Linked:       []LinkedService{}, MonthToDateUSD: monthToDate(row, now),
	}

	settings, override := map[string]string{}, ""
	switch {
	case r != nil:
		st.Kind, settings, st.ConnectionID = r.Kind, r.Settings, r.Target
		if g := r.GPU; g != nil {
			st.GPU = &provider.GPURequest{Kind: g.Kind, Count: g.Count}
		}
		if pin, perr := pinStatusOf(s.pinOf(resource, *r)); perr == nil && pin.UpdateAvailable {
			st.UpdateAvailable, st.Pin = true, pin
		}
	case row.Request != nil:
		// Waiting for a GPU server: the manifest gets it once it's up.
		st.Kind, _ = recipes.ModelKindFor(row.Engine, row.Request.Target.GPU != nil)
		if g := row.Request.Target.GPU; g != nil {
			st.GPU = &provider.GPURequest{Kind: g.Kind, Count: g.Count}
		}
	}
	if row != nil {
		override = row.HourlyUSDOverride
	}
	entryID := settings["entry"]
	if entryID == "" && row != nil {
		entryID = row.EntryID
	}
	var entry registry.Entry
	var quant registry.Quantization
	if v, lerr := s.lookup(ctx, p.OrgID, entryID); lerr == nil {
		entry, st.Entry = v.entry, v.summary
		qid := settings["quantization"]
		if qid == "" && row != nil {
			qid = row.Quantization
		}
		quant, _ = entry.Quantization(qid)
	}
	st.EngineVersion = engines.Current()[quant.Engine]
	if n, aerr := strconv.Atoi(settings["max_model_len"]); aerr == nil {
		st.MaxModelLen = n
	}
	if quant.ID != "" {
		st.Verification = s.verification(entry, quant, st.Kind, st.GPU)
	}

	fact, err := s.d.Deployed.State(ctx, p.OrgID, projectID, env.ID, resource)
	if err != nil {
		return nil, err
	}
	st.PlanID = fact.PlanID
	if fact.ConnectionID != "" {
		st.ConnectionID = fact.ConnectionID
	}
	if row != nil && (row.Phase == PhaseWaitingForServer || row.Phase == PhaseNeedsAttention) {
		st.State, st.Message = row.Phase, row.Message
	} else {
		st.State, st.Message = fact.Value, fact.Message
		if st.Message == "" && row != nil {
			st.Message = row.Message
		}
	}
	if st.State == StateFailed {
		repo, _ := weightsOf(entry, quant)
		st.Hint = failureHint(fact.Message, st.MaxModelLen, repo)
		if fact.Bound {
			st.Hint = strings.TrimSpace(st.Hint + " " + hintStillServes)
		}
	}
	if fact.Bound {
		if u, uerr := s.d.Deployed.URL(ctx, p.OrgID, env.ID, resource); uerr == nil {
			st.InternalURL = u
		}
	}

	names := s.connectionNames(ctx, p)
	st.ConnectionName = names[st.ConnectionID]
	if st.ConnectionName == "" && row != nil {
		st.ConnectionName = row.ServerName
	}
	bindings, err := s.d.Deployed.Bindings(ctx, p.OrgID, env.ID)
	if err != nil {
		return nil, err
	}
	st.Linked = linked(env, m, resource, st.ConnectionID, names, bindings)
	st.Cost = s.costOf(ctx, p, st.Kind, st.GPU, st.ConnectionID, override)
	if ownsServer(row) && gpuKind(st.Kind) && st.GPU != nil && st.ConnectionID != "" {
		// A server Loomplane created for the model is all the model's
		// until another workload holds a GPU there.
		own := 0
		if b, engine := engineBinding(bindings, resource); b != nil && b.ConnectionID == st.ConnectionID && engine.GPU != nil && engine.GPU.Kind == st.GPU.Kind {
			own = engine.GPU.Count
		}
		if c, ok := wholeServerCost(s.gpuTarget(ctx, p, st.ConnectionID, st.GPU.Kind), own, override); ok {
			st.Cost = c
		}
	}
	if r == nil && row.Request != nil && row.Request.Target.Source == TargetServerSize {
		// Waiting for a new server: the whole server is the model's, at the
		// size's price.
		st.Cost = s.sizeCost(ctx, p, row.Request.Target, override)
	}
	if row != nil && waitsForServer(row) && row.Request != nil && row.Request.Moving != nil && row.Request.Target.Source == TargetServerSize {
		// The new server is the move's, at its size's price. The override is
		// what the owner says the GPUs it runs on now cost, so it isn't used.
		c := s.sizeCost(ctx, p, row.Request.Target, "")
		st.MoveCost = &c
	}
	st.ProjectedUSD = projected(st.Cost, st.MonthToDateUSD, now)
	if allowed, aerr := s.d.Authz.Allowed(ctx, p, writeNeed(env)); aerr == nil {
		st.CanEdit = allowed
	}
	return st, nil
}

// homeReach is the environment of a model that runs in that one alone.
func homeReach(env *projects.Environment) Reach {
	return Reach{EnvironmentID: env.ID, Environment: env.Name, Mode: ReachHome,
		Message: "Runs in " + env.Name + ". Services there link to it and get MODEL_URL and MODEL_API_KEY."}
}

// verification is q's fit on the GPUs the model runs on, or on CPU, from
// the memory math and the qualification results: for a llama.cpp GGUF too,
// whose quantizations have no min_gpu_memory_gib.
func (s *Service) verification(e registry.Entry, q registry.Quantization, kind string, gpu *provider.GPURequest) registry.Fit {
	if gpuKind(kind) {
		if gpu == nil {
			return registry.Fit{Status: registry.StatusNoFit, Reason: "It has no GPU to run on."}
		}
		return s.d.Registry.FitOn(e, q, gpu.Kind, max(gpu.Count, 1), engines.Current())
	}
	return s.d.Registry.FitOn(e, q, registry.CPUKind, 0, engines.Current())
}

// failureHint says what to try after a failed start, from its message.
func failureHint(msg string, maxModelLen int, repo string) string {
	switch {
	case strings.Contains(msg, "out of memory") || strings.Contains(msg, "OutOfMemoryError"):
		if maxModelLen <= 0 {
			return hintMemoryNoLen
		}
		return fmt.Sprintf(hintMemory, maxModelLen)
	case strings.Contains(msg, "huggingface.co") && (strings.Contains(msg, "401") || strings.Contains(msg, "403")) && repo != "":
		return fmt.Sprintf("Your Hugging Face account hasn't accepted the license for %s. Accept it at %s/%s, then try again.", repo, hfhub.DefaultURL, repo)
	}
	return ""
}

// monthToDate is what the row says the model cost this month, "0.0000"
// when it has nothing for this month.
func monthToDate(row *Deployment, now time.Time) string {
	if row == nil || row.MonthCostUSD == "" || row.Month != now.UTC().Format("2006-01") {
		return "0.0000"
	}
	return row.MonthCostUSD
}

// projected is month to date plus the hourly price for what's left of the
// month, to the cent, half up. Empty when the price is unknown, and for a
// CPU model, whose cost is its server's.
func projected(c Cost, monthToDate string, now time.Time) string {
	if !c.Known || c.Source == CostSourceCPU {
		return ""
	}
	hourly, err := costAmount.parse(c.HourlyUSD)
	if err != nil {
		return ""
	}
	spent, err := costAmount.parse(monthToDate)
	if err != nil {
		return ""
	}
	now = now.UTC()
	next := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	seconds := int64(next.Sub(now) / time.Second)
	units := spent + (hourly*seconds+1800)/3600
	cents := (units + 50) / 100
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// costOf is what the model's GPUs cost per hour (Ruling M25). A CPU kind
// holds no GPU, so its cost is part of its server's. A GPU kind whose
// GPU count or host price isn't known is unknown, never "0.00". An
// override wins either way.
func (s *Service) costOf(ctx context.Context, p security.Principal, kind string, gpu *provider.GPURequest, connectionID, override string) Cost {
	if kind != "" && !gpuKind(kind) {
		return HourlyCost(gpus.GPUTarget{}, 0, override)
	}
	if gpu == nil || gpu.Count <= 0 {
		if c := HourlyCost(gpus.GPUTarget{}, 0, override); c.Source == CostSourceOverride {
			return c
		}
		return Cost{Source: CostSourceUnknown}
	}
	return HourlyCost(s.gpuTarget(ctx, p, connectionID, gpu.Kind), gpu.Count, override)
}

// sizeCost is what the server a server size target creates costs per
// hour: the size's list price for all its GPUs, unknown when the size or
// its price isn't known, and the override when there is one.
func (s *Service) sizeCost(ctx context.Context, p security.Principal, t TargetChoice, override string) Cost {
	if c := HourlyCost(gpus.GPUTarget{}, 0, override); c.Source == CostSourceOverride {
		return c
	}
	if s.d.GPUs == nil || t.GPU == nil {
		return Cost{Source: CostSourceUnknown}
	}
	list, err := s.d.GPUs.Targets(ctx, p, gpus.GPUTargetQuery{Count: 1})
	if err != nil {
		return Cost{Source: CostSourceUnknown}
	}
	for _, row := range list.Targets {
		if row.Source == gpus.SourceServerSize && row.ConnectionID == t.ConnectionID && row.SizeID == t.SizeID && row.Accelerator.Kind == t.GPU.Kind {
			return serverCost(row.Accelerator, row.HourlyUSD, row.HourlyPriceKnown)
		}
	}
	return Cost{Source: CostSourceUnknown}
}

// gpuTarget is the GPU target row of a connection's GPU kind, the zero
// row when it can't be read.
func (s *Service) gpuTarget(ctx context.Context, p security.Principal, connectionID, kind string) gpus.GPUTarget {
	if s.d.GPUs == nil || connectionID == "" {
		return gpus.GPUTarget{}
	}
	list, err := s.d.GPUs.Targets(ctx, p, gpus.GPUTargetQuery{Count: 1})
	if err != nil {
		return gpus.GPUTarget{}
	}
	for _, t := range list.Targets {
		if t.Source == gpus.SourceConnection && t.ConnectionID == connectionID && t.Accelerator.Kind == kind {
			return t
		}
	}
	return gpus.GPUTarget{}
}

// connectionNames names the workspace's connections by ID, as p sees
// them; empty when they can't be read.
func (s *Service) connectionNames(ctx context.Context, p security.Principal) map[string]string {
	out := map[string]string{}
	if s.d.Connections == nil {
		return out
	}
	conns, err := s.d.Connections.List(ctx, p)
	if err != nil {
		return out
	}
	for _, c := range conns {
		out[c.ID] = c.Name
	}
	return out
}

// linked lists the services that need the model and whether each reaches
// it. A model runs in one environment until the gateway, so that's the
// one: a service there reaches it on the same server or cluster.
func linked(env *projects.Environment, m *manifest.Manifest, resource, modelConn string, names map[string]string, bindings []*planning.Binding) []LinkedService {
	out := []LinkedService{}
	if m == nil {
		return out
	}
	name := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	for _, svcName := range slices.Sorted(maps.Keys(m.Services)) {
		svc := m.Services[svcName]
		if !slices.Contains(svc.Needs, resource) {
			continue
		}
		ls := LinkedService{Environment: env.Name, Service: svcName}
		conn := serviceConnection(svcName, svc, env.Name, m.Defaults, bindings)
		switch {
		case modelConn == "":
			ls.Message = fmt.Sprintf("%s isn't placed on a server or cluster yet, so %s gets MODEL_URL once it is.", resource, svcName)
		case conn == "":
			ls.Message = fmt.Sprintf("%s doesn't run on a server or cluster, so it can't reach %s on %s. Run %s on %s.", svcName, resource, name(modelConn), svcName, name(modelConn))
		case conn != modelConn:
			ls.Message = fmt.Sprintf("%s runs on %s, and %s runs on %s. Run them on the same server or cluster.", svcName, name(conn), resource, name(modelConn))
		default:
			ls.Reachable = true
		}
		out = append(out, ls)
	}
	return out
}

// serviceConnection is the server or cluster a service runs on in env: its
// binding's, or the manifest's target until it's deployed. "" when it
// runs on neither.
func serviceConnection(name string, svc manifest.Service, env string, d manifest.Defaults, bindings []*planning.Binding) string {
	for _, b := range bindings {
		if b.Subject == planning.ServiceSubject && b.Name == name && b.ConnectionID != "" {
			return b.ConnectionID
		}
	}
	return svc.For(env, d).Target
}

// Update changes a model's monthly budget and its hourly price override.
// It writes the row only: nothing about what runs changes, so nothing is
// planned. It needs WriteNeed of the environment.
func (s *Service) Update(ctx context.Context, p security.Principal, projectID, environmentID, resource string, in UpdateInput) (*Status, error) {
	env, row, err := s.changeRow(ctx, p, projectID, environmentID, resource)
	if err != nil {
		return nil, err
	}
	if in.Revision != row.Revision {
		return nil, fmt.Errorf("%w: %s changed since you read it. Load it again", ErrStale, resource)
	}
	if in.MonthlyBudgetUSD != nil {
		budget := strings.TrimSpace(*in.MonthlyBudgetUSD)
		if budget != "" {
			units, perr := budgetAmount.parse(budget)
			if perr != nil || units <= 0 || units > maxBudgetUSD*unitsPerUSD {
				return nil, fmt.Errorf("%w: a monthly budget is an amount in dollars above 0 and at most %d, like 250.00", ErrInvalid, maxBudgetUSD)
			}
		}
		row.MonthlyBudgetUSD = budget
	}
	if in.HourlyUSDOverride != nil {
		override := strings.TrimSpace(*in.HourlyUSDOverride)
		if override != "" {
			if _, perr := overrideAmount.parse(override); perr != nil {
				return nil, fmt.Errorf("%w: an hourly price is an amount in dollars, like 1.57", ErrInvalid)
			}
		}
		row.HourlyUSDOverride = override
	}
	if uerr := s.d.Store.Update(ctx, row); uerr != nil {
		return nil, uerr
	}
	s.audit(ctx, p, projectID, "ai.model.update", map[string]any{
		"resource": resource, "environment": env.Name,
		"monthly_budget_usd": row.MonthlyBudgetUSD, "hourly_usd_override": row.HourlyUSDOverride,
	})
	return s.status(ctx, p, projectID, env, resource, row)
}

// changeRow reads the row of a model the caller may change: WriteNeed of
// the environment, checked first.
func (s *Service) changeRow(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*projects.Environment, *Deployment, error) {
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, nil, err
	}
	if aerr := s.d.Authz.Require(ctx, p, writeNeed(env), ""); aerr != nil {
		return nil, nil, aerr
	}
	if s.d.Store == nil {
		return nil, nil, errors.New("models: deployments aren't configured")
	}
	row, err := s.d.Store.Get(ctx, p.OrgID, projectID, env.ID, resource)
	if err != nil {
		return nil, nil, err
	}
	return env, row, nil
}

// waitingRefusal refuses a change to a model that waits for a GPU server.
func waitingRefusal(row *Deployment, what string) error {
	if row == nil || (row.Phase != PhaseWaitingForServer && row.Phase != PhaseNeedsAttention) {
		return nil
	}
	return fmt.Errorf("%w: %s is waiting for its GPU server. Cancel that first, then %s it", ErrInvalid, row.Resource, what)
}

// MovePreview says what moving the model would do, as Preview does for a
// deploy. ai:read; it writes nothing, except the review of a GPU server
// when in.ReviewServer asks for one.
func (s *Service) MovePreview(ctx context.Context, p security.Principal, projectID, environmentID, resource string, in MoveInput) (*DeployPreview, error) {
	if err := s.d.Authz.Require(ctx, p, security.AIRead, ""); err != nil {
		return nil, err
	}
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	_, din, err := s.moveRequest(ctx, p, projectID, env, resource, in)
	if err != nil {
		return nil, err
	}
	pv, err := s.preview(ctx, p, projectID, env, din, false)
	if err != nil {
		return nil, err
	}
	if cerr := s.cacheProblem(ctx, p, projectID, env, pv); cerr != nil {
		return nil, cerr
	}
	allowed, err := s.d.Authz.Allowed(ctx, p, writeNeed(env))
	if err != nil {
		return nil, err
	}
	pv.CanDeploy = len(pv.Problems) == 0 && allowed && !pv.ServerReviewNeeded
	return pv, nil
}

// moveRequest reads the model a move moves and turns the move into the
// deploy it amounts to.
func (s *Service) moveRequest(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, resource string, in MoveInput) (*Deployment, DeployInput, error) {
	// r is env's copy: the move's edit changes it alone (moveResource).
	r, _, err := s.modelResource(ctx, p, projectID, env, resource)
	if err != nil {
		return nil, DeployInput{}, err
	}
	row, err := s.rowOf(ctx, p.OrgID, projectID, env.ID, resource)
	if err != nil {
		return nil, DeployInput{}, err
	}
	if werr := waitingRefusal(row, "move"); werr != nil {
		return nil, DeployInput{}, werr
	}
	from := &MovingFrom{Kind: r.Kind, ConnectionID: r.Target}
	// The GPUs it gives back are the ones its deployed engine holds, never
	// what the manifest asks for: a manifest edited and not deployed yet
	// holds nothing more, and one never deployed holds nothing.
	if s.d.Deployed != nil {
		bindings, berr := s.d.Deployed.Bindings(ctx, p.OrgID, env.ID)
		if berr != nil {
			return nil, DeployInput{}, berr
		}
		if b, cfg := engineBinding(bindings, resource); b != nil && b.ConnectionID == r.Target && cfg.GPU != nil {
			from.GPUKind, from.GPUs = cfg.GPU.Kind, cfg.GPU.Count
		}
	}
	if row != nil {
		from.Phase, from.ServerName, from.PlanID = row.Phase, row.ServerName, row.PlanID
		from.ServerCloudID, from.ServerOperationID = row.ServerCloudID, row.ServerOperationID
	}
	return row, DeployInput{
		EntryID: r.Settings["entry"], Quantization: strings.TrimSpace(in.Quantization), Target: in.Target, Resource: resource,
		MaxModelLen: in.MaxModelLen, ReviewServer: in.ReviewServer, Server: in.Server, Moving: from,
	}, nil
}

// Move moves the model onto another server or cluster, reusing the
// deploy's rules for the new target, and plans its environment. The edit
// changes the resource's target, provider, GPUs and settings, and keeps
// its name and served name, so linked services keep their variables. A
// GPU server size target creates the server first, as a deploy does: the
// move waits for it, the model keeps running where it is meanwhile, and
// the reconcile finishes the move once the server is up. It needs
// WriteNeed of the environment.
func (s *Service) Move(ctx context.Context, p security.Principal, projectID, environmentID, resource string, in MoveInput) (*DeployResult, error) {
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	if aerr := s.d.Authz.Require(ctx, p, writeNeed(env), ""); aerr != nil {
		return nil, aerr
	}
	if s.d.Manifests == nil || s.d.Plans == nil {
		return nil, errors.New("models: moving isn't configured")
	}
	row, din, err := s.moveRequest(ctx, p, projectID, env, resource, in)
	if err != nil {
		return nil, err
	}
	pv, err := s.preview(ctx, p, projectID, env, din, true)
	if err != nil {
		return nil, err
	}
	if cerr := s.cacheProblem(ctx, p, projectID, env, pv); cerr != nil {
		return nil, cerr
	}
	if len(pv.Problems) > 0 {
		return nil, &ProblemError{Problem: pv.Problems[0]}
	}
	if pv.Server != nil {
		return s.moveServerFirst(ctx, p, projectID, env, din, pv, row)
	}
	rev, err := s.moveResource(ctx, p, projectID, env, pv)
	if err != nil {
		return nil, err
	}
	var res *DeployResult
	left := ""
	if row != nil {
		if pv.Target.ConnectionID != din.Moving.ConnectionID {
			// Off the server: a server Loomplane created for the model stays.
			left = leftServer(row)
			row.ServerCloudID, row.ServerOperationID = "", ""
		}
		row.Phase, row.Quantization, row.Engine, row.ServerName = PhasePlanned, pv.quant.ID, string(pv.quant.Engine), pv.TargetName
		row.Message = ""
		res, err = s.planRow(ctx, p, projectID, env, rev, row, true)
	} else {
		res, err = s.plan(ctx, p, projectID, env, rev, &PlanAfterEditError{Resource: resource, Environment: env.Name, Move: true})
	}
	if err != nil {
		return res, err
	}
	s.modelsChanged(ctx, p.OrgID, projectID)
	if left != "" {
		res.Message = left
		if merr := s.sayOnRow(ctx, row, left); merr != nil {
			return res, merr
		}
	}
	return res, nil
}

// retryableServer refuses a Retry of row while its GPU server's operation
// is still a review nobody's create went through, as after the capacity
// policy refused it. The reconcile approves a review again only for a row
// the capacity policy paused, so a Retry there would wait until the review
// expired. Any other operation goes on to the reconcile, which says what's
// wrong with it.
func (s *Service) retryableServer(ctx context.Context, p security.Principal, row *Deployment) error {
	if s.d.Servers == nil {
		return nil
	}
	op, err := s.d.Servers.GetOperation(ctx, p, row.ServerCloudID, row.ServerOperationID)
	switch {
	case errors.Is(err, security.ErrForbidden) || errors.Is(err, cloudinfra.ErrNotFound):
		return nil
	case err != nil:
		return err
	case op.Status == "preview":
		return fmt.Errorf("%w: %s was never created, and Retry can't approve its review again. Cancel this and deploy the model again to review a new server",
			ErrInvalid, serverOf(row))
	}
	return nil
}

// leftServer says that the GPU server Loomplane created for d keeps
// running once the model moves off it, "" when Loomplane created none.
func leftServer(d *Deployment) string {
	if d == nil || d.ServerCloudID == "" {
		return ""
	}
	return fmt.Sprintf("%s keeps running and costs money until you delete it in Infrastructure.", serverOf(d))
}

// createdServer is the name of the GPU server Loomplane created for d, ""
// when it created none.
func createdServer(d *Deployment) string {
	if d == nil || d.ServerCloudID == "" {
		return ""
	}
	return serverOf(d)
}

// sayOnRow puts msg on d, so the model's page shows it.
func (s *Service) sayOnRow(ctx context.Context, d *Deployment, msg string) error {
	d.Message = msg
	if err := s.d.Store.Update(context.WithoutCancel(ctx), d); err != nil {
		return fmt.Errorf("the move is planned, but its deployment row doesn't say so: %w", err)
	}
	return nil
}

// plan plans env from rev, for a model without a row. A failure is
// perr, with the planner's error.
func (s *Service) plan(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, rev *manifests.Revision, perr *PlanAfterEditError) (*DeployResult, error) {
	plan, err := s.d.Plans.Create(ctx, p, projectID, planning.CreatePlanInput{Environment: env.Name, Catalog: rev.Manifest.CatalogOnly()})
	if err != nil {
		perr.err = err
		return nil, perr
	}
	return &DeployResult{PlanID: plan.ID}, nil
}

// moveResource makes a move's one manifest edit: the provider, target,
// GPUs and settings of env's copy for the preview's target (moveUpdate).
// It keeps the served name, so callers keep asking for the same model,
// and never shrinks the weight cache. A setting the new quantization
// doesn't have, a GGUF file, is deleted.
func (s *Service) moveResource(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, pv *DeployPreview) (*manifests.Revision, error) {
	cur, m, err := s.modelResource(ctx, p, projectID, env, pv.Resource)
	if err != nil {
		return nil, err
	}
	settings := moveSettings(cur, pv)
	refusal, err := s.cacheRefusal(ctx, p.OrgID, env, cur, pv, settings)
	if err != nil {
		return nil, err
	}
	if refusal != "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, refusal)
	}
	update := moveUpdate(m.Resources[pv.Resource], cur, env.Name, m.Defaults, pv, settings)
	rev, _, err := s.d.Manifests.Edit(ctx, p, projectID, manifests.EditInput{UpdateResource: update})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, p, projectID, "ai.model.move", map[string]any{
		"resource": pv.Resource, "environment": env.Name, "from": cur.Target, "target": pv.Target.ConnectionID,
		"quantization": pv.quant.ID, "manifest_revision": rev.Revision,
	})
	return rev, nil
}

// moveSettings are the settings a move to pv's target and quantization
// gives env's copy cur: the new quantization's, with cur's served name,
// cur's weight cache when it's bigger, and "" for each setting of cur's
// the new quantization doesn't have, which the edit deletes.
func moveSettings(cur manifest.Resource, pv *DeployPreview) map[string]string {
	settings := engines.Settings(pv.spec)
	if served := cur.Settings["served_name"]; served != "" {
		settings["served_name"] = served
	}
	if have, perr := strconv.ParseInt(cur.Settings["cache_mb"], 10, 64); perr == nil {
		if need, nerr := strconv.ParseInt(settings["cache_mb"], 10, 64); nerr == nil && have > need {
			settings["cache_mb"] = cur.Settings["cache_mb"]
		}
	}
	for name := range cur.Settings {
		if _, ok := settings[name]; !ok {
			settings[name] = ""
		}
	}
	return settings
}

// cacheRefusal says why env's copy cur can't move to pv's target with
// settings, "" when it can: the new quantization needs a bigger weight
// cache than cur has, and cur keeps it on a Kubernetes volume on that
// same cluster, which can't grow in place. A move onto another target
// gets a new volume.
func (s *Service) cacheRefusal(ctx context.Context, orgID string, env *projects.Environment, cur manifest.Resource, pv *DeployPreview, settings map[string]string) (string, error) {
	need, err := strconv.ParseInt(settings["cache_mb"], 10, 64)
	if err != nil || !cacheGrows(cur.Kind, cur.Settings, need) {
		return "", nil
	}
	fixed, err := s.fixedCache(ctx, orgID, env.ID, pv.Resource, pv.Target.ConnectionID)
	if err != nil || !fixed {
		return "", err
	}
	return fmt.Sprintf("%s keeps its weight cache on a Kubernetes volume, which can't grow in place, and %s needs a bigger one. Remove %s from %s and deploy it again to move it",
		pv.Resource, pv.Quantization, pv.Resource, env.Name), nil
}

// cacheProblem adds cacheRefusal to a move's preview as a problem, so the
// preview never offers a move that Move refuses. A preview that already
// has a problem has no settings to check.
func (s *Service) cacheProblem(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, pv *DeployPreview) error {
	if len(pv.Problems) > 0 || pv.Target.Source == TargetServerSize {
		return nil
	}
	cur, _, err := s.modelResource(ctx, p, projectID, env, pv.Resource)
	if err != nil {
		return err
	}
	refusal, err := s.cacheRefusal(ctx, p.OrgID, env, cur, pv, moveSettings(cur, pv))
	if err != nil {
		return err
	}
	if refusal != "" {
		pv.Problems = append(pv.Problems, Problem{Code: ProblemTarget, Message: refusal + "."})
	}
	return nil
}

// moveUpdate is a move's edit of raw for env, whose copy is cur, to the
// preview's target with settings. It edits the top level, as a model in
// one environment always has, when env is the only environment raw runs
// in that reads every field the edit changes there (soleReader), so no
// other copy moves. Otherwise it edits env's own block, writing only what
// env doesn't already read.
func moveUpdate(raw, cur manifest.Resource, env string, d manifest.Defaults, pv *DeployPreview, settings map[string]string) *manifests.ResourceUpdateInput {
	top := &manifests.ResourceUpdateInput{Resource: pv.Resource, Provider: &pv.Target.Provider, Target: &pv.Target.ConnectionID, Settings: settings}
	var gpu *manifest.GPU
	switch {
	case pv.GPU != nil:
		gpu = &manifest.GPU{Kind: pv.GPU.Kind, Count: pv.GPU.Count}
		top.GPU = gpu
	case cur.GPU != nil:
		top.ClearGPU = true
	}
	// What the edit changes: a field whose new value differs from the
	// top level's, or from env's own block where that sets it. A field
	// env's block sets can't move at the top level, so soleReader sends
	// the edit to the block then.
	block := raw.Environments[env]
	sameGPU := func(a, b *manifest.GPU) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	changed := []string{"target"}
	if pv.Target.Provider != raw.Provider || (block.Provider != "" && block.Provider != pv.Target.Provider) {
		changed = append(changed, "provider")
	}
	if !sameGPU(gpu, raw.GPU) || (block.GPU != nil && !sameGPU(gpu, block.GPU)) {
		changed = append(changed, "gpu")
	}
	for _, key := range slices.Sorted(maps.Keys(settings)) {
		own, set := block.Settings[key]
		if settings[key] != raw.Settings[key] || (set && own != settings[key]) {
			changed = append(changed, key)
		}
	}
	if soleReader(raw, env, changed...) {
		return top
	}
	return environmentUpdate(raw, env, d, pv, settings)
}

// moveServerFirst moves the model onto a GPU server Loomplane creates
// first. The row says it waits for the server, with what it was before,
// and then the server is approved; the manifest isn't touched until the
// server is up. The row's revision is checked on the way, so two moves at
// once approve one server, never two.
func (s *Service) moveServerFirst(ctx context.Context, p security.Principal, projectID string, env *projects.Environment, in DeployInput, pv *DeployPreview, row *Deployment) (*DeployResult, error) {
	if s.d.Servers == nil || s.d.Store == nil {
		return nil, errors.New("models: creating GPU servers isn't configured")
	}
	op := pv.Server
	if row == nil {
		// A model added by hand gets its row now, as it runs.
		r, _, err := s.modelResource(ctx, p, projectID, env, pv.Resource)
		if err != nil {
			return nil, err
		}
		_, q, _ := registryQuantization(s.d.Registry, r.Settings)
		row = &Deployment{
			OrgID: p.OrgID, ProjectID: projectID, EnvironmentID: env.ID, Resource: pv.Resource, EntryID: pv.entry.ID,
			Quantization: r.Settings["quantization"], Engine: string(q.Engine), Phase: PhaseActive, RequestedBy: p,
		}
		if row.Engine == "" {
			row.Engine = string(pv.quant.Engine)
		}
		if cerr := s.createRow(ctx, p.OrgID, pv.entry, row); cerr != nil {
			return nil, cerr
		}
	}
	before := *row
	req := in
	req.Resource, req.Quantization, req.ReviewServer = pv.Resource, pv.Quantization, false
	req.Server = &ServerApproval{CloudID: op.CloudID, OperationID: op.ID, Review: op.Review}
	row.Phase, row.ServerCloudID, row.ServerOperationID, row.ServerName = PhaseWaitingForServer, op.CloudID, op.ID, op.Input.Name
	row.Request, row.RequestedBy, row.Message = &req, p, ""
	// The row comes first: a server that's paid for always has one.
	if err := s.d.Store.Update(ctx, row); err != nil {
		return nil, err
	}
	undo := func(ctx context.Context) error {
		back := before
		back.Revision = row.Revision
		if err := s.d.Store.Update(ctx, &back); err != nil {
			return err
		}
		*row = back
		return nil
	}
	if err := s.giveWayOnName(ctx, row, undo); err != nil {
		return nil, err
	}
	res, err := s.applyServer(ctx, p, projectID, env, pv, row, undo)
	// Only an approved server moves the model off the old one. A move the
	// capacity policy paused says so on the row, and one that failed moves
	// nothing.
	if err == nil && res != nil && row.Message == "" {
		res.Message = leftServer(&before)
	}
	return res, err
}

// Remove takes the environment's copy of the model out and plans the
// environment. While other environments run the model, it turns the
// environment off in the resource and clears its block (removalUpdate),
// and goes ahead even when services there link to it (Decision 4): their
// plan warns need-off for each one, and the result says so. Removing the
// last environment removes the resource, and refuses, with ErrInUse,
// while a service needs it, naming each one. The row goes once the plan
// is made. The weights stay on the server as retained storage, and the
// result says so. It needs WriteNeed of the environment.
func (s *Service) Remove(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*DeployResult, error) {
	env, err := s.environment(ctx, p.OrgID, projectID, environmentID)
	if err != nil {
		return nil, err
	}
	if aerr := s.d.Authz.Require(ctx, p, writeNeed(env), ""); aerr != nil {
		return nil, aerr
	}
	if s.d.Manifests == nil || s.d.Plans == nil {
		return nil, errors.New("models: removing isn't configured")
	}
	row, err := s.rowOf(ctx, p.OrgID, projectID, env.ID, resource)
	if err != nil {
		return nil, err
	}
	if werr := waitingRefusal(row, "remove"); werr != nil {
		return nil, werr
	}
	rev, err := s.d.Manifests.Current(ctx, p, projectID)
	if err != nil {
		return nil, err
	}
	r, ok := rev.Manifest.Resources[resource]
	if !ok || !recipes.IsModelKind(r.Kind) || !enabledIn(r, env.Name) {
		return nil, fmt.Errorf("%w: %s has no model %s", ErrNotFound, env.Name, resource)
	}
	users := servicesNeeding(rev.Manifest, resource)
	last := !slices.ContainsFunc(slices.Collect(maps.Keys(r.Environments)), func(name string) bool { return name != env.Name && enabledIn(r, name) })
	if last && len(users) > 0 {
		verb, them := "links", "it"
		if len(users) > 1 {
			verb, them = "link", "them"
		}
		return nil, fmt.Errorf("%w: %s in %s %s to it. Unlink %s first", ErrInUse, joinNames(users), env.Name, verb, them)
	}
	server := s.connectionNames(ctx, p)[r.For(env.Name, rev.Manifest.Defaults).Target]
	if server == "" && row != nil {
		server = row.ServerName
	}
	edit := manifests.EditInput{RemoveResource: resource}
	if !last {
		edit = manifests.EditInput{UpdateResource: removalUpdate(r, env.Name)}
		edit.UpdateResource.Resource = resource
	}
	edited, _, err := s.d.Manifests.Edit(ctx, p, projectID, edit)
	if err != nil {
		return nil, err
	}
	attrs := map[string]any{"resource": resource, "environment": env.Name, "manifest_revision": edited.Revision}
	if !last {
		attrs["removes_environment"] = true
	}
	s.audit(ctx, p, projectID, "ai.model.remove", attrs)
	plan, err := s.d.Plans.Create(ctx, p, projectID, planning.CreatePlanInput{Environment: env.Name, Catalog: edited.Manifest.CatalogOnly()})
	if err != nil {
		perr := &PlanAfterEditError{Resource: resource, Environment: env.Name, Removal: true, copyOnly: !last, err: err}
		if row == nil {
			return nil, perr
		}
		row.Message = perr.removed() + fmt.Sprintf(" Plan %s from the project to remove it.", env.Name)
		if uerr := s.d.Store.Update(context.WithoutCancel(ctx), row); uerr != nil {
			return &DeployResult{Deployment: row}, errors.Join(perr, uerr)
		}
		return &DeployResult{Deployment: row}, perr
	}
	res := &DeployResult{PlanID: plan.ID, Deployment: row, Message: weightsStay(resource, server, createdServer(row))}
	// Only services that run in env lose MODEL_URL there.
	here := slices.DeleteFunc(slices.Clone(users), func(name string) bool {
		return !manifest.On(rev.Manifest.Services[name].For(env.Name, rev.Manifest.Defaults).Enabled)
	})
	if !last && len(here) > 0 {
		res.Message += " " + needOff(here, resource, env.Name)
	}
	if row != nil {
		if derr := s.d.Store.Delete(ctx, row.OrgID, row.ProjectID, row.EnvironmentID, row.Resource); derr != nil && !errors.Is(derr, ErrNotFound) {
			s.modelsChanged(ctx, p.OrgID, projectID)
			return res, fmt.Errorf("the plan %s removes %s, but its deployment row stays: %w", plan.ID, resource, derr)
		}
	}
	s.modelsChanged(ctx, p.OrgID, projectID)
	return res, nil
}

// removalUpdate takes env's copy of raw out and leaves the rest of raw
// alone: env turned off, and its block's target, provider, GPU and
// settings cleared, so the block says only enabled: false. The caller
// sets Resource.
func removalUpdate(raw manifest.Resource, env string) *manifests.ResourceUpdateInput {
	off, none := false, ""
	u := &manifests.ResourceUpdateInput{Environment: env, Enabled: &off, Target: &none, Provider: &none}
	block := raw.Environments[env]
	u.ClearGPU = block.GPU != nil
	for _, key := range slices.Sorted(maps.Keys(block.Settings)) {
		if u.Settings == nil {
			u.Settings = map[string]string{}
		}
		u.Settings[key] = ""
	}
	return u
}

// needOff says that the services users of env lose MODEL_URL once the
// plan that removes env's copy of resource is approved, and that the plan
// warns need-off for them.
func needOff(users []string, resource, env string) string {
	verb, them := "gets", "it"
	if len(users) > 1 {
		verb, them = "get", "them"
	}
	return fmt.Sprintf("%s in %s %s no MODEL_URL once this plan is approved. The plan warns need-off for %s until you deploy %s to %s again or unlink %s.",
		joinNames(users), env, verb, them, resource, env, them)
}

// weightsStay says what a removed model leaves on its server, and when
// Loomplane created that server for it (made, its name), that the server
// itself stays too.
func weightsStay(resource, server, made string) string {
	if server == "" {
		server = cmp.Or(made, "its server")
	}
	msg := fmt.Sprintf("The plan removes %s. Its downloaded weights stay on %s as retained storage, so deploying it there again doesn't download them again. Delete them from the server's storage when you no longer need them.", resource, server)
	if made != "" {
		msg += fmt.Sprintf(" Loomplane created %s for this model, and it keeps running and costs money until you delete it in Infrastructure.", made)
	}
	return msg
}

// servicesNeeding lists, by name, the services of m that need resource.
func servicesNeeding(m *manifest.Manifest, resource string) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(m.Services)) {
		if slices.Contains(m.Services[name].Needs, resource) {
			out = append(out, name)
		}
	}
	return out
}

// joinNames is "a", "a and b" or "a, b and c".
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// Retry tries a model again. One whose GPU server needs attention waits
// for the same server again, carried on as the caller from now on: a
// server that failed is retried in Infrastructure first, and no second
// server is ever created. One that failed to start gets a new plan of its
// environment, from the manifest as it is. It needs WriteNeed of the
// environment.
func (s *Service) Retry(ctx context.Context, p security.Principal, projectID, environmentID, resource string) (*Status, error) {
	env, row, err := s.changeRow(ctx, p, projectID, environmentID, resource)
	if err != nil {
		return nil, err
	}
	switch {
	case row.Phase == PhaseNeedsAttention && row.ServerOperationID != "":
		if perr := s.retryableServer(ctx, p, row); perr != nil {
			return nil, perr
		}
		row.Phase, row.Message, row.RequestedBy = PhaseWaitingForServer, "", p
		if uerr := s.d.Store.Update(ctx, row); uerr != nil {
			return nil, uerr
		}
		s.audit(ctx, p, projectID, "ai.model.retry", map[string]any{"resource": resource, "operation_id": row.ServerOperationID})
		return s.status(ctx, p, projectID, env, resource, row)
	case row.Phase == PhaseWaitingForServer || row.Phase == PhaseNeedsAttention:
		return nil, fmt.Errorf("%w: %s is waiting for its GPU server, so there's nothing to retry yet", ErrInvalid, resource)
	}
	if s.d.Deployed == nil || s.d.Plans == nil || s.d.Manifests == nil {
		return nil, errors.New("models: retrying isn't configured")
	}
	fact, err := s.d.Deployed.State(ctx, p.OrgID, projectID, env.ID, resource)
	if err != nil {
		return nil, err
	}
	if fact.Value != catalogstate.Failed {
		return nil, fmt.Errorf("%w: only a model that failed to start, or whose GPU server needs attention, can be retried", ErrInvalid)
	}
	rev, err := s.d.Manifests.Current(ctx, p, projectID)
	if err != nil {
		return nil, err
	}
	if _, ok := rev.Manifest.Resources[resource]; !ok {
		return nil, fmt.Errorf("%w: the manifest has no model %s", ErrNotFound, resource)
	}
	plan, err := s.d.Plans.Create(ctx, p, projectID, planning.CreatePlanInput{Environment: env.Name, Catalog: rev.Manifest.CatalogOnly()})
	if err != nil {
		return nil, err
	}
	row.PlanID, row.Message = plan.ID, ""
	if uerr := s.d.Store.Update(ctx, row); uerr != nil {
		return nil, fmt.Errorf("the plan %s is made, but its deployment row wasn't updated: %w", plan.ID, uerr)
	}
	s.audit(ctx, p, projectID, "ai.model.retry", map[string]any{"resource": resource, "plan_id": plan.ID})
	return s.status(ctx, p, projectID, env, resource, row)
}
