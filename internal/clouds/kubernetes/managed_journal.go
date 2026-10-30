package kubernetes

import (
	"context"
	"errors"
	"time"
)

var ErrManagedAuthority = errors.New("managed workload mutation requires current journal authority")

// ManagedScope contains server-owned identity only. Epoch is the current durable
// workload attempt generation. Revocation applies to the whole release.
type ManagedScope struct {
	Org, Project, Environment, Service, Plan, Deployment, Action string
	Connection, Instance, RuntimeRef, RuntimeRevision, Release   string
	Namespace, NamespaceUID, ClusterCA                           string
	Epoch                                                        int64
}

type ManagedRequest struct {
	Group, Version, Resource, Namespace, Name, Method string
	UID                                               string
	ResourceVersion                                   string `json:",omitempty"`
}

type ManagedOutcome struct {
	// State is accepted or rejected. Unknown requests have no outcome.
	State                string
	UID, ResourceVersion string
	Generation           int64
	StatusCode           int
}

type ManagedIssuance struct {
	CleanupGrantID       string `json:",omitempty"`
	CleanupGrantRevision int64  `json:",omitempty"`
	ID                   string
	Scope                ManagedScope
	Request              ManagedRequest
	Outcome              *ManagedOutcome
	IssuedAt             time.Time
	ResolvedAt           *time.Time
}

// ManagedJournal records issuance before transmission. Resolve must retain exact
// accepted outcomes even after revocation, without restoring execution authority.
type ManagedJournal interface {
	Issue(context.Context, ManagedScope, ManagedRequest) (string, error)
	Resolve(context.Context, string, ManagedScope, ManagedOutcome) error
}

type managedScopeKey struct{}

func WithManagedScope(ctx context.Context, scope ManagedScope) context.Context {
	return context.WithValue(ctx, managedScopeKey{}, scope)
}

// ManagedAuthority survives binding and instance deletion for cleanup recovery.
type ManagedAuthority struct {
	Scope                ManagedScope
	SchemaVersion        int
	Revision             int64
	CreatedAt, UpdatedAt time.Time
	RevokedAt            *time.Time
}
