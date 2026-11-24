package planning

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
)

// addressParts are the parts of an address a variable can hold.
var addressParts = []string{"host", "port", "user", "password", "database"}

// A mapping can only name an output the reviewed service already receives.
// A workspace reference to an inventory ID also works across different manifest names.
func (p *planner) mapResourceOutputs(a *Action) {
	rules, ok := p.in.ResourceMappings[a.Service.Service]
	if !ok {
		rules = p.in.ResourceMappings[""]
	}
	links := a.Service.Env
	for _, key := range slices.Sorted(maps.Keys(rules)) {
		ref := rules[key]
		resource, field, _ := strings.Cut(ref, ".")
		if field == "" {
			field = "url"
		}
		var selected *EnvLink
		linked, ambiguous := false, false
		for i := range links {
			link := &links[i]
			if link.Subject != ResourceSubject || (link.From != resource && p.in.Manifest.Resources[link.From].ExistingResource != resource) {
				continue
			}
			linked = true
			if link.Field == "output:CLOUDFLARE_BINDING" {
				continue
			}
			output := strings.TrimPrefix(strings.TrimPrefix(link.Field, "output:"), "consumer:")
			if output == "" {
				output = "url"
			}
			if output != field {
				continue
			}
			if selected != nil && (selected.From != link.From || selected.Field != link.Field) {
				ambiguous = true
				break
			}
			if selected == nil || link.Key == key {
				selected = link
			}
		}
		if !linked {
			continue
		}
		if ambiguous {
			p.problem(Finding{Code: "resource-variable-ambiguous", Subject: ServiceSubject, Name: a.Name, Path: "variables." + key, Message: fmt.Sprintf("More than one attachment supplies %s to %s. Choose its manifest name for %s.", ref, a.Name, key)})
			continue
		}
		// A database or cache's address has parts a variable can hold, as
		// a wired one does.
		if selected == nil && slices.Contains(addressParts, field) {
			for i := range links {
				link := &links[i]
				if link.Subject == ResourceSubject && link.Field == "" && link.Part == "" && link.Gateway == "" &&
					(link.From == resource || p.in.Manifest.Resources[link.From].ExistingResource == resource) &&
					catalogconnect.EngineForKind(p.in.Manifest.Resources[link.From].Kind) != "" {
					part := *link
					part.Part = field
					selected = &part
					break
				}
			}
		}
		if selected == nil {
			p.problem(Finding{Code: "resource-variable-output", Subject: ServiceSubject, Name: a.Name, Path: "variables." + key, Message: fmt.Sprintf("Choose an available output from %s for %s on %s.", resource, key, a.Name)})
			continue
		}
		if selected.Key == key && selected.Part == "" {
			continue
		}
		if slices.ContainsFunc(links, func(link EnvLink) bool { return link.Key == key }) {
			p.problem(Finding{Code: "resource-variable-conflict", Subject: ServiceSubject, Name: a.Name, Path: "variables." + key, Message: fmt.Sprintf("%s already receives another output as %s. Choose a different variable name.", a.Name, key)})
			continue
		}
		alias := *selected
		alias.Key = key
		a.Service.Env = append(a.Service.Env, alias)
	}
}
