package releaseprep

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/agentsafety"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/releasehealth"
	"github.com/xraph/loomplane/internal/releasewatch"
)

// MaxInputBytes is the most a review starts from, in bytes: agentruns
// refuses a task's input over 64 KiB.
const MaxInputBytes = 64 << 10

// commitsWhenCut is how many commit titles of each source stay when the
// input must shrink.
const commitsWhenCut = 20

// What the input leaves out to fit, said at its end.
const (
	notePatches = "- migration patches omitted to fit; get_release_report has them"
	noteCommits = "- commit titles beyond 20 per source omitted to fit; get_release_report has them"
	noteRest    = "- the rest of the report didn't fit; read it with get_release_report"
)

// partKind is how a part of the input fares when it must shrink.
type partKind int

const (
	partText       partKind = iota // always kept
	partPatch                      // a migration file with its patch
	partPatchCut                   // the same file without it
	partCommits                    // every commit title of a source
	partCommitsCut                 // its first commitsWhenCut
)

type part struct {
	text string
	kind partKind
}

// RenderInput is what the review starts from: the report as Loomplane
// computed it, at most MaxInputBytes. Text from outside Loomplane (commit
// titles and authors, migration paths and patches, report titles, error
// and verdict text) reaches the model only through safety.Untrusted,
// quoted with its source; ctx must be bound to the report's workspace.
// When it doesn't fit, migration patches go first, then commit titles
// beyond 20 a source, then whole parts from the end, and the input says
// what it left out.
func RenderInput(ctx context.Context, safety *agentsafety.Scanner, r *ReleaseReport) string {
	parts := (&renderer{ctx: ctx, safety: safety}).parts(r)
	if out := assemble(parts, true, true, nil); len(out) <= MaxInputBytes {
		return out
	}
	if out := assemble(parts, false, true, []string{notePatches}); len(out) <= MaxInputBytes {
		return out
	}
	if out := assemble(parts, false, false, []string{notePatches, noteCommits}); len(out) <= MaxInputBytes {
		return out
	}
	return fit(parts, []string{notePatches, noteCommits, noteRest})
}

// keep reports whether part p is in an input with or without the patches
// and every commit title.
func keep(p part, patches, commits bool) bool {
	switch p.kind {
	case partPatch:
		return patches
	case partPatchCut:
		return !patches
	case partCommits:
		return commits
	case partCommitsCut:
		return !commits
	}
	return true
}

func leftOut(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "\n## Left out to fit\n" + strings.Join(notes, "\n") + "\n"
}

func assemble(parts []part, patches, commits bool, notes []string) string {
	var b strings.Builder
	for _, p := range parts {
		if keep(p, patches, commits) {
			b.WriteString(p.text)
		}
	}
	b.WriteString(leftOut(notes))
	return b.String()
}

// fit keeps whole parts, in order, while they fit with the notes, so no
// quoted block is ever cut open.
func fit(parts []part, notes []string) string {
	tail := leftOut(notes)
	var b strings.Builder
	for _, p := range parts {
		if !keep(p, false, false) {
			continue
		}
		if b.Len()+len(p.text)+len(tail) > MaxInputBytes {
			break
		}
		b.WriteString(p.text)
	}
	b.WriteString(tail)
	return b.String()
}

type renderer struct {
	ctx    context.Context
	safety *agentsafety.Scanner
	out    []part
}

// quote is untrusted text as the model may read it.
func (w *renderer) quote(source, text string) string {
	return w.safety.Untrusted(w.ctx, source, text)
}

func (w *renderer) text(format string, args ...any) {
	w.out = append(w.out, part{text: fmt.Sprintf(format, args...)})
}

func (w *renderer) add(kind partKind, text string) {
	w.out = append(w.out, part{text: text, kind: kind})
}

func (w *renderer) parts(r *ReleaseReport) []part {
	w.text("Loomplane prepared this release of project %s and computed its readiness report. Review it.\n", r.ProjectID)
	w.plan(r)
	w.gates(r)
	ev := r.Evidence
	if ev == nil {
		w.text("\n## Evidence\nLoomplane holds no evidence for this release, so every gate that needs it fails.\n")
		return w.out
	}
	// What decides the release first, the long diffs last: a cut to fit
	// drops from the end.
	w.unavailable(ev)
	w.budget(ev)
	w.services(ev)
	w.findings(ev)
	w.soak(ev, r.FromEnvironment)
	w.canaries(ev)
	w.coverage(ev, r.ToEnvironment)
	w.reports(ev, r.FromEnvironment)
	w.artifacts(ev)
	w.sources(ev)
	return w.out
}

func (w *renderer) plan(r *ReleaseReport) {
	scope := "every source"
	if len(r.Scope) > 0 {
		scope = "sources " + strings.Join(r.Scope, ", ")
	}
	made := "Loomplane made a new plan for it."
	if r.ReusedPlan {
		made = "Loomplane reused production's open promotion plan for it."
	}
	w.text("\n## Plan\nreport_id: %s\nplan_id: %s\nrevision: %s\nIt promotes %s to %s, %s. %s Asked for by %s.\n",
		r.ID, r.PlanID, r.PlanRevision, r.FromEnvironment, r.ToEnvironment, scope, made, r.RequestedBy)
}

func (w *renderer) gates(r *ReleaseReport) {
	var b strings.Builder
	b.WriteString("\n## Gate preview\nLoomplane computed these before your review. approve_plan checks every gate again from fresh evidence.\n")
	if len(r.Gates) == 0 {
		b.WriteString("No gate was computed.\n")
	}
	for _, g := range r.Gates {
		result := "failed"
		if g.Passed {
			result = "passed"
		}
		fmt.Fprintf(&b, "- gate %d (%s): %s: %s\n", g.Number, g.Name, result, g.Detail)
	}
	w.text("%s", b.String())
}

func (w *renderer) services(ev *ReadinessEvidence) {
	var b strings.Builder
	b.WriteString("\n## Changed services\n")
	if len(ev.ChangedServices) == 0 {
		b.WriteString("The plan changes no service.\n")
	}
	for _, s := range ev.ChangedServices {
		fmt.Fprintf(&b, "- %s (%s, %s): %s\n", s.Name, orDefault(s.Provider, "no provider"), s.Op, guard(s, ev.Coverage))
		if s.LastCanary != "" {
			status := "in a state Loomplane couldn't read"
			if i := slices.IndexFunc(ev.Canaries, func(c CanarySummary) bool { return c.DeploymentID == s.LastCanary }); i >= 0 {
				status = ev.Canaries[i].Status
			}
			fmt.Fprintf(&b, "  its last production canary, deployment %s, is %s\n", s.LastCanary, status)
		}
	}
	if len(ev.Destroy) > 0 {
		fmt.Fprintf(&b, "Destroys or replaces data: %s\n", strings.Join(ev.Destroy, ", "))
	}
	if len(ev.DataUpdates) > 0 {
		fmt.Fprintf(&b, "Updates data-bearing resources: %s\n", strings.Join(ev.DataUpdates, ", "))
	}
	if ev.BackupSchedule {
		b.WriteString("Sets a default backup schedule.\n")
	}
	w.text("%s", b.String())
}

// guard says what protects a changed service once it's live.
func guard(s ChangedService, cov *releasewatch.Coverage) string {
	switch {
	case s.Canary && releasehealth.SupportFor(s.Provider).Canary:
		return "a canary ramps it"
	case cov != nil && guarded(s, cov.Duration, cov.Services):
		return fmt.Sprintf("no canary: a %s post-deploy watch guards %s", formatDuration(cov.Duration), s.Name)
	}
	return "no canary and no post-deploy watch can judge " + s.Name
}

func (w *renderer) sources(ev *ReadinessEvidence) {
	w.text("\n## Sources\n")
	if len(ev.Sources) == 0 {
		w.text("The plan moves no source.\n")
	}
	for _, d := range ev.Sources {
		w.text("\n### %s (%s)\n%s\n", d.Alias, d.Repository, direction(d))
		if d.Error != "" {
			w.text("The diff isn't complete or couldn't be read, so it counts as missing evidence:\n%s\n",
				w.quote(agentsafety.SourceErrorOutput, d.Error))
		}
		if len(d.Commits) > 0 {
			w.commits(d)
		}
		if len(d.Migrations) > 0 {
			w.migrations(d)
		}
	}
}

func direction(d SourceDiff) string {
	commits := fmt.Sprintf("%d %s, %d %s changed (+%d -%d).", d.TotalCommits, plural(d.TotalCommits, "commit", "commits"),
		d.FilesChanged, plural(d.FilesChanged, "file", "files"), d.Additions, d.Deletions)
	switch d.Direction {
	case DirectionNew:
		return fmt.Sprintf("Production never ran this source; staging runs %s.", shortSHA(d.StagingCommit))
	case DirectionIdentical:
		return fmt.Sprintf("Staging runs production's commit %s: nothing changes.", shortSHA(d.StagingCommit))
	case DirectionAhead, DirectionBehind, DirectionDiverged:
		return fmt.Sprintf("Production runs %s, staging runs %s: staging is %s (ahead by %d, behind by %d). %s",
			shortSHA(d.ProductionCommit), shortSHA(d.StagingCommit), d.Direction, d.AheadBy, d.BehindBy, commits)
	}
	return fmt.Sprintf("Production runs %s, staging runs %s; Loomplane couldn't compare them.", shortSHA(d.ProductionCommit), shortSHA(d.StagingCommit))
}

// commits quotes a source's commit titles as one block, and when there
// are more than commitsWhenCut, a shorter block for when the input must
// shrink.
func (w *renderer) commits(d SourceDiff) {
	block := func(commits []DiffCommit) string {
		lines := make([]string, 0, len(commits))
		for _, c := range commits {
			line := shortSHA(c.SHA) + " " + c.Title
			if c.Author != "" {
				line += " (" + c.Author + ")"
			}
			lines = append(lines, line)
		}
		return w.quote(agentsafety.SourceCommitMessage, strings.Join(lines, "\n"))
	}
	head := fmt.Sprintf("Commit titles, newest first (%d):\n", len(d.Commits))
	if len(d.Commits) <= commitsWhenCut {
		w.add(partText, head+block(d.Commits)+"\n")
		return
	}
	w.add(partCommits, head+block(d.Commits)+"\n")
	w.add(partCommitsCut, fmt.Sprintf("The newest %d of %d commit titles:\n", commitsWhenCut, len(d.Commits))+block(d.Commits[:commitsWhenCut])+"\n")
}

// migrations quotes each changed migration as its own block, its path
// and patch together: the path is the repository's too.
func (w *renderer) migrations(d SourceDiff) {
	w.text("Changed migration and schema files (%d):\n", len(d.Migrations))
	for i, m := range d.Migrations {
		stats := fmt.Sprintf("%s (%s, +%d -%d)", m.Path, m.Status, m.Additions, m.Deletions)
		label := fmt.Sprintf("Migration %d:\n", i+1)
		patch := m.Patch
		if m.Truncated {
			patch += "\n[the patch was cut short]"
		}
		w.add(partPatch, label+w.quote(agentsafety.SourceManifest, stats+"\n\n"+patch)+"\n")
		w.add(partPatchCut, label+w.quote(agentsafety.SourceManifest, stats)+"\n")
	}
}

func (w *renderer) artifacts(ev *ReadinessEvidence) {
	var b strings.Builder
	b.WriteString("\n## Artifacts\n")
	if len(ev.Artifacts) == 0 {
		b.WriteString("None listed.\n")
	}
	for _, a := range ev.Artifacts {
		if a.Reused {
			fmt.Fprintf(&b, "- %s: reuses %s from %s\n", a.Service, orDefault(a.Reference, "its image"), orDefault(a.From, "staging"))
			continue
		}
		fmt.Fprintf(&b, "- %s: builds again: %s\n", a.Service, orDefault(a.Reason, "no reason given"))
	}
	w.text("%s", b.String())
}

func (w *renderer) findings(ev *ReadinessEvidence) {
	w.text("\n## Problems and warnings\n")
	if len(ev.Problems)+len(ev.Warnings) == 0 {
		w.text("None.\n")
	}
	for _, f := range []struct {
		kind string
		list []planning.Finding
	}{{"problem", ev.Problems}, {"warning", ev.Warnings}} {
		for _, x := range f.list {
			name := ""
			if x.Name != "" {
				name = " (" + x.Name + ")"
			}
			w.text("- %s %s%s:\n%s\n", f.kind, x.Code, name, w.quote(agentsafety.SourceErrorOutput, x.Message))
		}
	}
}

func (w *renderer) soak(ev *ReadinessEvidence, from string) {
	from = orDefault(from, "staging")
	w.text("\n## Staging soak\n")
	switch s := ev.Soak; {
	case s == nil:
		w.text("Not read.\n")
	case !s.Running:
		w.text("%s isn't running the plan's commits.\n", from)
	default:
		health := "healthy"
		switch {
		case s.Unverified:
			health = "unverified: the evidence is missing"
		case !s.Healthy:
			health = "not healthy"
		}
		w.text("%s has run the plan's commits for %s, %s.\n", from, formatDuration(s.Duration), health)
	}
	if s := ev.Soak; s != nil {
		var why []string
		for _, svc := range s.Services {
			w.text("- %s (%s): %s\n", svc.Name, svc.Provider, svc.Verdict)
			if svc.Reason != "" {
				why = append(why, svc.Name+": "+svc.Reason)
			}
		}
		why = append(why, s.Reasons...)
		if len(why) > 0 {
			w.text("What the soak reported:\n%s\n", w.quote(agentsafety.SourceErrorOutput, strings.Join(why, "\n")))
		}
	}
	w.release(ev.StagingRelease, from)
}

// release says what the watch says of an environment's last release.
func (w *renderer) release(r *releasewatch.ReleaseEvidence, env string) {
	switch {
	case r == nil:
		w.text("%s's last release: not read.\n", env)
	case r.Unverified:
		w.text("%s's last release %s is unverified: %s.\n", env, orDefault(r.DeploymentID, "(none)"), unverifiedRelease(r, env))
	case !r.Watched:
		w.text("%s's last release %s wasn't watched.\n", env, orDefault(r.DeploymentID, "(none)"))
	default:
		w.text("%s's last release %s: watched, status %s, verdict %s.\n", env, r.DeploymentID, orDefault(r.Status, "unknown"), orDefault(r.Verdict, "none"))
	}
}

func (w *renderer) canaries(ev *ReadinessEvidence) {
	w.text("\n## Production canaries\n")
	switch {
	case ev.Canaries == nil:
		w.text("Not read.\n")
	case len(ev.Canaries) == 0:
		w.text("No canary ever ramped a changed service.\n")
	}
	for _, c := range ev.Canaries {
		w.text("- deployment %s (plan %s): %s, ramped %s\n", c.DeploymentID, c.PlanID, c.Status, strings.Join(c.Services, ", "))
		if c.Reason != "" {
			w.text("%s\n", w.quote(agentsafety.SourceErrorOutput, c.Reason))
		}
	}
}

func (w *renderer) coverage(ev *ReadinessEvidence, to string) {
	to = orDefault(to, "production")
	var b strings.Builder
	b.WriteString("\n## Production watch coverage\n")
	if c := ev.Coverage; c == nil {
		b.WriteString("Not read.\n")
	} else {
		if c.Duration > 0 {
			fmt.Fprintf(&b, "A release to %s is watched for %s.", to, formatDuration(c.Duration))
		} else {
			fmt.Fprintf(&b, "A release to %s arms no post-deploy watch.", to)
		}
		if len(c.Canary) > 0 {
			fmt.Fprintf(&b, " A canary ramps %s.", strings.Join(c.Canary, ", "))
		}
		b.WriteString("\n")
		for _, s := range c.Services {
			watched := "not watched"
			if s.Watchable {
				watched = "watched"
			}
			fmt.Fprintf(&b, "- %s (%s): %s\n", s.Name, s.Provider, watched)
		}
		if c.Unverified {
			b.WriteString("The coverage is unverified: a release here would leave missing evidence.\n")
		}
	}
	w.text("%s", b.String())
	w.release(ev.ProductionRelease, to)
}

func (w *renderer) reports(ev *ReadinessEvidence, from string) {
	w.text("\n## Open reports on staging\n")
	switch {
	case ev.OpenReports == nil:
		w.text("Not read.\n")
	case len(ev.OpenReports) == 0:
		w.text("None is open on %s.\n", orDefault(from, "staging"))
	}
	for _, o := range ev.OpenReports {
		w.text("- report %s (%s), open since %s; get_report reads it:\n%s\n", o.ID, orDefault(o.Severity, "no severity"),
			o.OpenedAt.UTC().Format("2006-01-02 15:04 UTC"), w.quote(agentsafety.SourceErrorOutput, o.Title))
	}
}

func (w *renderer) budget(ev *ReadinessEvidence) {
	switch b := ev.Budget; {
	case b == nil:
		w.text("\n## Budget\nNot read.\n")
	case b.Exhausted:
		w.text("\n## Budget\nThe agent's budget is used up: $%s spent of $%s.\n", b.SpentUSD, b.LimitUSD)
	default:
		w.text("\n## Budget\n$%s spent of $%s this month.\n", b.SpentUSD, b.LimitUSD)
	}
}

func (w *renderer) unavailable(ev *ReadinessEvidence) {
	w.text("\n## Unavailable evidence\n")
	if len(ev.Unavailable) == 0 {
		w.text("None: every piece of evidence was read.\n")
		return
	}
	for _, key := range slices.Sorted(maps.Keys(ev.Unavailable)) {
		w.text("- %s:\n%s\n", key, w.quote(agentsafety.SourceErrorOutput, ev.Unavailable[key]))
	}
}
