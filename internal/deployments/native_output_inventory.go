package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/xraph/loomplane/internal/planning"
)

// nativeOutputInventory records names only. Its durable union survives a value
// write that commits before returning an error, so cleanup can still find it.
type nativeOutputInventory struct {
	mu                     sync.Mutex
	store                  ResourceSecrets
	org, environment, name string
	touched                map[string]bool
	strict                 bool
}

func nativeOutputProvider(name string) bool {
	return name == "vercel" || name == "cloudflare-workers" || name == "cloudflare"
}

func nativeOutputBinding(binding *planning.Binding) bool {
	if binding == nil || binding.Subject != planning.ResourceSubject || planning.ExistingResourceBinding(binding) {
		return false
	}
	if nativeOutputProvider(binding.Provider) {
		return true
	}
	for i := range binding.Replaced {
		if nativeOutputProvider(binding.Replaced[i].Provider) && binding.Replaced[i].Subject == planning.ResourceSubject && !planning.ExistingResourceBinding(&binding.Replaced[i]) {
			return true
		}
	}
	return false
}

func (r *Runner) trackNativeOutputs(ctx context.Context, job *Job) error {
	a := job.Action
	if a.Subject != planning.ResourceSubject || a.Resource != nil && a.Resource.ExistingResource != nil || planning.ExistingResourceBinding(job.Binding) {
		return nil
	}
	i := &nativeOutputInventory{store: r.resources, org: job.Deployment.OrgID, environment: job.Deployment.EnvironmentID, name: a.Name, touched: map[string]bool{}, strict: nativeOutputProvider(a.Provider)}
	if !i.strict && !nativeOutputBinding(job.Binding) && !nativeOutputBinding(job.Replaces) {
		if i.store == nil {
			return nil
		}
		previous, readErr := i.store.Get(ctx, i.key(apiOutputFields))
		if errors.Is(readErr, ErrNoCredential) || readErr == nil && len(previous) == 0 {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if _, inventoryErr := i.fields(ctx); inventoryErr != nil {
			return inventoryErr
		}
	}
	job.nativeOutputs = i
	keep := job.Keep
	job.Keep = func(ctx context.Context, field string, value []byte) error {
		if strings.HasPrefix(field, "output:") {
			if !validExternalOutputField(field) {
				if !i.strict {
					return keep(ctx, field, value)
				}
				return errors.New("the native application output name is invalid")
			}
			return i.keep(ctx, field, value, keep)
		}
		if i.strict && !slices.Contains(resourceCredentialFields, field) {
			return errors.New("the native resource credential field has no cleanup contract")
		}
		return keep(ctx, field, value)
	}
	job.Forget = i.forget
	return nil
}

func (i *nativeOutputInventory) key(field string) string {
	return resourceKey(i.org, i.environment, i.name, field)
}

func (i *nativeOutputInventory) fields(ctx context.Context) ([]string, error) {
	if i.store == nil {
		return nil, errors.New("no resource credential store")
	}
	raw, err := i.store.Get(ctx, i.key(apiOutputFields))
	if errors.Is(err, ErrNoCredential) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var fields []string
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("the native output inventory is unreadable")
	}
	for _, field := range fields {
		if !validExternalOutputField(field) {
			return nil, errors.New("the native output inventory is invalid")
		}
	}
	slices.Sort(fields)
	return slices.Compact(fields), nil
}

func (i *nativeOutputInventory) save(ctx context.Context, fields []string) error {
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return i.store.Put(ctx, i.key(apiOutputFields), raw)
}

func (i *nativeOutputInventory) keep(ctx context.Context, field string, value []byte, put func(context.Context, string, []byte) error) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	fields, err := i.fields(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(fields, field) {
		fields = append(fields, field)
		slices.Sort(fields)
	}
	if saveErr := i.save(ctx, fields); saveErr != nil {
		return saveErr
	}
	if putErr := put(ctx, field, value); putErr != nil {
		return putErr
	}
	i.touched[field] = true
	return nil
}

// finishNativeOutputs runs only after the executor succeeds, before its new
// binding is saved. Failed attempts retain every previously recorded name.
func (job *Job) finishNativeOutputs(ctx context.Context) error {
	i := job.nativeOutputs
	if i == nil || i.store == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	previous, err := i.fields(ctx)
	if err != nil {
		return err
	}
	for _, field := range previous {
		if i.touched[field] {
			continue
		}
		if deleteErr := i.store.Delete(ctx, i.key(field)); deleteErr != nil {
			return deleteErr
		}
	}
	fields := make([]string, 0, len(i.touched))
	for field := range i.touched {
		fields = append(fields, field)
	}
	slices.Sort(fields)
	if len(fields) == 0 {
		return i.store.Delete(ctx, i.key(apiOutputFields))
	}
	return i.save(ctx, fields)
}

func (i *nativeOutputInventory) forget(ctx context.Context) error {
	if i.store == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	fields, err := i.fields(ctx)
	if err != nil {
		return err
	}
	fields = append(fields, resourceCredentialFields...)
	slices.Sort(fields)
	for _, field := range slices.Compact(fields) {
		if deleteErr := i.store.Delete(ctx, i.key(field)); deleteErr != nil {
			return deleteErr
		}
	}
	// Remove the inventory last. A partial delete leaves proof for the retry.
	return i.store.Delete(ctx, i.key(apiOutputFields))
}
