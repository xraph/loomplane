package planning

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// gpuPlacement checks want against the connection and returns the reviewed
// placement, or adds a Finding and returns nil. A connection whose GPUs
// were never read, or can't be, never fits. How many GPUs other workloads
// on a server already have is checkServerGPUs's to count, once the plan
// has every action.
func (p *planner) gpuPlacement(subject Subject, name, path string, connection *ConnectionOption, want provider.GPURequest, minMemoryGiB int) *provider.GPUPlacement {
	problem := func(code, message string) *provider.GPUPlacement {
		p.problem(Finding{Code: code, Subject: subject, Name: name, Path: path, Message: message})
		return nil
	}
	if connection == nil {
		return nil // the target's own problem says why
	}
	if !OwnedCloud(connection.Cloud) {
		return problem("gpu-target-unsupported", "GPUs run on a server or Kubernetes target.")
	}
	if connection.Details["architecture"] == "arm64" {
		return problem("gpu-architecture", "GPU workloads need an amd64 target.")
	}
	if _, known := provider.LookupAcceleratorKind(want.Kind); !known && minMemoryGiB > 0 {
		return problem("catalog-gpu-memory", fmt.Sprintf("Loomplane doesn't know how much memory a %s GPU has, and %s needs at least %d GiB per GPU. Choose a listed GPU kind.", want.Kind, name, minMemoryGiB))
	}
	inv, ok := provider.ParseGPUInventory(connection.Details)
	if !ok {
		inv = provider.GPUInventory{Status: provider.GPUStatusUnknown}
	}
	placed, err := inv.Place(want)
	if err != nil {
		var unavailable *provider.GPUUnavailableError
		if errors.As(err, &unavailable) {
			return problem(unavailable.Code, unavailable.Message)
		}
		return problem("gpu-request-invalid", err.Error())
	}
	return placed
}

// componentGPU settles a catalog component's GPU request: the kind the
// component fixes, else the one the resource chooses, and the larger of
// their counts. It returns nil for a component that uses no GPU.
func (p *planner) componentGPU(name, path string, kind recipes.Kind, c recipes.Component, chosen *manifest.GPU, connection *ConnectionOption) *provider.GPUPlacement {
	g := c.Resources.GPU
	if g == nil {
		return nil
	}
	want := provider.GPURequest{Kind: g.Kind, Count: g.Count}
	if chosen != nil {
		if want.Kind == "" {
			want.Kind = chosen.Kind
		}
		want.Count = max(want.Count, chosen.Count)
	}
	if want.Kind == "" {
		p.problem(Finding{Code: "catalog-gpu-required", Subject: ResourceSubject, Name: name, Path: path, Message: fmt.Sprintf("Choose a GPU for %s with gpu: {kind, count}.", name)})
		return nil
	}
	if k, ok := provider.LookupAcceleratorKind(want.Kind); ok && k.MemoryGiB < g.MinMemoryGiB {
		p.problem(Finding{Code: "catalog-gpu-memory", Subject: ResourceSubject, Name: name, Path: path, Message: fmt.Sprintf("%s has %d GiB per GPU; %s needs at least %d.", k.Title, k.MemoryGiB, kind.Title, g.MinMemoryGiB)})
		return nil
	}
	return p.gpuPlacement(ResourceSubject, name, path, connection, want, g.MinMemoryGiB)
}

// gpuKind reports whether a kind runs a GPU component, leaving out the
// ones that require a dependency that's off.
func gpuKind(kind recipes.Kind, off map[string]bool) bool {
	return slices.ContainsFunc(kind.Components, func(c recipes.Component) bool {
		return c.Resources.GPU != nil && (c.Requires == "" || !off[c.Requires])
	})
}

// gpuOnArm64 reports, as a problem, a GPU kind planned onto an arm64
// target, before resolving its images says only that one is missing.
func (p *planner) gpuOnArm64(name, path string, kind recipes.Kind, off map[string]bool, connection *ConnectionOption) bool {
	if connection.Details["architecture"] != "arm64" || !gpuKind(kind, off) {
		return false
	}
	p.problem(Finding{Code: "gpu-architecture", Subject: ResourceSubject, Name: name, Path: path, Message: fmt.Sprintf("%s runs on NVIDIA GPUs and needs an amd64 target.", kind.Title)})
	return true
}

// gpuPath is where a resource's gpu setting is in the manifest: under the
// environment when it sets its own.
func (p *planner) gpuPath(name string) string {
	r := p.in.Manifest.Resources[name]
	if o, ok := r.Environments[p.in.Environment]; ok && o.GPU != nil {
		return "resources." + name + ".environments." + p.in.Environment + ".gpu"
	}
	return "resources." + name + ".gpu"
}

// unusedGPU warns about a resource's gpu setting that nothing it runs uses.
func (p *planner) unusedGPU(name, title string) {
	p.warn(Finding{Code: "gpu-unused", Subject: ResourceSubject, Name: name, Path: p.gpuPath(name), Message: fmt.Sprintf("%s doesn't use GPUs, so its gpu setting is ignored.", title)})
}

// actionGPU is the GPU placement an action holds on a server, and the
// manifest path that asks for it. A service on a Cloud pool, or one waiting
// for pool capacity, gets its GPUs from the pool's reservation instead.
func actionGPU(a *Action) (gpu *provider.GPUPlacement, path string) {
	switch {
	case a.Service != nil:
		cfg := a.Service
		if cfg.GPU == nil || cfg.CapacityIntent != nil || (cfg.CloudPlacement != nil && cfg.CloudPlacement.PoolID != "") {
			return nil, ""
		}
		return cfg.GPU, "services." + a.Name + ".size.gpu"
	case a.Resource != nil && a.Resource.Component != nil:
		parent, _, _ := strings.Cut(a.Name, "--")
		return a.Resource.Component.GPU, "resources." + parent + ".gpu"
	}
	return nil, ""
}

// boundGPU is the GPU placement a deployed service or component holds.
func boundGPU(b *Binding) *provider.GPUPlacement {
	var cfg struct {
		GPU       *provider.GPUPlacement `json:"gpu"`
		Component *struct {
			GPU *provider.GPUPlacement `json:"gpu"`
		} `json:"component"`
	}
	if json.Unmarshal(b.Config, &cfg) != nil {
		return nil
	}
	if cfg.GPU != nil {
		return cfg.GPU
	}
	if cfg.Component != nil {
		return cfg.Component.GPU
	}
	return nil
}

// checkServerGPUs reports a server whose GPUs of a kind this plan gives
// out more of than it has. What's deployed there and isn't planned again
// holds its GPUs, even when this plan deletes it, since deletes run last;
// what's planned again holds what it's planned with. Other environments'
// workloads aren't visible here; the runner checks the server itself.
func (p *planner) checkServerGPUs() {
	type holder struct {
		name  string
		count int
	}
	planned := map[string]bool{}
	for _, a := range p.actions {
		planned[a.ID] = true
	}
	holders := map[[2]string][]holder{}
	for _, id := range slices.Sorted(maps.Keys(p.bindings)) {
		b := p.bindings[id]
		if planned[id] || b.ConnectionID == "" {
			continue
		}
		if g := boundGPU(b); g != nil {
			key := [2]string{b.ConnectionID, g.Kind}
			holders[key] = append(holders[key], holder{b.Name, g.Count})
		}
	}
	for _, a := range p.actions {
		g, path := actionGPU(a)
		if g == nil || a.Op == Delete {
			continue
		}
		i := slices.IndexFunc(p.in.Connections, func(c ConnectionOption) bool { return c.ID == a.ConnectionID })
		if i < 0 || p.in.Connections[i].Cloud != "server" {
			continue
		}
		c := p.in.Connections[i]
		inv, _ := provider.ParseGPUInventory(c.Details)
		have := 0
		for _, d := range inv.Devices {
			if d.Kind == g.Kind {
				have++
			}
		}
		key := [2]string{c.ID, g.Kind}
		used, names := 0, []string{}
		for _, h := range holders[key] {
			used += h.count
			names = append(names, h.name)
		}
		if used > 0 && used+g.Count > have {
			title := g.Kind
			if k, ok := provider.LookupAcceleratorKind(g.Kind); ok {
				title = k.Title
			}
			sort.Strings(names)
			p.problem(Finding{
				Code: provider.GPUCountUnavailable, Subject: a.Subject, Name: a.Name, Path: path,
				Message: fmt.Sprintf("%s has %d %s GPUs and %d are already given to %s.", c.Name, have, title, used, strings.Join(names, ", ")),
			})
			continue
		}
		holders[key] = append(holders[key], holder{a.Name, g.Count})
	}
}

// composedGPU reports whether a composed resource runs anything that uses
// its gpu setting: one of its components, or a dependency it provisions.
func (p *planner) composedGPU(composition *recipes.Composition, choices map[string]DependencyChoice) bool {
	if slices.ContainsFunc(composition.Components, func(c recipes.ResolvedComponent) bool { return c.Resources.GPU != nil }) {
		return true
	}
	for _, choice := range choices {
		if choice.Mode != DependencyNew {
			continue
		}
		if a := p.resources[choice.Resource]; a != nil && a.Resource != nil && a.Resource.Component != nil && a.Resource.Component.Component.Resources.GPU != nil {
			return true
		}
	}
	return false
}

// gpuText is a GPU placement as review shows it.
func gpuText(g *provider.GPUPlacement) string {
	if g == nil {
		return ""
	}
	return render(g)
}

// serviceGPU settles a service's GPUs from its size: on a server or a
// cluster it reaches directly, the reviewed placement; anywhere else, a
// problem. A service on a Cloud pool, or waiting for pool capacity, gets
// its GPUs from the pool's reservation, so planning doesn't fit them here.
// It gets the kind and count alone, for the runtime to assign devices, and
// never a GPU placement it held on a server or cluster before.
func (p *planner) serviceGPU(name, base string, prov *provider.Provider, set manifest.Settings, cfg *ServiceConfig, c *ConnectionOption) {
	if set.Size == nil || set.Size.GPU == nil {
		if OwnedCloud(prov.Cloud) {
			cfg.GPU = nil
		}
		return
	}
	path := base + ".size.gpu"
	if !OwnedCloud(prov.Cloud) {
		p.problem(Finding{Code: "gpu-target-unsupported", Subject: ServiceSubject, Name: name, Path: path, Message: "GPUs run on a server or Kubernetes target."})
		return
	}
	if p.pooled(name, cfg) {
		cfg.GPU = &provider.GPUPlacement{GPURequest: provider.GPURequest{Kind: set.Size.GPU.Kind, Count: set.Size.GPU.Count}}
		return
	}
	cfg.GPU = p.gpuPlacement(ServiceSubject, name, path, c, provider.GPURequest{Kind: set.Size.GPU.Kind, Count: set.Size.GPU.Count}, 0)
}

// pooled reports whether a service runs on a Cloud pool or waits for pool
// capacity.
func (p *planner) pooled(name string, cfg *ServiceConfig) bool {
	if cfg.CapacityIntent != nil {
		return true
	}
	pin, ok := p.in.CloudPlacements[name]
	return ok && pin.PoolID != ""
}
