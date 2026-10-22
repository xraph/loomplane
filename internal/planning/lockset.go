package planning

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/xraph/loomplane/internal/discovery"
)

// commitPattern is a ref that names a commit rather than a branch.
var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)

// lockset pins each live repository of a plan: one in scope to its newest
// successful analysis, one held to what's deployed in the environment.
// scope names the repositories that move; empty means all of them. A name
// scope gives that isn't a live repository is refused, never ignored.
func lockset(sources []*discovery.Source, latest map[string]*discovery.Analysis, deployed map[string]*DeployedSource, scope []string, env string) ([]PlanSource, error) {
	if len(sources) == 0 {
		return nil, ErrNoSources
	}
	moves := map[string]bool{}
	for _, a := range scope {
		a = strings.TrimSpace(a)
		if !slices.ContainsFunc(sources, func(s *discovery.Source) bool { return s.Alias == a }) {
			return nil, fmt.Errorf("%w: %q isn't one of this project's repositories", ErrInvalid, a)
		}
		moves[a] = true
	}
	out := make([]PlanSource, 0, len(sources))
	for _, s := range sources {
		ps := PlanSource{SourceID: s.ID, Alias: s.Alias, Repository: s.Repository, Root: s.Root, Ref: s.Ref, InScope: len(moves) == 0 || moves[s.Alias]}
		if ps.InScope {
			a := latest[s.ID]
			if a == nil {
				return nil, fmt.Errorf("%w: %s has no successful analysis", ErrNoAnalysis, s.Alias)
			}
			ps.AnalysisID, ps.CommitSHA = a.ID, a.CommitSHA
			// The commit came from the branch the analysis read, which
			// "Another branch" can set apart from the source's own.
			if ref := strings.TrimSpace(a.Ref); ref != "" && !commitPattern.MatchString(ref) {
				ps.Ref = ref
			}
		} else {
			d := deployed[s.ID]
			if d == nil {
				return nil, fmt.Errorf("%w: %s has never been deployed to %s, so include it in the plan", ErrNeverDeployed, s.Alias, env)
			}
			ps.AnalysisID, ps.CommitSHA = d.AnalysisID, d.CommitSHA
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}
