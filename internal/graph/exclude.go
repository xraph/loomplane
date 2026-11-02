package graph

import (
	"path"
	"slices"
	"strings"
)

// Exclusion kinds. A service exclusion names one service by ID; a path
// exclusion names a directory glob, so services added under it later are
// excluded too.
const (
	ExcludeService = "service"
	ExcludePath    = "path"
)

// ExclusionRule is one of a project's exclusions, as the graph needs it.
type ExclusionRule struct {
	Kind  string `json:"kind"`
	Match string `json:"match"`
}

// Excluded is a service a project excluded, with the rule that did it.
type Excluded struct {
	Service Service       `json:"service"`
	Rule    ExclusionRule `json:"rule"`
}

// Matches reports whether the rule excludes s. A path rule matches the
// service's directory or entrypoint: "tests/acceptance/**" takes
// everything under tests/acceptance, and a pattern with no wildcard
// takes that directory and everything under it.
func (r ExclusionRule) Matches(s Service) bool {
	switch r.Kind {
	case ExcludeService:
		return r.Match == s.ID
	case ExcludePath:
		return matchPath(r.Match, s.Path) || matchPath(r.Match, s.Entrypoint)
	}
	return false
}

func matchPath(pattern, name string) bool {
	pattern = strings.Trim(path.Clean(strings.TrimSpace(pattern)), "/")
	name = strings.Trim(path.Clean(name), "/")
	if pattern == "" || pattern == "." || name == "" || name == "." {
		return false
	}
	if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
		return name == dir || strings.HasPrefix(name, dir+"/")
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return name == pattern || strings.HasPrefix(name, pattern+"/")
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// Exclude returns g without the services rules exclude, and those services
// with the first rule that matched each. Edges from or to an excluded
// service go with it. Packages and resources stay: other services may
// still use them, and a resource nothing needs any more is harmless.
// With no rules, g comes back as it is.
func Exclude(g *Graph, rules []ExclusionRule) (*Graph, []Excluded) {
	out := []Excluded{}
	if g == nil || len(rules) == 0 {
		return g, out
	}
	gone := map[string]bool{}
	for _, s := range g.Services {
		for _, r := range rules {
			if r.Matches(s) {
				gone[s.ID] = true
				out = append(out, Excluded{Service: s, Rule: r})
				break
			}
		}
	}
	if len(gone) == 0 {
		return g, out
	}
	kept := *g
	kept.Services = slices.DeleteFunc(slices.Clone(g.Services), func(s Service) bool { return gone[s.ID] })
	kept.Edges = slices.DeleteFunc(slices.Clone(g.Edges), func(e Edge) bool { return gone[e.From] || gone[e.To] })
	return &kept, out
}
