package fleets

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"github.com/xraph/loomplane/pkg/fleet"
)

// ResourceCredentialRef identifies a version in the existing endpoint vault.
type ResourceCredentialRef struct {
	EndpointID string `json:"endpoint_id"`
	Version    int64  `json:"version"`
}

// ResourceBinding pins a physical resource separately from its application access.
// Attached and shared resources never transfer physical ownership to an instance.
type ResourceBinding struct {
	PrincipalID          string                `json:"principal_id"`
	AccessScope          string                `json:"access_scope"`
	ID                   string                `json:"id"`
	Scope                FleetScope            `json:"scope"`
	InstanceID           string                `json:"instance_id"`
	EnvironmentID        string                `json:"environment_id"`
	Name                 string                `json:"name"`
	Ownership            string                `json:"ownership"`
	Generation           int64                 `json:"generation"`
	OwnershipEpoch       int64                 `json:"ownership_epoch"`
	ResourceID           string                `json:"resource_id"`
	Incarnation          string                `json:"incarnation"`
	Provider             string                `json:"provider"`
	ConnectionID         string                `json:"connection_id"`
	ConnectionGeneration int64                 `json:"connection_generation"`
	AttachmentID         string                `json:"attachment_id,omitempty"`
	AttachmentVersion    string                `json:"attachment_version,omitempty"`
	Credential           ResourceCredentialRef `json:"credential"`
}

func (b ResourceBinding) PhysicalDeletionAllowed(instance string, epoch int64) bool {
	return b.Ownership == "owned" && instance != "" && b.InstanceID == instance && epoch > 0 && b.OwnershipEpoch == epoch && b.ResourceID != "" && b.Incarnation != "" && b.AttachmentID == ""
}

// DatabaseAccessScope is a provider-observed role and privilege inventory.
// The first protocol supports one private database/schema, not row filtering.
type DatabaseAccessScope struct {
	Database, Schema                                              string
	Login, CanRead, CanWrite                                      bool
	Superuser, CreateRole, CreateDatabase, Replication, BypassRLS bool
	OtherRoles, OtherDatabases, OtherSchemas                      []string
	PrivilegedFunctions                                           bool
}

// BucketAccessScope comes from the provider's effective credential policy.
// Naming a bucket in an SDK request does not establish this scope.
type BucketAccessScope struct {
	Bucket, Prefix, PolicyRevision                string
	ServerEnforced, CanRead, CanWrite             bool
	OtherBuckets, CanManagePolicy, CanListAccount bool
}

type ResourceAccessObservation struct {
	ResourceID, Incarnation, InstanceID string
	ConnectionGeneration                int64
	Credential                          ResourceCredentialRef
	Protocol, PrincipalID, ScopeID      string
	CheckedAt, ExpiresAt                time.Time
	Database                            *DatabaseAccessScope
	Bucket                              *BucketAccessScope
}

type ResourceBindingStore interface {
	ListInstanceResourceBindings(context.Context, FleetScope, string) ([]ResourceBinding, error)
	SaveInstanceResourceBinding(context.Context, FleetScope, InstanceSnapshot, ResourceBinding) error
}

// InstanceResourceAccess resolves current endpoint credentials internally and
// probes their scope. Callers cannot submit a successful observation themselves.
type InstanceResourceAccess interface {
	VerifyInstanceResourceAccess(context.Context, ResourceBinding) (ResourceAccessObservation, error)
}

func validateResourceAccess(b ResourceBinding, o ResourceAccessObservation, now time.Time) error {
	if b.ResourceID == "" || b.Incarnation == "" || b.InstanceID == "" || b.Credential.EndpointID == "" || b.Credential.Version < 1 || b.ConnectionGeneration < 1 || o.ResourceID != b.ResourceID || o.Incarnation != b.Incarnation || o.InstanceID != b.InstanceID || o.ConnectionGeneration != b.ConnectionGeneration || o.Credential != b.Credential || b.PrincipalID == "" || b.AccessScope == "" || o.PrincipalID != b.PrincipalID || o.ScopeID != b.AccessScope || o.CheckedAt.IsZero() || o.CheckedAt.After(now) || now.Sub(o.CheckedAt) > 5*time.Minute || !o.ExpiresAt.After(now) || o.ExpiresAt.After(o.CheckedAt.Add(5*time.Minute)) {
		return ErrInvalid
	}
	switch o.Protocol {
	case "postgres-role-v1":
		d := o.Database
		if d == nil || o.Bucket != nil || d.Database == "" || d.Schema == "" || o.ScopeID != d.Database+"/"+d.Schema || !d.Login || !d.CanRead || !d.CanWrite || d.Superuser || d.CreateRole || d.CreateDatabase || d.Replication || d.BypassRLS || len(d.OtherRoles) > 0 || len(d.OtherDatabases) > 0 || len(d.OtherSchemas) > 0 || d.PrivilegedFunctions {
			return ErrInvalid
		}
	case "bucket-policy-v1":
		bucket := o.Bucket
		if bucket == nil || o.Database != nil || bucket.Bucket == "" || o.ScopeID != bucket.Bucket || bucket.Prefix != "" || bucket.PolicyRevision == "" || !bucket.ServerEnforced || !bucket.CanRead || !bucket.CanWrite || bucket.OtherBuckets || bucket.CanManagePolicy || bucket.CanListAccount {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// ResolveResourceBindings returns only resources matching the current sealed
// instance and a fresh credential-scope check. No secret material leaves here.
func (s *Service) ResolveResourceBindings(ctx context.Context, scope FleetScope, snapshot InstanceSnapshot) ([]ResourceBinding, error) {
	if scope.OrgID == "" || scope.ProjectID == "" || scope.FleetID != snapshot.FleetID {
		return nil, ErrInvalid
	}
	if err := s.verifyResourceSnapshot(ctx, scope, snapshot); err != nil {
		return nil, err
	}
	store, ok := s.store.(ResourceBindingStore)
	if !ok {
		return nil, ErrInvalid
	}
	bindings, err := store.ListInstanceResourceBindings(ctx, scope, snapshot.InstanceID)
	if err != nil {
		return nil, err
	}
	if len(bindings) != len(snapshot.Template.Resources) || len(bindings) > 64 {
		return nil, ErrConflict
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		policy, exists := snapshot.Template.Resources[b.Name]
		if !exists || seen[b.Name] || !bindingMatchesSnapshot(b, scope, snapshot) || b.Ownership != policy.Ownership || b.AttachmentID != policy.AttachmentID || b.AttachmentVersion != policy.Version || s.resources == nil {
			return nil, ErrConflict
		}
		seen[b.Name] = true
		observation, checkErr := s.resources.VerifyInstanceResourceAccess(ctx, b)
		if checkErr != nil {
			return nil, checkErr
		}
		if validationErr := validateResourceAccess(b, observation, time.Now()); validationErr != nil {
			return nil, validationErr
		}
	}
	// Live probes can outlast a release or ownership change. Recheck all pins.
	if checkErr := s.verifyResourceSnapshot(ctx, scope, snapshot); checkErr != nil {
		return nil, checkErr
	}
	current, err := store.ListInstanceResourceBindings(ctx, scope, snapshot.InstanceID)
	if err != nil {
		return nil, err
	}
	sortBindings := func(rows []ResourceBinding) {
		slices.SortFunc(rows, func(a, b ResourceBinding) int {
			if a.Name < b.Name {
				return -1
			}
			if a.Name > b.Name {
				return 1
			}
			return 0
		})
	}
	sortBindings(bindings)
	sortBindings(current)
	if !reflect.DeepEqual(bindings, current) {
		return nil, ErrConflict
	}
	return bindings, nil
}

func bindingMatchesSnapshot(b ResourceBinding, scope FleetScope, snapshot InstanceSnapshot) bool {
	return b.ID != "" && b.PrincipalID != "" && b.AccessScope != "" && b.Scope == scope && b.InstanceID == snapshot.InstanceID && b.EnvironmentID == snapshot.EnvironmentID && b.Generation == snapshot.Generation && b.OwnershipEpoch == snapshot.OwnershipEpoch && b.ResourceID != "" && b.Incarnation != "" && b.Provider != "" && b.ConnectionID != "" && b.ConnectionGeneration > 0 && b.Credential.EndpointID != "" && b.Credential.Version > 0
}

func (s *Service) verifyResourceSnapshot(ctx context.Context, scope FleetScope, snapshot InstanceSnapshot) error {
	f, err := s.store.GetFleet(ctx, scope.OrgID, scope.FleetID)
	if err != nil {
		return err
	}
	if f.ProjectID != scope.ProjectID || f.Status != "active" {
		return ErrConflict
	}
	instance, err := s.store.ManagedEnvironment(ctx, scope.OrgID, snapshot.EnvironmentID)
	if err != nil {
		return err
	}
	if instance.Status == "retired" || instance.Status == "deleted" {
		return ErrConflict
	}
	releases, ok := s.store.(ReleaseStore)
	if !ok {
		return ErrInvalid
	}
	release, err := releases.GetRelease(ctx, scope, snapshot.ReleaseID)
	if err != nil {
		return err
	}
	rebuilt, err := RenderInstance(*release, *instance, fleet.CreateInstanceInput{StageID: snapshot.StageID, ReleaseID: snapshot.ReleaseID, ProfileID: snapshot.ProfileID, Parameters: snapshot.Parameters, SecretRefs: snapshot.SecretRefs})
	if err != nil {
		return err
	}
	a, err := json.Marshal(rebuilt)
	if err != nil {
		return err
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return ErrConflict
	}
	return nil
}
