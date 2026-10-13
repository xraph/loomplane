// Package discovery is the contract for Loomplane's repository analysis:
// what a detector reads, and what it reports.
//
// Detectors only read. They get a checkout as a file system and never run
// anything in it: no build scripts, no package managers, no code. Every
// finding carries the file and line it came from, and a confidence, so a
// person can check it and nothing weakly inferred gets acted on unconfirmed.
package discovery

import (
	"context"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Ecosystem is the language toolchain a unit belongs to.
type Ecosystem string

// The ecosystems Loomplane's detectors understand.
const (
	Go        Ecosystem = "go"
	Rust      Ecosystem = "rust"
	Node      Ecosystem = "node"
	Python    Ecosystem = "python"
	JVM       Ecosystem = "jvm"
	Container Ecosystem = "container"
)

// Confidence is how sure a detector is.
type Confidence string

const (
	// Certain findings were read straight from a manifest: a go.work use
	// directive, a Cargo workspace member, a package.json workspace.
	Certain Confidence = "certain"
	// Likely findings rest on strong signals, such as a Go main package that
	// imports an HTTP framework, or a Next.js dependency.
	Likely Confidence = "likely"
	// Possible findings rest on weak signals, such as a Postgres driver in a
	// dependency list. Nothing possible is acted on without confirmation.
	Possible Confidence = "possible"
)

// Rank orders confidences, Certain highest.
func (c Confidence) Rank() int {
	switch c {
	case Certain:
		return 3
	case Likely:
		return 2
	case Possible:
		return 1
	}
	return 0
}

// Evidence points at what a finding was read from.
type Evidence struct {
	// Path is slash-separated and relative to the repository root.
	Path string `json:"path"`
	// Line is 1-based. Zero means the file as a whole.
	Line int `json:"line,omitempty"`
	// Detail is what was read there, such as `use ./services/api`.
	Detail string `json:"detail"`
	// Source is the alias of the repository Path is in. It's empty in one
	// repository's analysis, and set when graphs from several are joined.
	Source string `json:"source,omitempty"`
}

// FrameworkKind says what a framework is for.
type FrameworkKind string

// Framework kinds.
const (
	// HTTP frameworks serve requests: net/http servers, axum, Express.
	HTTP FrameworkKind = "http"
	// GRPC frameworks serve gRPC.
	GRPC FrameworkKind = "grpc"
	// Web frameworks build a browser application, server-rendered or not.
	Web FrameworkKind = "web"
	// Static frameworks build files a CDN can serve as they are.
	Static FrameworkKind = "static"
	// CLI frameworks build command-line tools.
	CLI FrameworkKind = "cli"
	// Worker frameworks run background jobs or long-running consumers.
	Worker FrameworkKind = "worker"
	// Native frameworks build mobile or desktop apps, which Loomplane
	// doesn't deploy.
	Native FrameworkKind = "native"
)

// Framework is a framework a unit or entrypoint uses.
type Framework struct {
	Name       string        `json:"name"`
	Kind       FrameworkKind `json:"kind"`
	Confidence Confidence    `json:"confidence"`
	Evidence   Evidence      `json:"evidence"`
}

// ResourceUse is a backing service a unit or entrypoint appears to need,
// such as Postgres.
type ResourceUse struct {
	Kind       string     `json:"kind"`
	Confidence Confidence `json:"confidence"`
	Evidence   Evidence   `json:"evidence"`
}

// Entrypoint is one executable a unit builds: a Go main package, a Rust
// binary target, a Node package's start script.
type Entrypoint struct {
	Name string `json:"name"`
	// Path is the main package's directory, the binary's source file, or
	// the Node package's directory.
	Path       string        `json:"path"`
	Frameworks []Framework   `json:"frameworks,omitempty"`
	Resources  []ResourceUse `json:"resources,omitempty"`
	// Commands are the entrypoint's own, when they aren't its unit's: a
	// Procfile process or a task queue's worker beside a web app.
	Commands *Commands  `json:"commands,omitempty"`
	Evidence []Evidence `json:"evidence"`
}

// Commands are how a unit is built and run, as its manifest declares them.
type Commands struct {
	Build string `json:"build,omitempty"`
	Start string `json:"start,omitempty"`
	Dev   string `json:"dev,omitempty"`
}

// Unit is one buildable thing: a Go module, a Rust crate, a Node package.
type Unit struct {
	// ID is "<ecosystem>:<path>", stable for a given revision.
	ID        string    `json:"id"`
	Ecosystem Ecosystem `json:"ecosystem"`
	// Name is the module path, crate name or package name.
	Name string `json:"name"`
	// Path is the unit's directory, "." for the repository root.
	Path string `json:"path"`
	// Runtime is the toolchain the manifest asks for, such as "go 1.26".
	Runtime     string        `json:"runtime,omitempty"`
	Frameworks  []Framework   `json:"frameworks,omitempty"`
	Resources   []ResourceUse `json:"resources,omitempty"`
	Entrypoints []Entrypoint  `json:"entrypoints,omitempty"`
	Commands    Commands      `json:"commands"`
	// DependsOn are other units in this repository this one uses.
	DependsOn []Dependency `json:"depends_on,omitempty"`
	// Requires are the dependencies it declares that nothing in this
	// checkout provides. Another repository of the same project might.
	Requires []Requirement `json:"requires,omitempty"`
	Evidence []Evidence    `json:"evidence"`
}

// Dependency is a unit's use of another unit in the same checkout.
type Dependency struct {
	Unit     string   `json:"unit"`
	Evidence Evidence `json:"evidence"`
}

// Requirement is a dependency a unit declares on something outside the
// checkout: a module, crate or package, by the name it's published under.
type Requirement struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Git is the repository URL a Rust git dependency names.
	Git      string   `json:"git,omitempty"`
	Evidence Evidence `json:"evidence"`
}

// Workspace is a set of units managed together.
type Workspace struct {
	Ecosystem Ecosystem `json:"ecosystem"`
	// Tool is go-work, cargo, pnpm, npm, yarn or bun.
	Tool    string   `json:"tool"`
	Path    string   `json:"path"`
	Members []string `json:"members"`
	// Source is the alias of the repository it was found in, set when graphs are joined.
	Source   string     `json:"source,omitempty"`
	Evidence []Evidence `json:"evidence"`
}

// BuildSystem is a monorepo build tool, such as Turborepo or Nx.
type BuildSystem struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Source is the alias of the repository it was found in, set when graphs are joined.
	Source   string   `json:"source,omitempty"`
	Evidence Evidence `json:"evidence"`
}

// Image is a container image the repository can build: a Dockerfile.
type Image struct {
	// Path is the Dockerfile.
	Path string `json:"path"`
	// Context is the build context, the Dockerfile's directory unless a
	// Compose file says otherwise.
	Context string `json:"context"`
	Base    string `json:"base,omitempty"`
	Ports   []int  `json:"ports,omitempty"`
	// Builds are the RUN instructions that build something, which say
	// which of several binaries the image is for.
	Builds   []Evidence `json:"builds,omitempty"`
	Evidence []Evidence `json:"evidence"`
}

// Note is something the analysis couldn't read, or chose not to.
type Note struct {
	Path string `json:"path,omitempty"`
	// Source is the alias of the repository it was found in, set when graphs are joined.
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
}

// Report is everything the detectors found.
type Report struct {
	Workspaces   []Workspace   `json:"workspaces"`
	Units        []Unit        `json:"units"`
	Images       []Image       `json:"images"`
	Resources    []ResourceUse `json:"resources"`
	BuildSystems []BuildSystem `json:"build_systems"`
	Notes        []Note        `json:"notes"`
}

// Repo is a read-only checkout.
type Repo struct {
	FS fs.FS
	// Files is every file the checkout holds, slash-separated and sorted.
	// Dependency and build output directories (node_modules, vendor,
	// target, .git) are left out.
	Files []string
}

// Named returns the files whose base name is one of names.
func (r Repo) Named(names ...string) []string {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var out []string
	for _, f := range r.Files {
		if want[path.Base(f)] {
			out = append(out, f)
		}
	}
	return out
}

// Has reports whether the checkout holds the file.
func (r Repo) Has(name string) bool {
	i := sort.SearchStrings(r.Files, name)
	return i < len(r.Files) && r.Files[i] == name
}

// Detector finds one kind of thing in a checkout and adds it to a report.
// It must only read, and must be deterministic: the same checkout always
// gives the same findings, in the same order.
type Detector interface {
	Name() string
	Detect(ctx context.Context, repo Repo, report *Report) error
}

// UnitID is the ID of the unit of ecosystem e at dir.
func UnitID(e Ecosystem, dir string) string { return string(e) + ":" + dir }

// skippedDirs are directory names whose contents are dependencies or build
// output. Detectors never look inside them, and the checkout never writes
// them.
var skippedDirs = map[string]bool{
	"node_modules":      true,
	"bower_components":  true,
	"vendor":            true,
	"target":            true,
	"dist":              true,
	"coverage":          true,
	"__pycache__":       true,
	"jspm_packages":     true,
	"site-packages":     true,
	".pnpm-store":       true,
	"elm-stuff":         true,
	"Pods":              true,
	"DerivedData":       true,
	"bazel-out":         true,
	"bazel-bin":         true,
	"bazel-testlogs":    true,
	"cmake-build-debug": true,
}

// SkipDir reports whether a directory, by name, holds nothing a detector
// reads: dependencies, build output, or anything hidden (.git, .next).
func SkipDir(name string) bool {
	return skippedDirs[name] || (strings.HasPrefix(name, ".") && name != "." && name != "..")
}
