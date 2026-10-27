package octopusloom

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"
)

// syncRegistrations keeps a Docker gateway's FARP registrations as the
// bindings want them. A service is registered when Octopus can't be holding
// it (it isn't recorded, or its registration failed), when its binding
// revision changed (a new release), or when Octopus restarted. Otherwise
// it is sent a heartbeat, and registered again in the same sync when
// Octopus answers 404. A recorded registration nothing wants any more, such
// as a service that opted out or was removed, is deregistered and dropped.
//
// A registration Octopus refuses is recorded on that service as
// Registered.Error and the others carry on. It does not fail the sync: the
// result reports routes and upstreams, and the service says for itself. An
// error is returned only when Octopus stops answering.
func (r *syncRun) syncRegistrations(ctx context.Context, now time.Time) error {
	if r.gateway.Provider != ProviderServer {
		return nil
	}
	regs := maps.Clone(r.gateway.State.Registrations)
	if regs == nil {
		regs = map[string]Registered{}
	}
	r.registrations = regs

	wanted := map[string]bool{}
	for _, want := range r.desired.Registrations {
		wanted[want.Service] = true
		if err := r.keepRegistered(ctx, want, now); err != nil {
			return err
		}
	}
	for _, service := range slices.Sorted(maps.Keys(regs)) {
		if wanted[service] {
			continue
		}
		stored := regs[service]
		if stored.InstanceID == "" {
			stored.InstanceID = FarpInstanceID(service)
		}
		err := r.admin.Deregister(ctx, stored.InstanceID)
		switch {
		case errors.Is(err, ErrUnavailable):
			return err
		case err != nil:
			// Keep it, so the next sync deregisters it again.
			stored.Error = fmt.Sprintf("Octopus refused to deregister %s: %s", service, refusal(err))
			regs[service] = stored
		default:
			delete(regs, service)
		}
	}
	return nil
}

// keepRegistered registers or heartbeats one wanted service.
func (r *syncRun) keepRegistered(ctx context.Context, want Registration, now time.Time) error {
	stored, known := r.registrations[want.Service]
	if known && !stored.RegisteredAt.IsZero() && stored.Revision == want.Revision && !r.restarted {
		err := r.admin.Heartbeat(ctx, want.InstanceID)
		switch {
		case err == nil:
			stored.HeartbeatAt, stored.Error = now, ""
			r.registrations[want.Service] = stored
			return nil
		case errors.Is(err, ErrUnknownInstance):
			// Octopus lost it. Register again below, in this sync.
		case errors.Is(err, ErrUnavailable):
			return err
		default:
			stored.Error = fmt.Sprintf("Octopus refused %s's FARP heartbeat: %s", want.Service, refusal(err))
			r.registrations[want.Service] = stored
			return nil
		}
	}
	err := r.admin.Register(ctx, want)
	switch {
	case errors.Is(err, ErrUnavailable):
		return err
	case err != nil:
		// Not registered as far as Loomplane knows, so the next sync tries
		// again whatever the revision.
		r.registrations[want.Service] = Registered{
			InstanceID: want.InstanceID, Revision: want.Revision,
			Error: fmt.Sprintf("Octopus refused %s's FARP registration: %s", want.Service, refusal(err)),
		}
	default:
		r.registrations[want.Service] = Registered{
			InstanceID: want.InstanceID, Revision: want.Revision, RegisteredAt: now, HeartbeatAt: now,
		}
	}
	return nil
}

// refusal is what an error says about why Octopus refused: its HTTP status,
// never its body, which could echo a credential. An answer Loomplane
// couldn't read has no status.
func refusal(err error) string {
	var status *StatusError
	if errors.As(err, &status) {
		return strconv.Itoa(status.Status)
	}
	return "unreadable answer"
}
