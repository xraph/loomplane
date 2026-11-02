package agentops

import (
	"fmt"

	"github.com/xraph/loomplane/internal/projects"
	defs "github.com/xraph/loomplane/pkg/agentops"
)

// Builtins registers every operation this package serves, over d. It reads
// d's fields at call time, so the caller may fill them after Builtins runs,
// but not after the first call.
func Builtins(r *Registry, d *Deps) {
	r.SetAuditor(func() projects.Auditor { return d.Audit })
	registerProjects(r, d)
	registerRepositories(r, d)
	registerVariables(r, d)
	registerPlans(r, d)
	registerDeployments(r, d)
	registerReleases(r, d)
	registerRequests(r)
}

// def is the shared definition called name. It panics on an unknown name,
// which is a mistake in this package.
func def(name string) defs.Definition {
	d, ok := defs.Lookup(name)
	if !ok {
		panic(fmt.Sprintf("agentops: no definition is called %s", name))
	}
	return d
}
