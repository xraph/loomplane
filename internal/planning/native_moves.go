package planning

import (
	"context"
	"fmt"
)

// NativeFallbacks reads the records of where an environment deploys on a
// provider: FellBack says whether a service fell back from the provider's
// own environments to a project of its own, and Recorded whether the key
// has any record, native or fallback. Scope is the service's name on
// Vercel, and "" on Render.
type NativeFallbacks interface {
	FellBack(ctx context.Context, orgID, environmentID, provider, connectionID, scope string) (bool, error)
	Recorded(ctx context.Context, orgID, environmentID, provider, connectionID, scope string) (bool, error)
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

// renderMoves are the action IDs of the environment's Render services and
// datastores that run standalone with no record of the environment on
// their connection: a native deploy always records it, and so does a
// fallback, so there's none only once "try again" cleared a fallback (or
// before native environments). Each is planned as an update, and the
// deploy moves it into the environment's Render environment in place,
// keeping its data and address. Nil fallbacks plans none.
func renderMoves(ctx context.Context, fallbacks NativeFallbacks, orgID, environmentID string, bindings []*Binding) (map[string]bool, error) {
	moves := map[string]bool{}
	if fallbacks == nil {
		return moves, nil
	}
	recorded := map[string]bool{}
	for _, b := range bindings {
		if b.Provider != "render" || b.ExternalID == "" || b.Drift == DriftMissing || ExistingResourceBinding(b) ||
			(b.Subject != ServiceSubject && b.Subject != ResourceSubject) {
			continue
		}
		known, asked := recorded[b.ConnectionID]
		if !asked {
			var err error
			if known, err = fallbacks.Recorded(ctx, orgID, environmentID, "render", b.ConnectionID, ""); err != nil {
				return nil, fmt.Errorf("read the environment's render record: %w", err)
			}
			recorded[b.ConnectionID] = known
		}
		if !known {
			moves[actionID(b.Subject, b.Name)] = true
		}
	}
	return moves, nil
}

// renderMove says whether an unchanged Render action is updated to move
// it into the environment's Render environment, as renderMoves found.
func (p *planner) renderMove(a *Action, providerName string) bool {
	return providerName == "render" && p.in.RenderMoves[a.ID]
}
