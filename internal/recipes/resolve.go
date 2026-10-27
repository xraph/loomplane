package recipes

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Resolve pins a composed kind's release for one architecture and target.
// Components come in start order: a component's Step is 0 when it names
// nothing in After, otherwise one more than the deepest component it
// names; within a step, jobs come before processes.
func (r *Registry) Resolve(kind, version, architecture, target string) (*Composition, error) {
	k, ok := r.Kind(kind)
	if !ok {
		return nil, fmt.Errorf("%s is not a composed catalog kind", kind)
	}
	if !slices.Contains(k.Targets, target) {
		return nil, fmt.Errorf("%s doesn't run on %s targets", kind, target)
	}
	release, ok := k.release(version)
	if !ok {
		return nil, fmt.Errorf("%s %s has no supported release", kind, version)
	}
	if !slices.Contains(architectures, architecture) {
		return nil, fmt.Errorf("%s recipe requires a checked amd64 or arm64 target, got %q", kind, architecture)
	}

	c := &Composition{Kind: k.Name, Version: release.Version, Product: release.Product, Revision: release.Revision, Architecture: architecture, Target: target}
	for _, component := range k.Components {
		image := release.Images[component.Name]
		if _, ok := image.Digests[architecture]; !ok {
			return nil, fmt.Errorf("%s runs on NVIDIA GPUs and needs an amd64 target", kind)
		}
		c.Components = append(c.Components, ResolvedComponent{
			Component: component,
			Image:     image.Repository + "@" + image.Digests[architecture],
		})
	}
	c.rank()
	return c, nil
}

// Without is the composition with every component that requires an off
// dependency dropped, dropped from every After, and the rest ranked again
// from step 0. The receiver is unchanged.
func (c *Composition) Without(off map[string]bool) *Composition {
	if c == nil {
		return nil
	}
	out := *c
	out.Components = nil
	dropped := map[string]bool{}
	for _, rc := range c.Components {
		if rc.Requires != "" && off[rc.Requires] {
			dropped[rc.Name] = true
		}
	}
	for _, rc := range c.Components {
		if dropped[rc.Name] {
			continue
		}
		rc.Component = rc.clone()
		if slices.ContainsFunc(rc.After, func(name string) bool { return dropped[name] }) {
			rc.After = slices.DeleteFunc(rc.After, func(name string) bool { return dropped[name] })
			if len(rc.After) == 0 {
				rc.After = nil
			}
		}
		out.Components = append(out.Components, rc)
	}
	out.rank()
	return &out
}

// rank sets each component's Step and puts the components in start order:
// a component's Step is 0 when it names nothing in After, otherwise one
// more than the deepest component it names; within a step, jobs come
// before processes.
func (c *Composition) rank() {
	byName := map[string]Component{}
	for _, rc := range c.Components {
		byName[rc.Name] = rc.Component
	}
	steps := map[string]int{}
	var step func(name string) int
	step = func(name string) int {
		if s, ok := steps[name]; ok {
			return s
		}
		s := 0
		for _, after := range byName[name].After {
			s = max(s, step(after)+1)
		}
		steps[name] = s
		return s
	}
	for i := range c.Components {
		c.Components[i].Step = step(c.Components[i].Name)
	}
	sort.SliceStable(c.Components, func(i, j int) bool {
		a, b := c.Components[i], c.Components[j]
		if a.Step != b.Step {
			return a.Step < b.Step
		}
		return a.Role == RoleJob && b.Role != RoleJob
	})
}

// release finds a release by version; "" is the newest.
func (k Kind) release(version string) (Release, bool) {
	if version == "" && len(k.Releases) > 0 {
		return k.Releases[0], true
	}
	for _, release := range k.Releases {
		if release.Version == version {
			return release, true
		}
	}
	return Release{}, false
}

// LookupEngine resolves an engine's recipe. Built-in engines go through
// Lookup and KubernetesPostgres unchanged; a registered engine's recipe
// carries its Family, so it runs, backs up and restores as that family.
func (r *Registry) LookupEngine(kind, version, architecture, target string) (*Recipe, error) {
	e, ok := r.engine(kind)
	if !ok {
		return nil, fmt.Errorf("%s is not a catalog engine", kind)
	}
	if version == "" && len(e.Releases) > 0 {
		version = e.Releases[0].Version
	}
	if e.builtin {
		return builtinLookup(kind, version, architecture, target)
	}
	if !slices.Contains(e.Targets, target) {
		return nil, fmt.Errorf("%s has no qualified %s database recipe", target, kind)
	}
	i := slices.IndexFunc(e.Releases, func(release EngineRelease) bool { return release.Version == version })
	if i < 0 {
		return nil, fmt.Errorf("%s %s has no supported recipe", kind, version)
	}
	release := e.Releases[i]
	digest, ok := release.Image.Digests[architecture]
	if !ok {
		return nil, fmt.Errorf("%s recipe requires a checked amd64 or arm64 target, got %q", kind, architecture)
	}
	recipe := &Recipe{
		ID: kind + "-" + release.Version, Revision: release.Revision, ProductVersion: release.Product,
		Image: release.Image.Repository + "@" + digest, Architecture: architecture,
		MountPath: release.MountPath, BackupFormat: release.BackupFormat, Family: e.Family,
		FSGroup: release.FSGroup,
	}
	if len(release.Extensions) > 0 {
		recipe.Extensions = slices.Clone(release.Extensions)
	}
	if target == "kubernetes" {
		recipe.ID += "-kubernetes"
		recipe.VolumeSizeMB = release.KubernetesVolumeMB
	}
	return recipe, nil
}

// ForGPUs is what a component placed with count GPUs runs with: r plus
// count times its PerGPU step. The result has no step, so it's never
// added twice.
func (r Resources) ForGPUs(count int) Resources {
	step := r.PerGPU
	r.PerGPU = nil
	if step == nil || count < 1 {
		return r
	}
	r.CPUMillis += count * step.CPUMillis
	r.MemoryMB += count * step.MemoryMB
	r.ShmMB += count * step.ShmMB
	return r
}

// SizeMB is the weight cache's PVC request for a resource with settings:
// the value of SizeSetting, in MB, when it's larger than
// KubernetesSizeMB, else KubernetesSizeMB. A value that isn't digits
// changes nothing.
func (w WeightCache) SizeMB(settings map[string]string) int {
	raw := settings[w.SizeSetting]
	if w.SizeSetting == "" || raw == "" || strings.Trim(raw, "0123456789") != "" {
		return w.KubernetesSizeMB
	}
	mb, err := strconv.Atoi(raw)
	if err != nil {
		return w.KubernetesSizeMB
	}
	return max(w.KubernetesSizeMB, mb)
}
