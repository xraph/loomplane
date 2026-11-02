package recipes

import (
	"maps"
	"slices"
	"strings"
)

// fabriqLibrary shares the hosted stack's pins, dependency requirements and
// privileged jobs. Only the private worker and application credentials differ.
func fabriqLibrary(hosted Kind) Kind {
	library := hosted
	library.Name, library.Title = "fabriq-library", "Fabriq library"
	library.Description = "Engines, migrations and the stock Fabriq worker for a Go app that embeds Fabriq"
	library.ConsumerOptions = nil
	library.Secrets = slices.DeleteFunc(slices.Clone(hosted.Secrets), func(s GeneratedSecret) bool { return s.Name == "admin-key" })
	library.Releases = slices.Clone(hosted.Releases)
	for i := range library.Releases {
		images := maps.Clone(hosted.Releases[i].Images)
		images["worker"] = images["server"]
		delete(images, "server")
		delete(images, "admin")
		library.Releases[i].Images = images
	}
	library.Components = nil
	for _, component := range hosted.Components {
		switch component.Name {
		case "admin":
			continue
		case "server":
			component.Name = "worker"
			component.Env = slices.DeleteFunc(slices.Clone(component.Env), func(e EnvVar) bool { return strings.HasPrefix(e.Name, "FABRIQ_ADMIN_") })
			component.Env = append(component.Env, EnvVar{Name: "FABRIQ_DYNAMIC_SYNC_INTERVAL", Value: "15s"})
		case "rebuild-graph", "rebuild-search":
			component.After = []string{"worker"}
		}
		library.Components = append(library.Components, component)
	}
	library.Outputs = libraryOutputs(library.Components)
	return library
}

// fabriqLibraryOutputs are what an app that embeds Fabriq gets, by output
// name and the variable the worker reads it from.
var fabriqLibraryOutputs = []struct{ name, env string }{
	{"url", "FABRIQ_POSTGRES_DSN"},
	{"redis-addr", "FABRIQ_REDIS_ADDR"},
	{"redis-username", "FABRIQ_REDIS_USERNAME"},
	{"redis-password", "FABRIQ_REDIS_PASSWORD"},
	{"falkordb-addr", "FABRIQ_FALKORDB_ADDR"},
	{"falkordb-username", "FABRIQ_FALKORDB_USERNAME"},
	{"falkordb-password", "FABRIQ_FALKORDB_PASSWORD"},
	{"elasticsearch-addrs", "FABRIQ_ELASTICSEARCH_ADDRS"},
	{"elasticsearch-username", "FABRIQ_ELASTICSEARCH_USERNAME"},
	{"elasticsearch-password", "FABRIQ_ELASTICSEARCH_PASSWORD"},
}

// libraryOutputs builds the library's outputs from the worker's own
// environment, which is the hosted server's, so the app reaches Postgres,
// Redis and the projections exactly as the stock worker does. Every one is
// secret: each carries a credential or says where to use one.
func libraryOutputs(components []Component) []Output {
	var env []EnvVar
	for _, c := range components {
		if c.Name == "worker" {
			env = c.Env
		}
	}
	outputs := make([]Output, 0, len(fabriqLibraryOutputs))
	for _, want := range fabriqLibraryOutputs {
		i := slices.IndexFunc(env, func(e EnvVar) bool { return e.Name == want.env })
		if i < 0 {
			panic("recipes: the Fabriq worker doesn't set " + want.env)
		}
		outputs = append(outputs, outputFrom(want.name, env[i]))
	}
	return outputs
}

// outputFrom is an output that reads what e does: the same reference,
// template, generated secret or literal, with the same requirement.
func outputFrom(name string, e EnvVar) Output {
	o := Output{Name: name, EnvKey: e.Name, Secret: true, Template: e.Template, Requires: e.Requires}
	if e.From != nil {
		ref := *e.From
		o.From = &ref
	}
	switch {
	case e.Secret != "":
		o.Template = "${secret:" + e.Secret + "}"
	case e.Value != "":
		if strings.Contains(e.Value, "${") {
			panic("recipes: " + e.Name + "'s literal reads as a template")
		}
		o.Template = e.Value
	}
	return o
}
