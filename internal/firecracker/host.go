package firecracker

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"
)

type MachineDriver interface {
	Check(context.Context) (Capabilities, error)
	Start(context.Context, VM, Spec) error
	Inspect(context.Context, VM) (bool, error)
	Configure(context.Context, VM, Request) (GuestStatus, error)
	Stop(context.Context, VM) error
	Remove(context.Context, VM) error
	Guest(context.Context, VM, string, io.Reader) (GuestStatus, error)
}
type Host struct {
	PreparePublish func(int) error
	Store          *Store
	Driver         MachineDriver
	Limits         Limits
	Key            []byte
}

func (h *Host) fingerprint(q Request) (string, error) {
	raw, err := json.Marshal(struct {
		Scope   Scope
		Spec    Spec
		Values  Values
		Runtime string
	}{q.Scope, q.Spec, q.Values, q.RuntimeIdentity})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, h.Key)
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}
func (h *Host) verify(ctx context.Context, q Request) error {
	if err := q.Validate(time.Now()); err != nil {
		return err
	}
	return h.verifyAuthority(ctx, q)
}
func (h *Host) verifyAuthority(ctx context.Context, q Request) error {
	if h.Store == nil || h.Driver == nil || len(h.Key) != 32 {
		return ErrUnavailable
	}
	if err := q.ValidateAuthority(time.Now()); err != nil {
		return err
	}
	c, err := h.Driver.Check(ctx)
	if err != nil {
		return err
	}
	if !c.Isolation || c.RuntimeIdentity != q.RuntimeIdentity {
		return ErrUnavailable
	}
	return nil
}
func (h *Host) Apply(ctx context.Context, q Request) (Record, error) {
	if err := h.verify(ctx, q); err != nil {
		return Record{}, err
	}
	h.Store.mu.Lock()
	defer h.Store.mu.Unlock()
	if !q.Authority.Expires.After(time.Now()) {
		return Record{}, ErrConflict
	}
	r, err := h.Store.Get(ctx, q.Scope)
	if err != nil && !errors.Is(err, ErrMissing) {
		return Record{}, err
	}
	digest, err := h.fingerprint(q)
	if err != nil {
		return Record{}, err
	}
	if r.Generation > q.Authority.Generation || (r.Generation == q.Authority.Generation && (r.Operation != q.Authority.Operation || r.Digest != digest)) {
		return r, ErrConflict
	}
	if r.Generation == q.Authority.Generation && (r.State == "removed" || r.State == "stopped") {
		return r, ErrConflict
	}
	if r.Generation == q.Authority.Generation && r.State == "ready" && r.Current != nil {
		live, e := h.Driver.Inspect(ctx, *r.Current)
		if e != nil {
			return r, e
		}
		if !live {
			return r, fmt.Errorf("%w: recorded VM is absent; authorize a new generation", ErrConflict)
		}
		err = h.finishReady(ctx, &r)
		return r, err
	}
	if r.Generation < q.Authority.Generation {
		resume := r.State == "stopped" && r.Current != nil && r.Current.Spec.Kind == "app" && firecrackerSameSpec(r.Current.Spec, q.Spec)
		if resume {
			r.Pending = r.Current
			r.Current = nil
		}
		if r.Pending != nil && !resume {
			r.Garbage = append(r.Garbage, *r.Pending)
			r.Pending = nil
		}
		r.Scope = q.Scope
		r.Generation = q.Authority.Generation
		r.Operation = q.Authority.Operation
		r.Digest = digest
		r.RuntimeIdentity = q.RuntimeIdentity
		r.State = "preparing"
		if checkErr := h.Store.Save(ctx, r); checkErr != nil {
			return r, checkErr
		}
	}
	if checkErr := h.cleanup(ctx, &r); checkErr != nil {
		return r, checkErr
	}
	if r.Pending == nil {
		slot, e := h.reserve(ctx, q.Scope, q.Spec)
		if e != nil {
			return r, e
		}
		id := sha256.Sum256([]byte(q.Scope.Key() + fmt.Sprintf("/%d", q.Authority.Generation)))
		network := q.Scope.Network()
		if q.Spec.Kind == "build" {
			network = "build-" + q.Scope.Key()[:24]
		}
		r.Pending = &VM{ID: hex.EncodeToString(id[:16]), Slot: slot, Spec: q.Spec, Network: network, Created: time.Now().UTC()}
		r.State = "starting"
		if checkErr := h.Store.Save(ctx, r); checkErr != nil {
			return r, checkErr
		}
	}
	vm := *r.Pending
	// Start is idempotent for this exact VM identity; partial starts remain journaled.
	if !q.Authority.Expires.After(time.Now()) {
		return r, ErrConflict
	}
	if checkErr := h.Driver.Start(ctx, vm, q.Spec); checkErr != nil {
		return r, checkErr
	}
	if !q.Authority.Expires.After(time.Now()) {
		return r, ErrConflict
	}
	status, err := h.Driver.Configure(ctx, vm, q)
	if err != nil {
		return r, err
	}
	if !q.Authority.Expires.After(time.Now()) {
		return r, ErrConflict
	}
	if !status.Ready && q.Spec.Kind != "build" {
		return r, errors.New("the guest application is not ready")
	}
	if h.PreparePublish != nil {
		if checkErr := h.PreparePublish(q.Spec.PublishedPort); checkErr != nil {
			return r, checkErr
		}
	}
	if r.Current != nil {
		r.Garbage = append(r.Garbage, *r.Current)
	}
	r.Current = r.Pending
	r.Pending = nil
	r.State = "ready"
	r.Updated = time.Now().UTC()
	if checkErr := h.Store.Save(ctx, r); checkErr != nil {
		return r, checkErr
	}
	err = h.finishReady(ctx, &r)
	return r, err
}
func (h *Host) finishReady(ctx context.Context, r *Record) error {
	if h.PreparePublish != nil {
		if err := h.PreparePublish(r.Current.Spec.PublishedPort); err != nil {
			return err
		}
	}
	if network, ok := h.Driver.(interface {
		RefreshNetwork(context.Context, VM) error
	}); ok {
		if err := network.RefreshNetwork(ctx, *r.Current); err != nil {
			return err
		}
	}
	return h.cleanup(ctx, r)
}

func (h *Host) reserve(ctx context.Context, scope Scope, s Spec) (int, error) {
	all, err := h.Store.List(ctx)
	if err != nil {
		return 0, err
	}
	used := map[int]bool{}
	cpus, mem, disk := s.VCPUs, s.MemoryMiB+128, s.DiskMiB
	for _, r := range all {
		if r.Scope != scope && r.Current != nil && s.PublishedPort != 0 && r.Current.Spec.PublishedPort == s.PublishedPort {
			return 0, ErrConflict
		}
		if r.Scope != scope && r.Pending != nil && s.PublishedPort != 0 && r.Pending.Spec.PublishedPort == s.PublishedPort {
			return 0, ErrConflict
		}
		vms := append([]VM{}, r.Garbage...)
		if r.Current != nil {
			vms = append(vms, *r.Current)
		}
		if r.Pending != nil {
			vms = append(vms, *r.Pending)
		}
		for _, v := range vms {
			used[v.Slot] = true
			cpus += v.Spec.VCPUs
			mem += v.Spec.MemoryMiB + 128
			disk += v.Spec.DiskMiB
		}
	}
	if len(used) >= h.Limits.VMs || cpus > h.Limits.VCPUs || mem > h.Limits.MemoryMiB || disk > h.Limits.DiskMiB {
		return 0, ErrCapacity
	}
	for i := 1; i <= h.Limits.VMs; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, ErrCapacity
}
func (h *Host) cleanup(ctx context.Context, r *Record) error {
	for len(r.Garbage) > 0 {
		v := r.Garbage[0]
		if err := h.Driver.Stop(ctx, v); err != nil {
			return err
		}
		if err := h.Driver.Remove(ctx, v); err != nil {
			return err
		}
		r.Garbage = r.Garbage[1:]
		if err := h.Store.Save(ctx, *r); err != nil {
			return err
		}
	}
	return nil
}
func (h *Host) Inspect(ctx context.Context, s Scope) (Record, error) {
	if !s.Valid() {
		return Record{}, ErrInvalid
	}
	return h.Store.Get(ctx, s)
}
func (h *Host) Mutate(ctx context.Context, q Request, action string) (Record, error) {
	if err := h.verifyAuthority(ctx, q); err != nil {
		return Record{}, err
	}
	if action != "stop" && action != "remove" {
		return Record{}, ErrInvalid
	}
	h.Store.mu.Lock()
	defer h.Store.mu.Unlock()
	if !q.Authority.Expires.After(time.Now()) {
		return Record{}, ErrConflict
	}
	r, err := h.Store.Get(ctx, q.Scope)
	if err != nil {
		return r, err
	}
	digest, err := h.fingerprint(q)
	if err != nil {
		return r, err
	}
	digest = action + ":" + digest
	if q.Authority.Generation < r.Generation || (q.Authority.Generation == r.Generation && (q.Authority.Operation != r.Operation || digest != r.Digest)) {
		return r, ErrConflict
	}
	r.Generation = q.Authority.Generation
	r.Operation = q.Authority.Operation
	r.Digest = digest
	r.State = action + "-pending"
	if checkErr := h.Store.Save(ctx, r); checkErr != nil {
		return r, checkErr
	}
	err = h.finishMutation(ctx, &r)
	return r, err
}
func (h *Host) finishMutation(ctx context.Context, r *Record) error {
	remove := r.State == "remove-pending"
	if r.Current != nil {
		if err := h.Driver.Stop(ctx, *r.Current); err != nil {
			return err
		}
		if remove {
			r.Garbage = append(r.Garbage, *r.Current)
			r.Current = nil
		}
	}
	if r.Pending != nil {
		r.Garbage = append(r.Garbage, *r.Pending)
		r.Pending = nil
	}
	// Persist retirement before deleting any allocation so cleanup can be replayed.
	if err := h.Store.Save(ctx, *r); err != nil {
		return err
	}
	if err := h.cleanup(ctx, r); err != nil {
		return err
	}
	r.State = "stopped"
	if remove {
		r.State = "removed"
	}
	r.Updated = time.Now().UTC()
	return h.Store.Save(ctx, *r)
}

func (h *Host) Guest(ctx context.Context, q Request, action string, body io.Reader) (GuestStatus, error) {
	if err := h.verify(ctx, q); err != nil {
		return GuestStatus{}, err
	}
	h.Store.mu.Lock()
	defer h.Store.mu.Unlock()
	if !q.Authority.Expires.After(time.Now()) {
		return GuestStatus{}, ErrConflict
	}
	r, err := h.Store.Get(ctx, q.Scope)
	if err != nil {
		return GuestStatus{}, err
	}
	if r.Generation != q.Authority.Generation || r.Operation != q.Authority.Operation || r.Current == nil || r.State != "ready" {
		return GuestStatus{}, ErrConflict
	}
	if action != "status" && action != "logs" && action != "source" {
		return GuestStatus{}, ErrInvalid
	}
	if action == "source" && r.Current.Spec.Kind != "build" {
		return GuestStatus{}, ErrInvalid
	}
	call, cancel := context.WithDeadline(ctx, q.Authority.Expires)
	defer cancel()
	return h.Driver.Guest(call, *r.Current, action, body)
}

func firecrackerSameSpec(a, b Spec) bool { return reflect.DeepEqual(a, b) }
