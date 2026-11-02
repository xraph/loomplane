package recipes_test

import (
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

func reusableService() recipes.Kind {
	k := recipestest.Graph()
	k.Placement = recipes.PlacementServiceProject
	k.Service = &recipes.ServiceDefinition{Connection: map[string]string{recipes.FieldURL: "url", recipes.FieldPassword: "password"}, Recovery: "graph", BackupFormat: "graph-rdb-v1"}
	k.Outputs = append(k.Outputs, recipes.Output{Name: "password", EnvKey: "GRAPH_PASSWORD", Secret: true, Template: "${secret:password}"})
	return k
}

func TestReusableServiceKindValidation(t *testing.T) {
	k := reusableService()
	r := recipes.NewRegistry()
	if err := r.Register(k); err != nil {
		t.Fatal(err)
	}
	k.Service.Connection[recipes.FieldPassword] = "changed"
	got, _ := r.Kind(k.Name)
	if !got.DirectService() || got.Service.Connection[recipes.FieldPassword] != "password" {
		t.Fatalf("service metadata: %+v", got.Service)
	}
	got.Service.Connection[recipes.FieldPassword] = "changed-again"
	got, _ = r.Kind(k.Name)
	if got.Service.Connection[recipes.FieldPassword] != "password" {
		t.Fatal("returned connection map aliases the registry")
	}
	if !recipestest.Graph().DirectService() || recipestest.Sample().DirectService() {
		t.Fatal("legacy direct-service classification changed")
	}
	unprotected := reusableService()
	unprotected.Service.Recovery = ""
	unprotected.Service.BackupFormat = ""
	if err := recipes.NewRegistry().Register(unprotected); err != nil {
		t.Fatalf("service without recovery: %v", err)
	}
	for _, tc := range []struct {
		name, want string
		edit       func(*recipes.Kind)
	}{
		{"two processes", "exactly one process", func(k *recipes.Kind) { k.Components = append(k.Components, k.Components[0]) }},
		{"job", "exactly one process", func(k *recipes.Kind) { k.Components[0].Role = recipes.RoleJob }},
		{"dependencies", "no dependencies", func(k *recipes.Kind) {
			k.Dependencies = []recipes.Dependency{{Name: "db", Title: "DB", Kinds: []string{"postgres"}}}
		}},
		{"unknown output", "connection", func(k *recipes.Kind) { k.Service.Connection[recipes.FieldPassword] = "missing" }},
		{"unknown field", "connection", func(k *recipes.Kind) { k.Service.Connection["invented"] = "url" }},
		{"missing url", "connection", func(k *recipes.Kind) { delete(k.Service.Connection, recipes.FieldURL) }},
		{"empty connection", "connection", func(k *recipes.Kind) { k.Service.Connection = nil }},
		{"missing recovery", "recovery", func(k *recipes.Kind) { k.Service.Recovery = "" }},
		{"missing backup format", "backup", func(k *recipes.Kind) { k.Service.BackupFormat = "" }},
		{"per consumer", "per consumer", func(k *recipes.Kind) { k.Outputs[1].Template = ""; k.Outputs[1].PerConsumer = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := reusableService()
			tc.edit(&k)
			err := recipes.NewRegistry().Register(k)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

func TestReusableServiceDependencyFields(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		service := reusableService()
		consumer := recipestest.Sample()
		consumer.Dependencies = append(consumer.Dependencies, recipes.Dependency{Name: "graph", Title: "Graph", Kinds: []string{"graph"}})
		consumer.Components[1].Env = append(consumer.Components[1].Env, recipes.EnvVar{Name: "GRAPH_PASSWORD", From: &recipes.Ref{Dependency: "graph", Field: recipes.FieldPassword}}, recipes.EnvVar{Name: "LEGACY_PASSWORD", From: &recipes.Ref{Dependency: "graph", Field: "secret:password"}})
		list := []recipes.Kind{service, consumer}
		if reverse {
			list[0], list[1] = list[1], list[0]
		}
		r := recipes.NewRegistry()
		for _, k := range list {
			if err := r.Register(k); err != nil {
				t.Fatalf("reverse %t: %v", reverse, err)
			}
		}
	}
}

func TestReusableServiceAcceptsCompatibleAlternativesAndExternal(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		first, second := reusableService(), reusableService()
		second.Name = "othergraph"
		consumer := recipestest.Sample()
		consumer.Dependencies = append(consumer.Dependencies, recipes.Dependency{Name: "graph", Title: "Graph", Kinds: []string{"graph", "othergraph"}, External: &recipes.Requirement{Engine: "redis"}})
		consumer.Components[1].Env = append(consumer.Components[1].Env, recipes.EnvVar{Name: "GRAPH_USER", From: &recipes.Ref{Dependency: "graph", Field: recipes.FieldUsername}})
		list := []recipes.Kind{first, second, consumer}
		if reverse {
			list[0], list[2] = list[2], list[0]
		}
		r := recipes.NewRegistry()
		for _, k := range list {
			if err := r.Register(k); err != nil {
				t.Fatalf("reverse %t: %v", reverse, err)
			}
		}
	}
}

func TestReusableServiceValidatesEachAlternative(t *testing.T) {
	first, second := reusableService(), reusableService()
	second.Name = "othergraph"
	second.Secrets = nil
	second.Components[0].Env = nil
	second.Outputs = second.Outputs[:1]
	second.Outputs[0].Template = "redis://${component:server:address:resp}"
	delete(second.Service.Connection, recipes.FieldPassword)
	consumer := recipestest.Sample()
	consumer.Dependencies = append(consumer.Dependencies, recipes.Dependency{Name: "graph", Title: "Graph", Kinds: []string{"graph", "othergraph"}})
	consumer.Components[1].Env = append(consumer.Components[1].Env, recipes.EnvVar{Name: "GRAPH_PRIVATE", From: &recipes.Ref{Dependency: "graph", Field: "secret:password"}})
	r := recipes.NewRegistry()
	for _, k := range []recipes.Kind{first, second} {
		if err := r.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Register(consumer); err == nil || !strings.Contains(err.Error(), "othergraph") {
		t.Fatalf("missing private field in alternative: %v", err)
	}
}

func TestReusableServiceHiddenMetadata(t *testing.T) {
	hidden := reusableService()
	hidden.Placement = recipes.PlacementDependency
	hidden.Settings = []recipes.Setting{{Name: "database", Title: "Database", Pattern: "[a-z]+", Default: "graph"}}
	consumer := recipestest.Sample()
	consumer.Dependencies = append(consumer.Dependencies, recipes.Dependency{Name: "graph", Title: "Graph", Kinds: []string{"graph"}, External: &recipes.Requirement{Engine: "redis"}})
	consumer.Components[1].Env = append(consumer.Components[1].Env, recipes.EnvVar{Name: "GRAPH_PASSWORD", From: &recipes.Ref{Dependency: "graph", Field: recipes.FieldPassword}})
	r := recipes.NewRegistry()
	for _, k := range []recipes.Kind{hidden, consumer} {
		if err := r.Register(k); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := r.Kind("graph")
	if got.Placement != recipes.PlacementDependency || !got.DirectService() {
		t.Fatal("normalized capability changed hidden placement")
	}
}

func TestReusableServiceExternalRejectsPrivateReads(t *testing.T) {
	for _, field := range []string{"output:password", "secret:password"} {
		for _, reverse := range []bool{false, true} {
			t.Run(field+"/"+map[bool]string{false: "service-first", true: "consumer-first"}[reverse], func(t *testing.T) {
				service := reusableService()
				service.Outputs[1].Template = "must_stay_private"
				consumer := recipestest.Sample()
				consumer.Dependencies = append(consumer.Dependencies, recipes.Dependency{Name: "graph", Title: "Graph", Kinds: []string{"graph"}, External: &recipes.Requirement{Engine: "redis"}})
				consumer.Components[1].Env = append(consumer.Components[1].Env, recipes.EnvVar{Name: "GRAPH_PRIVATE", From: &recipes.Ref{Dependency: "graph", Field: field}})
				list := []recipes.Kind{service, consumer}
				if reverse {
					list[0], list[1] = list[1], list[0]
				}
				r := recipes.NewRegistry()
				var err error
				for _, k := range list {
					if err = r.Register(k); err != nil {
						break
					}
				}
				if err == nil || !strings.Contains(err.Error(), "external") || strings.Contains(err.Error(), "must_stay_private") {
					t.Fatalf("incompatible external private read: %v", err)
				}
			})
		}
	}
}
