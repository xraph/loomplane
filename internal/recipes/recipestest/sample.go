// Package recipestest holds a composed kind that exercises every part of a
// recipes.Kind, for tests in the packages that plan and run compositions.
package recipestest

import (
	"testing"

	"github.com/xraph/loomplane/internal/recipes"
)

// Sample is kind "sample": a migration job, an API with a public and a
// control port, and a worker that starts after the API. It needs Postgres
// (new, connected or external) and may use a Redis or Valkey cache.
func Sample() recipes.Kind {
	api := recipes.Image{Repository: "registry.example/sample-api", Digests: map[string]string{
		"amd64": "sha256:cb2ffce7d6a5fca5f8615339cc171c74dfe47017c634894dd43e33e10cb4cbba",
		"arm64": "sha256:01873eef9d70a2cf2ee33aab65b3496fcfc9674a5707b42b12fe19e7ae0c5ce7",
	}}
	migrate := recipes.Image{Repository: "registry.example/sample-migrate", Digests: map[string]string{
		"amd64": "sha256:41471f155b32f2187ba0b9703db6d4da171af3a003e51f2a9b9cbc41c2c46bc8",
		"arm64": "sha256:6b1f63a59ef133cbd801d04ddcd873d29b9407cd6c756982d2074459fe32f0f5",
	}}
	database := recipes.EnvVar{Name: "DATABASE_URL", From: &recipes.Ref{Dependency: "postgres", Field: recipes.FieldURL}}
	return recipes.Kind{
		Name:        "sample",
		Title:       "Sample",
		Description: "A test kind with a migration job, an API and a worker.",
		Placement:   recipes.PlacementAppProject,
		Targets:     []string{"server", "kubernetes"},
		Releases: []recipes.Release{{
			Version: "1", Product: "1.0.0", Revision: "1",
			Images: map[string]recipes.Image{"migrate": migrate, "api": api, "worker": api},
		}},
		Dependencies: []recipes.Dependency{
			{Name: "postgres", Title: "PostgreSQL", Kinds: []string{"postgres"}, External: &recipes.Requirement{Engine: "postgres", MinVersion: "16"}},
			{Name: "cache", Title: "Cache", Kinds: []string{"redis", "valkey"}, Optional: true},
		},
		Secrets: []recipes.GeneratedSecret{{Name: "admin-password"}},
		Components: []recipes.Component{
			{
				Name:    "migrate",
				Role:    recipes.RoleJob,
				Command: []string{"sample", "migrate"},
				Env:     []recipes.EnvVar{database},
			},
			{
				Name:    "api",
				Role:    recipes.RoleProcess,
				Command: []string{"sample", "serve"},
				Ports: []recipes.Port{
					{Name: "http", Container: 8080, Protocol: "http", Routable: true},
					{Name: "admin", Container: 9090, Protocol: "http", Control: true},
				},
				Env: []recipes.EnvVar{
					database,
					{Name: "REDIS_URL", From: &recipes.Ref{Dependency: "cache", Field: recipes.FieldURL}, Requires: "cache"},
					{Name: "ADMIN_PASSWORD", Secret: "admin-password"},
				},
				Readiness: &recipes.HTTPProbe{Port: "http", Path: "/readyz"},
				After:     []string{"migrate"},
			},
			{
				Name:      "worker",
				Role:      recipes.RoleProcess,
				Command:   []string{"sample", "work"},
				Ports:     []recipes.Port{{Name: "health", Container: 8081, Protocol: "http"}},
				Env:       []recipes.EnvVar{{Name: "API_URL", From: &recipes.Ref{Component: "api", Field: "url:http"}}},
				Readiness: &recipes.HTTPProbe{Port: "health", Path: "/healthz"},
				After:     []string{"api"},
			},
		},
		Outputs: []recipes.Output{
			{Name: "url", EnvKey: "SAMPLE_URL", From: &recipes.Ref{Component: "api", Field: "url:http"}},
			{Name: "admin", EnvKey: "SAMPLE_ADMIN_URL", From: &recipes.Ref{Component: "api", Field: "url:admin"}},
		},
	}
}

// Graph is kind "graph", a dependency-only kind like a graph database: one
// process speaking RESP with a volume and a generated password, whose
// primary output is a redis URL built from a template.
func Graph() recipes.Kind {
	server := recipes.Image{Repository: "registry.example/graph", Digests: map[string]string{
		"amd64": "sha256:3f1c6a1d0b9e0f3c7d2a8e4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d",
		"arm64": "sha256:4a2d7b2e1c0f1a4d8e3b9f5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e",
	}}
	return recipes.Kind{
		Name:      "graph",
		Title:     "Graph",
		Placement: recipes.PlacementDependency,
		Targets:   []string{"server", "kubernetes"},
		Releases: []recipes.Release{{
			Version: "1", Product: "1.0.0", Revision: "1",
			Images: map[string]recipes.Image{"server": server},
		}},
		Secrets: []recipes.GeneratedSecret{{Name: "password", Prefix: "graph_"}},
		Components: []recipes.Component{{
			Name:      "server",
			Role:      recipes.RoleProcess,
			Ports:     []recipes.Port{{Name: "resp", Container: 6379, Protocol: "redis"}},
			Env:       []recipes.EnvVar{{Name: "GRAPH_ARGS", Template: "--requirepass ${secret:password}"}},
			Readiness: &recipes.HTTPProbe{Port: "resp"},
			Volume:    &recipes.Volume{MountPath: "/data", KubernetesSizeMB: 1024, FSGroup: 1000},
		}},
		Outputs: []recipes.Output{
			{Name: "url", EnvKey: "GRAPH_URL", Secret: true, Template: "redis://:${secret:password}@${component:server:address:resp}"},
		},
	}
}

// GPUSample is kind "gpusample": a process that needs a GPU, shared memory
// and a weight cache, with an amd64-only image, beside a CPU sidecar.
func GPUSample() recipes.Kind {
	server := recipes.Image{Repository: "registry.example/gpusample", Digests: map[string]string{
		"amd64": "sha256:5b3e8c3f2d1a2b5e9f4c0a6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f",
	}}
	sidecar := recipes.Image{Repository: "registry.example/gpusample-sidecar", Digests: map[string]string{
		"amd64": "sha256:6c4f9d4a3e2b3c6f0a5d1b7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f70",
		"arm64": "sha256:7d5a0e5b4f3c4d7a1b6e2c8f9a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7081",
	}}
	return recipes.Kind{
		Name:        "gpusample",
		Title:       "GPU sample",
		Description: "A test kind with a GPU process and a CPU sidecar.",
		Placement:   recipes.PlacementAppProject,
		Targets:     []string{"server", "kubernetes"},
		Releases: []recipes.Release{{
			Version: "1", Product: "1.0.0", Revision: "1",
			Images: map[string]recipes.Image{"server": server, "sidecar": sidecar},
		}},
		Components: []recipes.Component{
			{
				Name:      "server",
				Role:      recipes.RoleProcess,
				Ports:     []recipes.Port{{Name: "http", Container: 8000, Protocol: "http", Routable: true}},
				Readiness: &recipes.HTTPProbe{Port: "http", Path: "/health"},
				Resources: recipes.Resources{
					CPUMillis: 2000, MemoryMB: 8192,
					GPU: &recipes.GPU{Count: 1, MinMemoryGiB: 20}, ShmMB: 2048,
				},
				WeightCache: &recipes.WeightCache{MountPath: "/cache", KubernetesSizeMB: 20480},
			},
			{
				Name:      "sidecar",
				Role:      recipes.RoleProcess,
				Ports:     []recipes.Port{{Name: "health", Container: 8081, Protocol: "http"}},
				Readiness: &recipes.HTTPProbe{Port: "health", Path: "/healthz"},
			},
		},
		Outputs: []recipes.Output{
			{Name: "url", EnvKey: "GPUSAMPLE_URL", From: &recipes.Ref{Component: "server", Field: "url:http"}},
		},
	}
}

// Featured is kind "featured": Sample under another name, belonging to
// the server feature recipes.FeatureAI, for tests of a feature that's off.
func Featured() recipes.Kind {
	k := Sample()
	k.Name, k.Title, k.Feature = "featured", "Featured", recipes.FeatureAI
	k.Outputs[0].EnvKey, k.Outputs[1].EnvKey = "FEATURED_URL", "FEATURED_ADMIN_URL"
	return k
}

// Registry is a fresh registry with the built-in engines, Sample and GPUSample.
func Registry(t testing.TB) *recipes.Registry {
	t.Helper()
	r := recipes.NewRegistry()
	for _, k := range []recipes.Kind{Sample(), GPUSample()} {
		if err := r.Register(k); err != nil {
			t.Fatalf("register %s kind: %v", k.Name, err)
		}
	}
	return r
}
