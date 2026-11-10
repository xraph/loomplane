package deployments

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
)

// FrontendReaper removes the frontends a project, or one of its
// environments, deployed to Vercel before the deletion, which their
// capacity slots otherwise refuse (projects.ErrFrontendsHeld). Each
// frontend use the ledger still holds goes one of three ways:
//
//   - a deploy that never started its effects is cancelled;
//   - one whose resource is known is removed through the executor and
//     connection that made it, and once the provider confirms it's gone,
//     that absence is recorded, which releases its slot. Known means the
//     ledger names it, or, for an older deploy that never named it,
//     Loomplane's own provider environment record proves what it is;
//   - one nothing proves is released only when the provider has nothing it
//     could be. Anything it could be is named in the refusal.
//
// Everything is read and resolved before anything is removed: a frontend
// that's still deploying, or one only a person can settle, refuses the
// deletion with nothing touched. It implements projects.Preparer.
type FrontendReaper struct {
	// Ledger lists the frontends still held.
	Ledger interface {
		HeldFrontends(ctx context.Context, org, project, environmentID string) ([]cloudinfra.HeldFrontend, error)
	}
	// Plans reads the bindings and settles the ledger.
	Plans interface {
		ListBindings(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
		RecordFrontendAbsence(ctx context.Context, e cloudinfra.FrontendAbsence) error
		CancelUnstartedFrontends(ctx context.Context, org, plan string) error
	}
	// Environments names the project's environments.
	Environments interface {
		ListEnvironments(ctx context.Context, orgID, projectID string) ([]*projects.Environment, error)
	}
	// Records lists the project's provider environments.
	Records interface {
		ProviderEnvironments(ctx context.Context, orgID, projectID string) ([]*ProviderEnvironment, error)
	}
	// Credentials reads the connection that deployed each frontend.
	Credentials Credentials
	// Executors remove each frontend, by provider name.
	Executors map[string]Executor
}

var _ projects.Preparer = (*FrontendReaper)(nil)

// UnrecordedFrontend is what a frontend deploy that never recorded its
// resource made, as far as Loomplane can prove it. At most one is set.
type UnrecordedFrontend struct {
	// External is its identity, as a binding names it, when Loomplane's
	// own records prove what it is.
	External string
	// Blocked says what the provider has that it may be, which nothing
	// proves Loomplane made.
	Blocked string
	// Evidence is the name checked when the provider has nothing it could
	// be.
	Evidence string
}

// unrecordedFrontendResolver works out what a frontend deploy that never
// recorded its resource made. rec is the environment's record for the
// service, or nil.
type unrecordedFrontendResolver interface {
	UnrecordedFrontend(ctx context.Context, job *Job, rec *ProviderEnvironment) (UnrecordedFrontend, error)
}

// frontendDeletion is one deletion's frontends and where they run.
type frontendDeletion struct {
	org, project, environmentID string
	// names are the project's environment names, by ID.
	names map[string]string
	// bound are the bindings of frontends that have one, by environment,
	// service and external ID.
	bound map[string]*planning.Binding
	// records are the project's provider environment records.
	records map[string]*ProviderEnvironment
	// remove are the frontends to remove, by what they are.
	remove []heldTarget
	// absent are the frontends the provider has nothing of, with the name
	// it was checked under.
	absent []heldTarget
	// cancel are the plans whose never-started uses go unused.
	cancel []string
}

// heldTarget is a held frontend and the identity it's settled under.
type heldTarget struct {
	h        cloudinfra.HeldFrontend
	external string
}

func boundKey(environmentID, service, external string) string {
	return environmentID + "|" + service + "|" + external
}

// describe names a held frontend for a person.
func (d *frontendDeletion) describe(h cloudinfra.HeldFrontend, external string) string {
	s := h.Service + " in " + d.names[h.EnvironmentID]
	if external != "" {
		s += " (" + external + ")"
	}
	return s
}

// PrepareDelete implements projects.Preparer.
func (fr *FrontendReaper) PrepareDelete(ctx context.Context, org, project, environmentID string, opts projects.DeleteEnvironmentOptions) (projects.Prepared, error) {
	held, err := fr.Ledger.HeldFrontends(ctx, org, project, environmentID)
	if err != nil {
		return projects.Prepared{}, fmt.Errorf("read the frontends it deployed: %w", err)
	}
	if len(held) == 0 {
		return projects.Prepared{}, nil
	}
	d, err := fr.read(ctx, org, project, environmentID, held)
	if err != nil {
		return projects.Prepared{}, err
	}
	keep := map[string]bool{}
	for _, id := range opts.Keep {
		keep[id] = true
	}
	var refused []string
	cancel := map[string]bool{}
	for _, h := range held {
		switch {
		case h.Deploying:
			refused = append(refused, d.describe(h, h.ExternalID)+": it's still deploying. Let the deployment finish, or cancel it, then delete again")
		case keep[d.bindingID(h)]:
			refused = append(refused, d.describe(h, h.ExternalID)+": keeping it would leave it running and counted with nothing left to release it, so it goes with the environment")
		case h.ExternalID != "":
			d.remove = append(d.remove, heldTarget{h: h, external: h.ExternalID})
		case h.Status == "pending":
			if !cancel[h.PlanID] {
				cancel[h.PlanID] = true
				d.cancel = append(d.cancel, h.PlanID)
			}
		case h.Status != "accepted":
			refused = append(refused, d.describe(h, "")+": its deploy never named what it made")
		default:
			got, rerr := fr.resolve(ctx, d, h)
			if rerr != nil {
				return projects.Prepared{}, fmt.Errorf("find what %s made: %w", d.describe(h, ""), rerr)
			}
			switch {
			case got.External != "":
				d.remove = append(d.remove, heldTarget{h: h, external: got.External})
			case got.Blocked != "":
				refused = append(refused, d.describe(h, "")+": "+got.Blocked)
			default:
				d.absent = append(d.absent, heldTarget{h: h, external: got.Evidence})
			}
		}
	}
	if len(refused) > 0 {
		return projects.Prepared{}, fmt.Errorf("%w: %s", projects.ErrFrontendsHeld, strings.Join(refused, "; "))
	}
	removes := make([]string, 0, len(d.remove))
	for _, t := range d.remove {
		removes = append(removes, d.describe(t.h, t.external))
	}
	return projects.Prepared{Removes: removes, Run: func(ctx context.Context) error { return fr.run(ctx, d) }}, nil
}

// read gathers what the held frontends need: environment names, bindings
// and provider environment records.
func (fr *FrontendReaper) read(ctx context.Context, org, project, environmentID string, held []cloudinfra.HeldFrontend) (*frontendDeletion, error) {
	d := &frontendDeletion{org: org, project: project, environmentID: environmentID,
		names: map[string]string{}, bound: map[string]*planning.Binding{}, records: map[string]*ProviderEnvironment{}}
	envs, err := fr.Environments.ListEnvironments(ctx, org, project)
	if err != nil {
		return nil, fmt.Errorf("read the environments its frontends run in: %w", err)
	}
	for _, e := range envs {
		d.names[e.ID] = e.Name
	}
	read := map[string]bool{}
	for _, h := range held {
		if read[h.EnvironmentID] {
			continue
		}
		read[h.EnvironmentID] = true
		bindings, berr := fr.Plans.ListBindings(ctx, org, h.EnvironmentID)
		if berr != nil {
			return nil, fmt.Errorf("read what runs in %s: %w", d.names[h.EnvironmentID], berr)
		}
		for _, b := range bindings {
			d.bound[boundKey(b.EnvironmentID, b.Name, b.ExternalID)] = b
		}
	}
	if fr.Records != nil {
		recs, rerr := fr.Records.ProviderEnvironments(ctx, org, project)
		if rerr != nil {
			return nil, fmt.Errorf("read where its frontends deployed: %w", rerr)
		}
		for _, pe := range recs {
			d.records[pe.EnvironmentID+"|"+recordOf(pe.Provider, pe.ConnectionID, pe.Scope)] = pe
		}
	}
	return d, nil
}

// bindingID is the ID of h's binding, or "".
func (d *frontendDeletion) bindingID(h cloudinfra.HeldFrontend) string {
	if b := d.bound[boundKey(h.EnvironmentID, h.Service, h.ExternalID)]; b != nil {
		return b.ID
	}
	return ""
}

// resolve works out what an accepted frontend that never named its
// resource made.
func (fr *FrontendReaper) resolve(ctx context.Context, d *frontendDeletion, h cloudinfra.HeldFrontend) (UnrecordedFrontend, error) {
	exec, job, err := fr.frontendJob(ctx, d, h)
	if err != nil {
		return UnrecordedFrontend{}, err
	}
	resolver, ok := exec.(unrecordedFrontendResolver)
	if !ok {
		return UnrecordedFrontend{Blocked: "its deploy never named what it made on " + h.Provider}, nil
	}
	return resolver.UnrecordedFrontend(ctx, job, d.records[h.EnvironmentID+"|"+recordOf(h.Provider, h.ConnectionID, h.Service)])
}

// run settles every held frontend it resolved, and refuses the deletion
// with what's still held.
func (fr *FrontendReaper) run(ctx context.Context, d *frontendDeletion) error {
	for _, plan := range d.cancel {
		if err := fr.Plans.CancelUnstartedFrontends(ctx, d.org, plan); err != nil {
			return fmt.Errorf("cancel what never started: %w", err)
		}
	}
	var failed []string
	for _, t := range d.remove {
		if err := fr.remove(ctx, d, t); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", d.describe(t.h, t.external), err))
		}
	}
	for _, t := range d.absent {
		if err := fr.Plans.RecordFrontendAbsence(ctx, absence(d, t.h, t.external)); err != nil {
			failed = append(failed, fmt.Sprintf("%s: record that it's gone: %v", d.describe(t.h, ""), err))
		}
	}
	if len(failed) == 0 {
		left, err := fr.Ledger.HeldFrontends(ctx, d.org, d.project, d.environmentID)
		if err != nil {
			return fmt.Errorf("read the frontends still held: %w", err)
		}
		for _, h := range left {
			failed = append(failed, d.describe(h, h.ExternalID)+": its deploy started but never named what it made, and nothing settled it")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%w: %s", projects.ErrFrontendsHeld, strings.Join(failed, "; "))
	}
	return nil
}

// remove removes the frontend t names through the executor and connection
// that made it, confirms the provider no longer has it, and records that.
func (fr *FrontendReaper) remove(ctx context.Context, d *frontendDeletion, t heldTarget) error {
	exec, job, err := fr.frontendJob(ctx, d, t.h)
	if err != nil {
		return err
	}
	verifier, ok := exec.(frontendAbsenceVerifier)
	if !ok {
		return fmt.Errorf("nothing can confirm a %s frontend is gone", t.h.Provider)
	}
	b := d.bound[boundKey(t.h.EnvironmentID, t.h.Service, t.external)]
	if b == nil {
		// A deploy that failed before binding it: the ledger, or the
		// record, is all that names it.
		b = &planning.Binding{
			OrgID: d.org, ProjectID: d.project, EnvironmentID: t.h.EnvironmentID, Subject: planning.ServiceSubject,
			Name: t.h.Service, Provider: t.h.Provider, ConnectionID: t.h.ConnectionID, ExternalID: t.external, PlanID: t.h.PlanID,
		}
	}
	job.Binding = b
	if err = exec.Remove(ctx, job, b); err != nil {
		return fmt.Errorf("remove it: %w", err)
	}
	absent, err := verifier.FrontendAbsent(ctx, job, b)
	if err != nil {
		return fmt.Errorf("confirm it's gone: %w", err)
	}
	if !absent {
		return errors.New("the provider still has it after removal")
	}
	return fr.Plans.RecordFrontendAbsence(ctx, absence(d, t.h, t.external))
}

// frontendJob is a removal job for h, through the connection that made it.
func (fr *FrontendReaper) frontendJob(ctx context.Context, d *frontendDeletion, h cloudinfra.HeldFrontend) (Executor, *Job, error) {
	exec, ok := fr.Executors[h.Provider]
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s", ErrNoExecutor, h.Provider)
	}
	if fr.Credentials == nil {
		return nil, nil, errors.New("nothing reads the connection that deployed it")
	}
	conn, cred, err := fr.Credentials.Credential(ctx, d.org, h.ConnectionID)
	if err != nil {
		return nil, nil, fmt.Errorf("read the connection that deployed it: %w", err)
	}
	job := &Job{
		Deployment: &Deployment{OrgID: d.org, ProjectID: d.project, EnvironmentID: h.EnvironmentID, Environment: d.names[h.EnvironmentID]},
		Connection: conn, Credential: cred,
		Action: planning.Action{
			Op: planning.Delete, Subject: planning.ServiceSubject, Name: h.Service,
			Provider: h.Provider, ConnectionID: h.ConnectionID,
		},
		Progress:   func(string, string) {},
		Checkpoint: func([]byte) error { return nil },
		Keep: func(context.Context, string, []byte) error {
			return errors.New("removing a deleted frontend keeps nothing")
		},
		Recall: func(context.Context, string) ([]byte, error) { return nil, ErrNoCredential },
		// The binding reaper deletes what the environment kept in Vault.
		Forget: func(context.Context) error { return nil },
	}
	return exec, job, nil
}

func absence(d *frontendDeletion, h cloudinfra.HeldFrontend, external string) cloudinfra.FrontendAbsence {
	return cloudinfra.FrontendAbsence{
		OrgID: d.org, ProjectID: d.project, EnvironmentID: h.EnvironmentID, Service: h.Service, Provider: h.Provider,
		ConnectionID: h.ConnectionID, AccountID: h.AccountID, ExternalID: external, PlanID: h.PlanID,
	}
}

// UnrecordedFrontend implements unrecordedFrontendResolver for a deploy
// that never recorded its Vercel project:
//
//   - a native record names the shared project the deploy claimed before
//     writing to it, and the environment's role picks its environment
//     there, as the deploy did;
//   - a fallback record proves the environment's own project, named after
//     the service and environment, is Loomplane's;
//   - with no record, a project under either name blocks, unless other
//     environments' records claim the shared one: a deploy claims before
//     it writes, so this one never wrote there.
func (v *Vercel) UnrecordedFrontend(ctx context.Context, job *Job, rec *ProviderEnvironment) (UnrecordedFrontend, error) {
	tok := token(job)
	if rec != nil && rec.Mode == ModeNative && rec.ProjectRef != "" {
		return v.unrecordedNative(ctx, tok, job, rec)
	}
	own, err := v.lookupProject(ctx, tok, ProjectName(job))
	if err != nil {
		return UnrecordedFrontend{}, err
	}
	if own != nil {
		if rec != nil && rec.Mode == ModeFallback {
			return UnrecordedFrontend{External: own.ID}, nil
		}
		return UnrecordedFrontend{Blocked: fmt.Sprintf("Vercel has %s, which it may be, and nothing records that Loomplane made it. Delete it there if Loomplane did, then delete again", own.Name)}, nil
	}
	shared, err := v.lookupProject(ctx, tok, ServiceProjectName(job))
	if err != nil {
		return UnrecordedFrontend{}, err
	}
	if shared != nil {
		var claims []*ProviderEnvironment
		if v.Environments != nil {
			if claims, err = v.Environments.Store.SharingProject(ctx, job.Deployment.OrgID, "vercel", shared.ID); err != nil {
				return UnrecordedFrontend{}, err
			}
		}
		if len(claims) == 0 {
			return UnrecordedFrontend{Blocked: fmt.Sprintf("Vercel has %s, a project no environment records using, which it may be. Delete it there if Loomplane made it, then delete again", shared.Name)}, nil
		}
	}
	return UnrecordedFrontend{Evidence: ProjectName(job)}, nil
}

// unrecordedNative is the environment a native deploy that never recorded
// it deployed to, on the project its record claims.
func (v *Vercel) unrecordedNative(ctx context.Context, tok vercel.Token, job *Job, rec *ProviderEnvironment) (UnrecordedFrontend, error) {
	if rec.EnvironmentRef != "" {
		return UnrecordedFrontend{External: planning.VercelRef(rec.ProjectRef, rec.EnvironmentRef)}, nil
	}
	if v.Environments == nil {
		return UnrecordedFrontend{Blocked: fmt.Sprintf("it deployed into the Vercel project %s without recording which of its environments", rec.ProjectRef)}, nil
	}
	if _, err := v.Client.GetProject(ctx, tok, rec.ProjectRef); vercel.IsNotFound(err) {
		return UnrecordedFrontend{Evidence: rec.ProjectRef}, nil
	} else if err != nil {
		return UnrecordedFrontend{}, err
	}
	et, err := v.Environments.Target(ctx, job)
	if err != nil {
		return UnrecordedFrontend{}, err
	}
	if et.Builtin != "" {
		return UnrecordedFrontend{External: planning.VercelRef(rec.ProjectRef, targetRef(vercel.Target{Builtin: et.Builtin}))}, nil
	}
	env, err := v.Client.GetCustomEnvironment(ctx, tok, rec.ProjectRef, strings.ToLower(et.Name))
	if vercel.IsNotFound(err) {
		// The deploy never made its environment there.
		return UnrecordedFrontend{Evidence: rec.ProjectRef}, nil
	}
	if err != nil {
		return UnrecordedFrontend{}, err
	}
	return UnrecordedFrontend{External: planning.VercelRef(rec.ProjectRef, targetRef(vercel.Target{CustomID: env.ID}))}, nil
}

// lookupProject is the Vercel project called name, or nil.
func (v *Vercel) lookupProject(ctx context.Context, tok vercel.Token, name string) (*vercel.Project, error) {
	p, err := v.Client.GetProject(ctx, tok, name)
	if vercel.IsNotFound(err) {
		return nil, nil
	}
	return p, err
}
