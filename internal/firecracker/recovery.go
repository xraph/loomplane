package firecracker

import (
	"context"
	"errors"
	"time"
)

// Reconcile retries recorded cleanup and enforces a build's absolute lifetime.
// Restarting an application requires fresh control-plane authorization.
func (h *Host) Reconcile(ctx context.Context) error {
	h.Store.mu.Lock()
	defer h.Store.mu.Unlock()
	all, err := h.Store.List(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range all {
		expired := false
		for _, v := range []*VM{r.Current, r.Pending} {
			if v != nil && v.Spec.Kind == "build" && (v.Created.IsZero() || time.Since(v.Created) > 40*time.Minute) {
				expired = true
			}
		}
		if expired {
			if r.Current != nil {
				r.Garbage = append(r.Garbage, *r.Current)
				r.Current = nil
			}
			if r.Pending != nil {
				r.Garbage = append(r.Garbage, *r.Pending)
				r.Pending = nil
			}
			r.State = "remove-pending"
			if e := h.Store.Save(ctx, r); e != nil {
				failures = append(failures, e)
				continue
			}
		}
		var repairErr error
		switch r.State {
		case "stop-pending", "remove-pending":
			repairErr = h.finishMutation(ctx, &r)
		case "ready":
			if r.Current != nil {
				repairErr = h.finishReady(ctx, &r)
			}
		default:
			repairErr = h.cleanup(ctx, &r)
		}
		if repairErr != nil {
			failures = append(failures, repairErr)
		}
	}
	return errors.Join(failures...)
}
