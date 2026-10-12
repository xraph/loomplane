// Package environments is Loomplane's environment configuration: the
// variables a service runs with, set in layers.
//
// A variable is set on one layer, organization-wide, for a project, or for
// one service of a project, and for every environment or for one. When a
// service runs in an environment, each key takes its value from the most
// specific layer that sets it:
//
//	organization, every environment     least specific
//	organization, this environment
//	project, every environment
//	project, this environment
//	service, every environment
//	service, this environment           most specific
//
// A secret variable's value is encrypted in Vault and never read back
// through the API. Only a deployment (milestone 4) reveals it.
package environments

import (
	"context"
	"errors"
	"time"
)

// Scopes, the layers a variable is set on.
const (
	ScopeOrganization = "organization"
	ScopeProject      = "project"
	ScopeService      = "service"
)

// Errors the service and stores return.
var (
	ErrNotFound = errors.New("variable not found")
	ErrInvalid  = errors.New("invalid variable")
	ErrExists   = errors.New("that key is already set on this layer for that environment; edit it instead")
)

// Variable is one key set on one layer.
type Variable struct {
	ID    string `json:"id" forge:"id"`
	OrgID string `json:"org_id"`
	// Scope is organization, project or service.
	Scope string `json:"scope"`
	// ProjectID is empty for an organization-wide variable.
	ProjectID string `json:"project_id,omitempty"`
	// Service is the analyzed service it's set for (an ID from the
	// project's application graph), for a service variable.
	Service string `json:"service,omitempty"`
	Key     string `json:"key"`
	// Value is always empty for a secret: secrets are written, never read
	// back.
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
	// Environment is the environment it applies in, by name; empty means
	// every environment.
	Environment string    `json:"environment"`
	CreatedBy   string    `json:"created_by"`
	UpdatedBy   string    `json:"updated_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// environmentID ties a project or service variable to its environment,
	// so deleting the environment deletes the variable with it.
	environmentID string
}

// rank orders layers from least to most specific.
func (v *Variable) rank() int {
	r := 0
	switch v.Scope {
	case ScopeProject:
		r = 2
	case ScopeService:
		r = 4
	}
	if v.Environment != "" {
		r++
	}
	return r
}

// Store persists variables. Every method is scoped to an organization.
type Store interface {
	// CreateVariable inserts v; a key the layer already sets for that
	// environment is ErrExists.
	CreateVariable(ctx context.Context, v *Variable) error
	GetVariable(ctx context.Context, orgID, id string) (*Variable, error)
	// UpdateVariable writes v's value, secrecy and who changed it.
	UpdateVariable(ctx context.Context, v *Variable) error
	DeleteVariable(ctx context.Context, orgID, id string) error
	// ListOrganization returns the organization-wide variables.
	ListOrganization(ctx context.Context, orgID string) ([]*Variable, error)
	// ListProject returns a project's own variables, project-wide and per
	// service.
	ListProject(ctx context.Context, orgID, projectID string) ([]*Variable, error)
	// ForEnvironment returns every variable that can apply in one of a
	// project's environments: the organization's, for every environment
	// or for one named envName, and the project's own, for every
	// environment or for envID.
	ForEnvironment(ctx context.Context, orgID, projectID, envID, envName string) ([]*Variable, error)
	// SecretIDs returns the IDs of the organization's secret variables.
	SecretIDs(ctx context.Context, orgID string) (map[string]bool, error)
}

// Secrets keeps secret values. VaultSecrets is the real one.
type Secrets interface {
	Put(ctx context.Context, orgID, variableID string, value []byte) error
	Reveal(ctx context.Context, orgID, variableID string) ([]byte, error)
	Delete(ctx context.Context, orgID, variableID string) error
	// IDs lists the variable IDs the organization has secrets stored for.
	IDs(ctx context.Context, orgID string) ([]string, error)
}
