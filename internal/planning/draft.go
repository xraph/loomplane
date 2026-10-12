package planning

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/graph"
	"github.com/xraph/loomplane/pkg/manifest"
	"github.com/xraph/loomplane/pkg/provider"
)

// nameLimit is the longest name a manifest accepts.
const nameLimit = 40

var notName = regexp.MustCompile(`[^a-z0-9]+`)

// Draft proposes a first manifest from what analysis found, for a project
// that has none: every deployable service on the first target in the
// catalog that runs its kind, and every resource found with good evidence
// on a target that provisions it, preferring the cloud most services
// landed on. A resource found only from weak evidence is left out, so the
// plan asks about it instead of provisioning something nobody uses. So is
// one no target provisions, such as Kafka today: drafting it would block
// the first plan, where leaving it out gets a warning you can act on. The
// draft is a starting point to review, never saved on its own.
func Draft(g *graph.Graph, reg *provider.Registry) *manifest.Manifest {
	m := &manifest.Manifest{
		Version: manifest.Version, Services: map[string]manifest.Service{}, Resources: map[string]manifest.Resource{},
	}
	taken := map[string]bool{}
	names := map[string]string{} // graph ID to manifest name
	clouds := map[string]int{}
	for _, gs := range g.Services {
		if !gs.Deployable {
			continue
		}
		name := unique(nameOf(gs.Name, "service"), taken)
		names[gs.ID] = name
		s := manifest.Service{Source: strings.TrimPrefix(gs.ID, "svc:")}
		if able := reg.Running(provider.Workload(gs.Kind)); len(able) > 0 {
			s.Provider = able[0].Name
			clouds[able[0].Cloud]++
		}
		m.Services[name] = s
	}

	for _, r := range g.Resources {
		if r.NeedsConfirmation {
			continue
		}
		prov := provisioner(reg, r.Kind, clouds)
		if prov == "" {
			continue
		}
		name := unique(nameOf(r.Kind, "resource"), taken)
		names[r.ID] = name
		m.Resources[name] = manifest.Resource{Kind: r.Kind, Provider: prov}
	}

	// A service needs what it reaches directly, and what the packages it
	// builds with reach.
	uses := map[string][]string{}
	for _, e := range g.Edges {
		uses[e.From] = append(uses[e.From], e.To)
	}
	for _, gs := range g.Services {
		name, ok := names[gs.ID]
		if !ok {
			continue
		}
		var needs []string
		for _, to := range reach(gs.ID, uses) {
			if need, ok := names[to]; ok && need != name && !slices.Contains(needs, need) {
				needs = append(needs, need)
			}
		}
		slices.Sort(needs)
		s := m.Services[name]
		s.Needs = needs
		m.Services[name] = s
	}
	if len(m.Resources) == 0 {
		m.Resources = nil
	}
	if len(m.Services) == 0 {
		m.Services = nil
	}
	return m
}

// reach lists every node from can reach, in the order found.
func reach(from string, edges map[string][]string) []string {
	seen := map[string]bool{from: true}
	var out []string
	queue := []string{from}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		for _, to := range edges[at] {
			if !seen[to] {
				seen[to] = true
				out = append(out, to)
				queue = append(queue, to)
			}
		}
	}
	return out
}

// provisioner picks a target for a resource kind: one on the cloud the
// most services use, or else the first in the catalog.
func provisioner(reg *provider.Registry, kind string, clouds map[string]int) string {
	able := reg.Provisioning(kind)
	if len(able) == 0 {
		return ""
	}
	best := able[0]
	for _, p := range able[1:] {
		if clouds[p.Cloud] > clouds[best.Cloud] {
			best = p
		}
	}
	return best.Name
}

// nameOf turns what analysis calls something into a manifest name.
func nameOf(s, fallback string) string {
	n := strings.Trim(notName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = fallback + "-" + n
	}
	return strings.TrimRight(n[:min(len(n), nameLimit)], "-")
}

// unique numbers name until no service or resource has it.
func unique(name string, taken map[string]bool) string {
	out := name
	for i := 2; taken[out]; i++ {
		suffix := fmt.Sprintf("-%d", i)
		out = strings.TrimRight(name[:min(len(name), nameLimit-len(suffix))], "-") + suffix
	}
	taken[out] = true
	return out
}
