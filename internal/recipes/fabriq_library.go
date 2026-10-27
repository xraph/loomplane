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
	library.Outputs = []Output{
		{Name: "url", EnvKey: "FABRIQ_POSTGRES_DSN", Secret: true, Template: "postgres://fabriq_app:${secret:app-password}@${dep:postgres:hostport}/${dep:postgres:database}?${dep:postgres:query}"},
		{Name: "redis-addr", EnvKey: "FABRIQ_REDIS_ADDR", Secret: true, From: &Ref{Dependency: "redis", Field: FieldHostPort}},
		{Name: "redis-username", EnvKey: "FABRIQ_REDIS_USERNAME", Secret: true, From: &Ref{Dependency: "redis", Field: FieldUsername}},
		{Name: "redis-password", EnvKey: "FABRIQ_REDIS_PASSWORD", Secret: true, From: &Ref{Dependency: "redis", Field: FieldPassword}},
		{Name: "falkordb-addr", EnvKey: "FABRIQ_FALKORDB_ADDR", Secret: true, Requires: "falkordb", From: &Ref{Dependency: "falkordb", Field: FieldURL}},
		{Name: "falkordb-username", EnvKey: "FABRIQ_FALKORDB_USERNAME", Secret: true, Requires: "falkordb", Template: "default"},
		{Name: "falkordb-password", EnvKey: "FABRIQ_FALKORDB_PASSWORD", Secret: true, Requires: "falkordb", From: &Ref{Dependency: "falkordb", Field: "secret:password"}},
		{Name: "elasticsearch-addrs", EnvKey: "FABRIQ_ELASTICSEARCH_ADDRS", Secret: true, Requires: "elasticsearch", From: &Ref{Dependency: "elasticsearch", Field: FieldURL}},
		{Name: "elasticsearch-username", EnvKey: "FABRIQ_ELASTICSEARCH_USERNAME", Secret: true, Requires: "elasticsearch", Template: "elastic"},
		{Name: "elasticsearch-password", EnvKey: "FABRIQ_ELASTICSEARCH_PASSWORD", Secret: true, Requires: "elasticsearch", From: &Ref{Dependency: "elasticsearch", Field: "secret:password"}},
	}
	return library
}
