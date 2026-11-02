package planning

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/discovery"
)

const (
	// analysisWaitPerAttempt is how long one planning attempt waits for
	// the analyses a promotion pins. It leaves compute half of the plan
	// activity's minute.
	analysisWaitPerAttempt = 30 * time.Second
	analysisPoll           = 2 * time.Second
)

// ErrAnalysisPending is a plan that pins an analysis still running. It
// isn't permanent: the plan activity retries it.
var ErrAnalysisPending = errors.New("an analysis this plan pins is still running")

// awaitAnalyses waits for the analyses a promotion queued for commits its
// source environment runs but nothing had analyzed: the in-scope sources
// with no From plan. It returns why the plan can never be planned when one
// failed, is gone, or resolved another commit, and ErrAnalysisPending when
// one is still running after analysisWaitPerAttempt. The plan activity
// retries that with its backoff, eight attempts in all, so a promotion
// waits about six minutes for a fresh analysis before it fails.
func (s *Service) awaitAnalyses(ctx context.Context, plan *Plan) (failure string, err error) {
	if s.commits == nil || plan.Kind != KindPromotion {
		return "", nil
	}
	var waiting []PlanSource
	for _, ps := range plan.Sources {
		if ps.InScope && ps.FromPlanID == "" {
			waiting = append(waiting, ps)
		}
	}
	deadline := s.now().Add(analysisWaitPerAttempt)
	for {
		var pending []string
		for _, ps := range waiting {
			a, aerr := s.commits.Analysis(ctx, plan.OrgID, ps.AnalysisID)
			switch {
			case errors.Is(aerr, discovery.ErrNotFound):
				return fmt.Sprintf("The analysis of %s at %s is gone; plan again", ps.Alias, short(ps.CommitSHA)), nil
			case aerr != nil:
				return "", fmt.Errorf("read the analysis of %s: %w", ps.Alias, aerr)
			case a.Status == discovery.StatusFailed && a.Error != "":
				return fmt.Sprintf("The analysis of %s at %s: %s", ps.Alias, short(ps.CommitSHA), a.Error), nil
			case a.Status == discovery.StatusFailed:
				return fmt.Sprintf("The analysis of %s at %s failed", ps.Alias, short(ps.CommitSHA)), nil
			case a.Status == discovery.StatusSucceeded && a.CommitSHA != ps.CommitSHA:
				return fmt.Sprintf("The analysis of %s at %s resolved %s instead of %s",
					ps.Alias, short(ps.CommitSHA), short(a.CommitSHA), short(ps.CommitSHA)), nil
			case a.Status != discovery.StatusSucceeded:
				pending = append(pending, fmt.Sprintf("%s at %s (%s)", ps.Alias, short(ps.CommitSHA), a.Status))
			}
		}
		if len(pending) == 0 {
			return "", nil
		}
		if !s.now().Before(deadline) {
			return "", fmt.Errorf("%w: %s", ErrAnalysisPending, strings.Join(pending, ", "))
		}
		if serr := s.sleep(ctx, analysisPoll); serr != nil {
			return "", serr
		}
	}
}

// waitFor waits for d, or until ctx ends.
func waitFor(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
