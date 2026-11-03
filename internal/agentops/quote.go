package agentops

import (
	"context"
	"slices"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/discovery"
	"github.com/xraph/loomplane/internal/planning"
)

// Text that comes from outside Loomplane (a cloud's error, a log line, a
// repository's content) reaches the model only through Safety.Untrusted,
// quoted with its source. These copy a service's answer with that text
// quoted, and leave the service's own value alone.

// quote is Safety.Untrusted, which withholds text when there is no scanner.
func (d *Deps) quote(ctx context.Context, source, text string) string {
	return d.Safety.Untrusted(ctx, source, text)
}

// quoteBinding quotes the cloud's word on a binding's drift.
func (d *Deps) quoteBinding(ctx context.Context, b *planning.Binding) planning.Binding {
	out := *b
	out.DriftMessage = d.quote(ctx, agentsafety.SourceErrorOutput, b.DriftMessage)
	return out
}

// quotePlan quotes why a plan failed, which can carry a cloud's or a
// provider's own words, and the lines of the repositories its problems and
// warnings quote as evidence.
func (d *Deps) quotePlan(ctx context.Context, p *planning.Plan) *planning.Plan {
	out := *p
	out.Error = d.quote(ctx, agentsafety.SourceErrorOutput, p.Error)
	out.Problems = d.quoteFindings(ctx, p.Problems)
	out.Warnings = d.quoteFindings(ctx, p.Warnings)
	// An automatic rollback's reason and decision note are built from
	// watch evidence.
	if p.Rollback != nil {
		rb := *p.Rollback
		rb.Reason = d.quote(ctx, evidence, rb.Reason)
		out.Rollback = &rb
	}
	out.DecisionNote = d.quote(ctx, evidence, p.DecisionNote)
	return &out
}

// quoteFindings quotes each finding's message, which can be a provider's
// error word for word, and the evidence it cites: a line of a
// repository's files.
func (d *Deps) quoteFindings(ctx context.Context, in []planning.Finding) []planning.Finding {
	if in == nil {
		return nil
	}
	out := slices.Clone(in)
	for i := range out {
		out[i].Message = d.quote(ctx, agentsafety.SourceErrorOutput, out[i].Message)
		if len(out[i].Evidence) == 0 {
			continue
		}
		out[i].Evidence = slices.Clone(out[i].Evidence)
		for j := range out[i].Evidence {
			ev := &out[i].Evidence[j]
			ev.Detail = d.quote(ctx, agentsafety.SourceReadme, ev.Detail)
		}
	}
	return out
}

// quoteDeployment quotes a deployment's error, its canary's reason (as
// canary_report does) and each step's message and warnings: they are what
// the cloud and the build said.
func (d *Deps) quoteDeployment(ctx context.Context, dep *deployments.Deployment) *deployments.Deployment {
	out := *dep
	out.Error = d.quote(ctx, agentsafety.SourceErrorOutput, dep.Error)
	out.Canary = d.quoteCanary(ctx, dep.Canary)
	out.Steps = slices.Clone(dep.Steps)
	for i := range out.Steps {
		s := &out.Steps[i]
		s.Message = d.quote(ctx, agentsafety.SourceErrorOutput, s.Message)
		if len(s.Warnings) > 0 {
			s.Warnings = slices.Clone(s.Warnings)
			for j := range s.Warnings {
				s.Warnings[j] = d.quote(ctx, agentsafety.SourceErrorOutput, s.Warnings[j])
			}
		}
	}
	return &out
}

// quoteAnalysis quotes why an analysis failed, which can quote the
// repository's own files. Its summary is counts only.
func (d *Deps) quoteAnalysis(ctx context.Context, a *discovery.Analysis) discovery.Analysis {
	out := *a
	out.Error = d.quote(ctx, agentsafety.SourceReadme, a.Error)
	return out
}

// planCreated tells the run driver about a plan the agent created.
func (d *Deps) planCreated(ctx context.Context, call Call, plan *planning.Plan) {
	if d.PlanCreated != nil && plan != nil {
		d.PlanCreated(ctx, call, plan)
	}
}
