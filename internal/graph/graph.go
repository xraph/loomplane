// Package graph is Loomplane's model of an application: the services a
// repository builds, the packages they share, the backing services they
// need, and how they connect. It knows nothing about clouds. Planning
// (milestone 3) maps it onto providers.
package graph

import (
	"github.com/xraph/loomplane/pkg/discovery"
)

// SchemaVersion is the version of the graph's JSON shape. Stored graphs
// carry it, so a later Loomplane can read an older one.
const SchemaVersion = 1

// ServiceKind is what a service is, as far as deploying it goes.
type ServiceKind string

const (
	// HTTPService serves HTTP or gRPC: a Go, Rust or Node server.
	HTTPService ServiceKind = "http-service"
	// WebApp is a server-rendered web app: Next.js, Remix, SvelteKit.
	WebApp ServiceKind = "web-app"
	// StaticSite builds files a CDN serves as they are.
	StaticSite ServiceKind = "static-site"
	// Worker is a long-running process with no server Loomplane could see.
	Worker ServiceKind = "worker"
	// Tool is a command-line tool. Loomplane doesn't deploy it.
	Tool ServiceKind = "tool"
	// NativeApp is a mobile or desktop app. Loomplane doesn't deploy it.
	NativeApp ServiceKind = "native-app"
)

// Deployable reports whether Loomplane would ever deploy this kind.
func (k ServiceKind) Deployable() bool {
	switch k {
	case HTTPService, WebApp, StaticSite, Worker:
		return true
	case Tool, NativeApp:
		return false
	}
	return false
}

// Graph is one version of an application's architecture.
type Graph struct {
	Version      int                     `json:"version"`
	Services     []Service               `json:"services"`
	Packages     []Package               `json:"packages"`
	Resources    []Resource              `json:"resources"`
	Edges        []Edge                  `json:"edges"`
	Workspaces   []discovery.Workspace   `json:"workspaces"`
	BuildSystems []discovery.BuildSystem `json:"build_systems"`
	Notes        []discovery.Note        `json:"notes"`
}

// Service is something that runs: a server, a web app, a worker.
type Service struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Kind       ServiceKind         `json:"kind"`
	Deployable bool                `json:"deployable"`
	Ecosystem  discovery.Ecosystem `json:"ecosystem"`
	// Unit is the module, crate or package it's built from. Empty for a
	// service known only from its Dockerfile.
	Unit string `json:"unit,omitempty"`
	// Path is the source directory.
	Path string `json:"path"`
	// Entrypoint is the main package, binary source or package directory.
	Entrypoint string `json:"entrypoint"`
	Framework  string `json:"framework,omitempty"`
	Runtime    string `json:"runtime,omitempty"`
	// Image is the Dockerfile that builds it, if there is one.
	Image      string               `json:"image,omitempty"`
	Ports      []int                `json:"ports,omitempty"`
	Commands   discovery.Commands   `json:"commands"`
	Confidence discovery.Confidence `json:"confidence"`
	Evidence   []discovery.Evidence `json:"evidence"`
}

// Package is shared code: a library module, crate or package.
type Package struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Ecosystem discovery.Ecosystem  `json:"ecosystem"`
	Path      string               `json:"path"`
	Runtime   string               `json:"runtime,omitempty"`
	Evidence  []discovery.Evidence `json:"evidence"`
}

// Resource is a backing service the application appears to need, such as
// Postgres. Loomplane never provisions one it isn't sure of without asking.
type Resource struct {
	ID         string               `json:"id"`
	Kind       string               `json:"kind"`
	Confidence discovery.Confidence `json:"confidence"`
	// NeedsConfirmation is true when the only evidence is weak, such as a
	// driver in a dependency list.
	NeedsConfirmation bool                 `json:"needs_confirmation"`
	Evidence          []discovery.Evidence `json:"evidence"`
}

// EdgeKind is how two nodes relate.
type EdgeKind string

const (
	// Uses joins a service or package to a package it builds with.
	Uses EdgeKind = "uses"
	// Needs joins a service or package to a resource it talks to.
	Needs EdgeKind = "needs"
)

// Edge is a directed relationship between two nodes, by ID.
type Edge struct {
	From       string               `json:"from"`
	To         string               `json:"to"`
	Kind       EdgeKind             `json:"kind"`
	Confidence discovery.Confidence `json:"confidence"`
	Evidence   []discovery.Evidence `json:"evidence"`
}

// Summary counts what a graph holds.
type Summary struct {
	Services          int `json:"services"`
	Deployable        int `json:"deployable"`
	Packages          int `json:"packages"`
	Resources         int `json:"resources"`
	NeedsConfirmation int `json:"needs_confirmation"`
}

// Summarize counts g.
func (g *Graph) Summarize() Summary {
	s := Summary{Services: len(g.Services), Packages: len(g.Packages), Resources: len(g.Resources)}
	for _, svc := range g.Services {
		if svc.Deployable {
			s.Deployable++
		}
	}
	for _, r := range g.Resources {
		if r.NeedsConfirmation {
			s.NeedsConfirmation++
		}
	}
	return s
}
