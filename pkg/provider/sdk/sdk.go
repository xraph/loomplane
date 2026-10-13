// Package sdk is how you add a deployment target to Loomplane without
// changing Loomplane: write a Provider, serve it with Handler, and point
// Loomplane at it in config.yaml.
//
//	type fly struct{}
//
//	func (fly) Describe() provider.Provider { ... }
//	func (fly) Check(ctx context.Context, c sdk.Credential) (*sdk.Account, error) { ... }
//	func (fly) Apply(ctx context.Context, job *sdk.Job) (*sdk.Outcome, error) { ... }
//	func (fly) Remove(ctx context.Context, job *sdk.Job) error { ... }
//	func (fly) Observe(ctx context.Context, t *sdk.Target) (*sdk.Observation, error) { ... }
//
//	http.ListenAndServe(":9000", sdk.Handler(fly{}, os.Getenv("PROVIDER_TOKEN")))
//
// and in Loomplane's config:
//
//	providers:
//	  plugins:
//	    - url: http://fly-provider:9000
//	      token: ${FLY_PROVIDER_TOKEN}
//
// A provider runs as its own process, on your infrastructure, in any
// language that speaks the protocol (protocol.go); this package is the Go
// side of it. Loomplane reads its description when it starts, puts it in
// the catalog next to the built-in targets, and from then on plans and
// deployments treat it like any other. The provider's cloud is a kind of
// connection: organizations connect an account to it under Integrations,
// Check decides whether the credential works, and Loomplane hands the
// credential to Apply, Remove and Observe, never storing it anywhere but
// Vault.
//
// Apply, Remove and Observe must be idempotent. Loomplane retries a step
// that failed or was interrupted, and a provider that restarted loses its
// operations, so the same job can arrive again; it should converge on the
// same result, not create a second one. Job.Resume carries what an earlier
// attempt checkpointed.
package sdk

import (
	"context"
	"errors"
	"io"

	"github.com/xraph/loomplane/pkg/provider"
)

// Provider is a deployment target.
type Provider interface {
	// Describe is the provider's catalog entry: what it runs and
	// provisions, and the cloud its connections are for. Its Name is how
	// manifests name it.
	Describe() provider.Provider
	// Check decides whether a credential works, and what it can do.
	// Return an error wrapping ErrRejected when the cloud refused it;
	// any other error means the cloud couldn't be asked.
	Check(ctx context.Context, c Credential) (*Account, error)
	// Apply makes a service or resource exist as the job describes, and
	// returns what it made.
	Apply(ctx context.Context, job *Job) (*Outcome, error)
	// Remove deletes what a binding names. Something already gone isn't
	// an error.
	Remove(ctx context.Context, job *Job) error
	// Observe reads what's live for a binding, without changing anything.
	Observe(ctx context.Context, t *Target) (*Observation, error)
}

// ErrRejected marks a credential the cloud refused.
var ErrRejected = errors.New("the cloud refused the credential")

// Credential is an organization's connection to the provider's cloud: the
// secret it pasted, and the settings it chose.
type Credential struct {
	Secret   []byte            `json:"secret"`
	Settings map[string]string `json:"settings,omitempty"`
}

// Account is what a credential acts as.
type Account struct {
	// Account names it: a project, a team.
	Account string `json:"account"`
	// Details are facts safe to show, never the secret.
	Details map[string]string `json:"details,omitempty"`
	// Missing lists what a deployment needs that the credential may not
	// do. A credential with something missing is kept as incomplete.
	Missing []string `json:"missing,omitempty"`
}

// Job is one action of an approved plan: a service to run or a resource
// to provision, or one to remove.
type Job struct {
	// Key is the same for every attempt at the same action in the same
	// deployment, so a provider can tell a retry from new work.
	Key         string `json:"key"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	// Name is the service's or resource's name in the manifest.
	Name   string `json:"name"`
	Region string `json:"region,omitempty"`
	// Commit is the commit the plan pins for the service's own
	// repository, the one Source holds. In a project of several
	// repositories each service has its own. It's empty for a resource
	// or a gateway.
	Commit     string     `json:"commit"`
	Credential Credential `json:"credential"`
	// Account is the connection's account and details.
	Account Account `json:"account"`
	// Service is set when the action runs a service, Resource when it
	// provisions one.
	Service  *Service  `json:"service,omitempty"`
	Resource *Resource `json:"resource,omitempty"`
	// Current is what runs the action now, if anything does.
	Current *Target `json:"current,omitempty"`
	// Resume is what Checkpoint saved in an earlier attempt, or nil.
	Resume []byte `json:"resume,omitempty"`

	source     func() (io.ReadCloser, error)
	progress   func(string)
	checkpoint func([]byte)
}

// Service is a service to run.
type Service struct {
	// Source is the service's entrypoint in the repository, as analysis
	// found it: a Go package's directory, a Node package, a Dockerfile.
	Source    string `json:"source"`
	Ecosystem string `json:"ecosystem,omitempty"`
	Framework string `json:"framework,omitempty"`
	// Dockerfile is the Dockerfile that builds it, if there is one.
	Dockerfile string `json:"dockerfile,omitempty"`
	// Context is the directory Dockerfile builds from, as a path from the
	// repository's root: "." for the root. Set whenever Dockerfile is.
	Context  string  `json:"context,omitempty"`
	Build    string  `json:"build,omitempty"`
	Start    string  `json:"start,omitempty"`
	Workload string  `json:"workload"`
	Port     int     `json:"port,omitempty"`
	Public   bool    `json:"public"`
	MinScale int     `json:"min_scale,omitempty"`
	MaxScale int     `json:"max_scale,omitempty"`
	CPU      float64 `json:"cpu,omitempty"`
	MemoryMB int     `json:"memory_mb,omitempty"`
	// Env are its variables: its own, and the addresses of what it needs.
	Env map[string]string `json:"env,omitempty"`
	// Secrets are the keys of Env that are secret. Keep them in the
	// cloud's secret store if it has one.
	Secrets []string `json:"secrets,omitempty"`
}

// Resource is a resource to provision.
type Resource struct {
	Kind    string `json:"kind"`
	Version string `json:"version,omitempty"`
}

// Target is what a binding names: something the provider made.
type Target struct {
	ExternalID string     `json:"external_id"`
	Revision   string     `json:"revision,omitempty"`
	URL        string     `json:"url,omitempty"`
	Credential Credential `json:"credential"`
	Account    Account    `json:"account"`
}

// Outcome is what Apply made.
type Outcome struct {
	// ExternalID is what the provider calls it, which Remove and Observe
	// get back.
	ExternalID string `json:"external_id"`
	// URL is a public service's address.
	URL string `json:"url,omitempty"`
	// Image is the image a build produced, if there was one.
	Image string `json:"image,omitempty"`
	// Revision is what went live, which drift checks compare with.
	Revision string `json:"revision,omitempty"`
	// Address is how services reach a resource, such as a database URL
	// with its password. Loomplane keeps it in Vault and gives it to the
	// services that need the resource as a secret.
	Address string `json:"address,omitempty"`
}

// Observation is what's live for a binding.
type Observation struct {
	Exists   bool   `json:"exists"`
	Revision string `json:"revision,omitempty"`
}

// Source opens the service's source: its own repository at the commit the
// plan pins for it (Commit), as a gzipped tarball with one top-level
// directory, the way GitHub serves it. A gateway's is the program
// Loomplane generates for it. Only a service's Apply has one.
func (j *Job) Source() (io.ReadCloser, error) {
	if j.source == nil {
		return nil, errors.New("this job has no source")
	}
	return j.source()
}

// Progress shows what the job is doing on the deployment's page.
func (j *Job) Progress(message string) {
	if j.progress != nil {
		j.progress(message)
	}
}

// Checkpoint saves progress a retry resumes from, such as a build's ID.
func (j *Job) Checkpoint(data []byte) {
	if j.checkpoint != nil {
		j.checkpoint(data)
	}
}
