package releaseprep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/xraph/loomplane/internal/agentops"
	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/projects"
	"github.com/xraph/loomplane/internal/releasewatch"
	"github.com/xraph/loomplane/internal/security"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// PrepareReleaseInput is prepare_release's. It has no replace: the model
// can't supersede a person's plan.
type PrepareReleaseInput struct {
	Project string   `json:"project" jsonschema:"project slug, name or ID"`
	From    string   `json:"from,omitempty" jsonschema:"the staging environment's name; defaults to the project's only staging environment"`
	To      string   `json:"to,omitempty" jsonschema:"the production environment's name; defaults to the project's only production environment"`
	Scope   []string `json:"scope,omitempty" jsonschema:"source aliases to release; empty releases every source"`
}

// PrepareReleaseOutput is the preparation prepare_release started, or why
// it couldn't, in words to tell the person.
type PrepareReleaseOutput struct {
	ReportID string `json:"report_id,omitempty"`
	// Status is the report's, or "refused" with Message saying why.
	Status  string `json:"status"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
}

// GetReleaseReportInput is get_release_report's.
type GetReleaseReportInput struct {
	ReportID string `json:"report_id"`
}

// NoteReleaseConcernInput is note_release_concern's.
type NoteReleaseConcernInput struct {
	ReportID string `json:"report_id"`
	Kind     string `json:"kind" jsonschema:"ask, so Loomplane asks a person, or risk, a note for reviewers"`
	Text     string `json:"text"`
}

// NoteReleaseConcernOutput is what note_release_concern stored.
type NoteReleaseConcernOutput struct {
	ReportID string `json:"report_id"`
	Kind     string `json:"kind"`
	Concerns int    `json:"concerns"`
	Note     string `json:"note"`
}

// The release preparation operations. None removes a concern, edits the
// evidence or clears a gate: the model can add caution, nothing else.
var (
	PrepareReleaseDef = defs.Define[PrepareReleaseInput]("prepare_release",
		"Prepare a production release for the person you're helping: Loomplane plans the promotion from staging, collects the evidence, and the agent reviews it. Only a person can start one. It never replaces a plan someone is deciding; if one is open, tell the person.",
		defs.RiskChange, defs.On(defs.SurfaceAgent))
	GetReleaseReportDef = defs.Define[GetReleaseReportInput]("get_release_report",
		"Read a release readiness report: the plan, the evidence Loomplane collected, the seven gates and the concerns noted so far.",
		defs.RiskRead, defs.ReadOnly(), defs.On(defs.SurfaceAgent))
	NoteReleaseConcernDef = defs.Define[NoteReleaseConcernInput]("note_release_concern",
		`Note a concern on the release you are reviewing, in one or two sentences. Kind "ask" makes Loomplane ask a person; kind "risk" is a note for reviewers. A concern never clears a gate.`,
		defs.RiskChange, defs.On(defs.SurfaceAgent))
)

// errUnavailable is a release operation called on a server where release
// preparation isn't installed.
var errUnavailable = errors.New("release preparation isn't available on this server")

// errNeedsPerson is prepare_release called by the agent alone. It isn't
// wrapped as a refusal, whose text the model reads only as the missing
// permission: this says what to do.
var errNeedsPerson = errors.New("release preparation is started by a person; ask one to start it")

// installed is the Service the operations call. The agent's extension
// builds its operations before release preparation's Service exists, so
// they read it at call time.
var installed atomic.Pointer[Service]

// Install makes s the Service the release operations call. The extension
// calls it in Register.
func Install(s *Service) { installed.Store(s) }

// Installed is the Service Install set, or nil.
func Installed() *Service { return installed.Load() }

// RegisterOps adds the release preparation operations to r over d. svc is
// read at each call; nil, or a nil Service, answers that release
// preparation isn't available.
//
// Each checks its permission on the project of the stored report (or the
// project prepare_release names), as the agent with the person it acts
// for, and reads only the calling agent's organization.
func RegisterOps(r *agentops.Registry, d *agentops.Deps, svc func() *Service) {
	service := func() (*Service, error) {
		if svc == nil {
			return nil, errUnavailable
		}
		if s := svc(); s != nil {
			return s, nil
		}
		return nil, errUnavailable
	}
	agentops.Register(r, PrepareReleaseDef, security.DeploymentCreate, func(ctx context.Context, call agentops.Call, in PrepareReleaseInput) (PrepareReleaseOutput, error) {
		s, err := service()
		if err != nil {
			return PrepareReleaseOutput{}, err
		}
		return prepareRelease(ctx, s, d, call, in)
	})
	agentops.Register(r, GetReleaseReportDef, security.DeploymentRead, func(ctx context.Context, call agentops.Call, in GetReleaseReportInput) (*ReleaseReport, error) {
		s, err := service()
		if err != nil {
			return nil, err
		}
		id, err := reportID(in.ReportID)
		if err != nil {
			return nil, err
		}
		if serr := s.inScope(ctx, call, id); serr != nil {
			return nil, serr
		}
		rep, err := s.Get(ctx, call.Principal, id)
		if err != nil {
			return nil, err
		}
		return quoteReport(ctx, d.Safety, rep)
	})
	agentops.Register(r, NoteReleaseConcernDef, security.DeploymentRead, func(ctx context.Context, call agentops.Call, in NoteReleaseConcernInput) (NoteReleaseConcernOutput, error) {
		s, err := service()
		if err != nil {
			return NoteReleaseConcernOutput{}, err
		}
		return noteConcern(ctx, s, call, in)
	})
}

func reportID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("%w: name the release readiness report: its report_id", ErrInvalid)
	}
	return id, nil
}

// prepareRelease starts a preparation for the person the agent acts for,
// through chat. An open plan or a preparation under way comes back as the
// result, for the model to tell the person.
func prepareRelease(ctx context.Context, s *Service, d *agentops.Deps, call agentops.Call, in PrepareReleaseInput) (PrepareReleaseOutput, error) {
	person, ok := call.Principal.Person()
	if !ok {
		return PrepareReleaseOutput{}, errNeedsPerson
	}
	if d.Projects == nil || d.Authz == nil {
		return PrepareReleaseOutput{}, errUnavailable
	}
	project, err := d.FindProject(ctx, call, in.Project)
	if err != nil {
		return PrepareReleaseOutput{}, err
	}
	// A run about one environment may prepare only a release to it.
	if call.Scope.EnvironmentID != "" {
		envs, eerr := d.Projects.Environments(ctx, call.Principal, project.ID)
		if eerr != nil {
			return PrepareReleaseOutput{}, eerr
		}
		to, perr := pick(envs, in.To, projects.KindProduction, ErrNoProduction)
		if perr != nil {
			return PrepareReleaseOutput{}, perr
		}
		if serr := call.InScope(ctx, project.ID, to.ID); serr != nil {
			return PrepareReleaseOutput{}, serr
		}
	}
	// Prepare checks the person; the agent must be allowed to deploy here
	// too, as it will be when the job plans.
	if aerr := d.Authz.Require(ctx, call.Principal, security.DeploymentCreate, project.ID); aerr != nil {
		return PrepareReleaseOutput{}, aerr
	}
	r, err := s.Prepare(ctx, person, project.ID, PrepareInput{From: in.From, To: in.To, Scope: in.Scope}, ViaChat)
	switch {
	case errors.Is(err, ErrOpenPlan), errors.Is(err, ErrInProgress):
		return PrepareReleaseOutput{Status: "refused", Message: err.Error()}, nil
	case err != nil:
		return PrepareReleaseOutput{}, err
	}
	return PrepareReleaseOutput{ReportID: r.ID, Status: r.Status, Path: "/release-reports/" + r.ID}, nil
}

// noteConcern adds a concern to the report the calling run reviews. The
// task is the run's own, which Loomplane knows; Note refuses any other.
func noteConcern(ctx context.Context, s *Service, call agentops.Call, in NoteReleaseConcernInput) (NoteReleaseConcernOutput, error) {
	id, err := reportID(in.ReportID)
	if err != nil {
		return NoteReleaseConcernOutput{}, err
	}
	if serr := s.inScope(ctx, call, id); serr != nil {
		return NoteReleaseConcernOutput{}, serr
	}
	if _, rerr := s.readable(ctx, call.Principal, id); rerr != nil {
		return NoteReleaseConcernOutput{}, rerr
	}
	rep, err := s.Note(ctx, call.Principal, call.TaskID, id, Concern{Kind: strings.TrimSpace(in.Kind), Text: in.Text})
	if err != nil {
		return NoteReleaseConcernOutput{}, err
	}
	note := "Noted for reviewers."
	if in.Kind == ConcernAsk {
		note = "Noted: Loomplane will ask a person before this release goes out."
	}
	return NoteReleaseConcernOutput{ReportID: rep.ID, Kind: strings.TrimSpace(in.Kind), Concerns: len(rep.Concerns), Note: note}, nil
}

// inScope checks report id is the calling agent's organization's, and in
// the run's project and environment. It reads no permission: a report
// outside the run is not found, whatever the person may read.
func (s *Service) inScope(ctx context.Context, call agentops.Call, id string) error {
	r, err := s.d.Store.Get(ctx, call.Principal.OrgID, id)
	if err != nil {
		return err
	}
	return call.InScope(ctx, r.ProjectID, r.ToEnvironmentID)
}

// readable is report id of p's organization, once p may read the
// deployments of its project.
func (s *Service) readable(ctx context.Context, p security.Principal, id string) (*ReleaseReport, error) {
	r, err := s.d.Store.Get(ctx, p.OrgID, id)
	if err != nil {
		return nil, err
	}
	if rerr := s.require(ctx, p, security.DeploymentRead, r.ProjectID); rerr != nil {
		return nil, rerr
	}
	return r, nil
}

// quoteReport is r as the model reads it: a copy whose text from outside
// Loomplane, and the concerns a model wrote, are quoted through
// safety.Untrusted with their source. The stored report is left alone.
func quoteReport(ctx context.Context, safety *agentsafety.Scanner, r *ReleaseReport) (*ReleaseReport, error) {
	out := *r
	out.Scope = slices.Clone(r.Scope)
	out.Gates = slices.Clone(r.Gates)
	out.Concerns = slices.Clone(r.Concerns)
	for i := range out.Concerns {
		out.Concerns[i].Text = safety.Untrusted(ctx, agentsafety.SourceToolResult, out.Concerns[i].Text)
	}
	if r.Evidence == nil {
		return &out, nil
	}
	raw, err := json.Marshal(r.Evidence)
	if err != nil {
		return nil, fmt.Errorf("copy the evidence: %w", err)
	}
	var ev ReadinessEvidence
	if err = json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("copy the evidence: %w", err)
	}
	quoteEvidence(ctx, safety, &ev)
	out.Evidence = &ev
	return &out, nil
}

// quoteEvidence quotes, in place, every field of ev that can hold text from
// outside Loomplane.
func quoteEvidence(ctx context.Context, safety *agentsafety.Scanner, ev *ReadinessEvidence) {
	q := func(source string, text *string) { *text = safety.Untrusted(ctx, source, *text) }
	for i := range ev.Sources {
		d := &ev.Sources[i]
		q(agentsafety.SourceErrorOutput, &d.Error)
		for j := range d.Commits {
			q(agentsafety.SourceCommitMessage, &d.Commits[j].Title)
			q(agentsafety.SourceCommitMessage, &d.Commits[j].Author)
		}
		for j := range d.Migrations {
			q(agentsafety.SourceManifest, &d.Migrations[j].Path)
			q(agentsafety.SourceManifest, &d.Migrations[j].Patch)
		}
	}
	for _, list := range [][]planning.Finding{ev.Problems, ev.Warnings} {
		for i := range list {
			q(agentsafety.SourceErrorOutput, &list[i].Message)
			for j := range list[i].Evidence {
				q(agentsafety.SourceReadme, &list[i].Evidence[j].Detail)
			}
		}
	}
	if s := ev.Soak; s != nil {
		for i := range s.Reasons {
			q(agentsafety.SourceErrorOutput, &s.Reasons[i])
		}
		for i := range s.Services {
			q(agentsafety.SourceErrorOutput, &s.Services[i].Reason)
		}
	}
	for _, r := range []*releasewatch.ReleaseEvidence{ev.StagingRelease, ev.ProductionRelease} {
		if r != nil {
			q(agentsafety.SourceErrorOutput, &r.Reason)
		}
	}
	if c := ev.Coverage; c != nil {
		for i := range c.Services {
			q(agentsafety.SourceErrorOutput, &c.Services[i].Reason)
		}
	}
	for i := range ev.Canaries {
		q(agentsafety.SourceErrorOutput, &ev.Canaries[i].Reason)
	}
	for i := range ev.OpenReports {
		q(agentsafety.SourceErrorOutput, &ev.OpenReports[i].Title)
	}
	for key, why := range ev.Unavailable {
		ev.Unavailable[key] = safety.Untrusted(ctx, agentsafety.SourceErrorOutput, why)
	}
}
