package planning

import (
	"context"
	"fmt"
)

// NativeFallbacks says whether an environment's service fell back from the
// provider's own environments to a project of its own. Scope is the
// service's name on Vercel.
type NativeFallbacks interface {
	FellBack(ctx context.Context, orgID, environmentID, provider, connectionID, scope string) (bool, error)
}

// nativeMoves are the action IDs of the environment's Vercel services that
// run on a project of their own with no fallback record saying they must:
// they were deployed before native environments, or "try again" cleared
// their fallback. Each is planned as a move into the shared project. Nil
// fallbacks plans none.
func nativeMoves(ctx context.Context, fallbacks NativeFallbacks, orgID, environmentID string, bindings []*Binding) (map[string]bool, error) {
	moves := map[string]bool{}
	if fallbacks == nil {
		return moves, nil
	}
	for _, b := range bindings {
		if b.Subject != ServiceSubject || b.Provider != "vercel" || b.ExternalID == "" || b.Drift == DriftMissing {
			continue
		}
		if _, _, native := ParseVercelRef(b.ExternalID); native {
			continue
		}
		fell, err := fallbacks.FellBack(ctx, orgID, environmentID, "vercel", b.ConnectionID, b.Name)
		if err != nil {
			return nil, fmt.Errorf("read %s's fallback record: %w", b.Name, err)
		}
		if !fell {
			moves[actionID(b.Subject, b.Name)] = true
		}
	}
	return moves, nil
}

// nativeMove plans a Vercel service on a project of its own as a move into
// its shared project, as the environment's own Vercel environment, when
// nativeMoves found nothing keeping it where it is.
func (p *planner) nativeMove(a *Action, b *Binding, providerName string) {
	if a.Op == Replace || a.Subject != ServiceSubject || providerName != "vercel" || !p.in.NativeMoves[a.ID] {
		return
	}
	if _, _, native := ParseVercelRef(b.ExternalID); native {
		return
	}
	a.Op, a.NativeMove = Replace, true
	a.Reason = fmt.Sprintf("Moves into %s's shared Vercel project as its %s environment", a.Name, p.in.Environment)
}
