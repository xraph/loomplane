package fleets

import (
	"testing"
	"time"
)

func resourceAccessFixture() (ResourceBinding, ResourceAccessObservation) {
	b := ResourceBinding{PrincipalID: "instance-role", AccessScope: "instance-database/instance-schema", ID: "binding", Scope: FleetScope{OrgID: "org", ProjectID: "project", FleetID: "fleet"}, InstanceID: "instance", EnvironmentID: "env", Name: "database", Ownership: "owned", Generation: 1, OwnershipEpoch: 1, ResourceID: "resource", Incarnation: "physical-1", Provider: "provider", ConnectionID: "connection", ConnectionGeneration: 1, Credential: ResourceCredentialRef{EndpointID: "endpoint", Version: 1}}
	o := ResourceAccessObservation{ResourceID: b.ResourceID, Incarnation: b.Incarnation, ConnectionGeneration: 1, Credential: b.Credential, InstanceID: b.InstanceID, CheckedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Minute), Protocol: "postgres-role-v1", PrincipalID: "instance-role", ScopeID: "instance-database/instance-schema", Database: &DatabaseAccessScope{Database: "instance-database", Schema: "instance-schema", Login: true, CanRead: true, CanWrite: true}}
	return b, o
}

func TestInstanceResourceCredentialsScoped(t *testing.T) {
	b, o := resourceAccessFixture()
	if err := validateResourceAccess(b, o, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ResourceAccessObservation){
		"superuser":          func(o *ResourceAccessObservation) { o.Database.Superuser = true },
		"create role":        func(o *ResourceAccessObservation) { o.Database.CreateRole = true },
		"create database":    func(o *ResourceAccessObservation) { o.Database.CreateDatabase = true },
		"replication":        func(o *ResourceAccessObservation) { o.Database.Replication = true },
		"bypass RLS":         func(o *ResourceAccessObservation) { o.Database.BypassRLS = true },
		"inherited role":     func(o *ResourceAccessObservation) { o.Database.OtherRoles = []string{"admin"} },
		"other database":     func(o *ResourceAccessObservation) { o.Database.OtherDatabases = []string{"neighbor"} },
		"other schema":       func(o *ResourceAccessObservation) { o.Database.OtherSchemas = []string{"neighbor"} },
		"definer function":   func(o *ResourceAccessObservation) { o.Database.PrivilegedFunctions = true },
		"recreated resource": func(o *ResourceAccessObservation) { o.Incarnation = "physical-2" },
		"other instance":     func(o *ResourceAccessObservation) { o.InstanceID = "neighbor" },
		"rotated credential": func(o *ResourceAccessObservation) { o.Credential.Version++ },
		"stale":              func(o *ResourceAccessObservation) { o.ExpiresAt = time.Now().Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			_, candidate := resourceAccessFixture()
			mutate(&candidate)
			if validateResourceAccess(b, candidate, time.Now()) == nil {
				t.Fatal("unscoped credential accepted")
			}
		})
	}
}

func TestInstanceBucketScopeClaims(t *testing.T) {
	b, o := resourceAccessFixture()
	o.Protocol = "bucket-policy-v1"
	o.Database = nil
	o.ScopeID = "instance-bucket"
	b.AccessScope = o.ScopeID
	o.Bucket = &BucketAccessScope{Bucket: "instance-bucket", PolicyRevision: "policy-1", ServerEnforced: true, CanRead: true, CanWrite: true}
	if err := validateResourceAccess(b, o, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*BucketAccessScope){func(b *BucketAccessScope) { b.Prefix = "tenant/" }, func(b *BucketAccessScope) { b.ServerEnforced = false }, func(b *BucketAccessScope) { b.OtherBuckets = true }, func(b *BucketAccessScope) { b.CanManagePolicy = true }, func(b *BucketAccessScope) { b.CanListAccount = true }, func(b *BucketAccessScope) { b.PolicyRevision = "" }} {
		copyObservation := o
		copyBucket := *o.Bucket
		copyObservation.Bucket = &copyBucket
		change(&copyBucket)
		if validateResourceAccess(b, copyObservation, time.Now()) == nil {
			t.Fatal("unscoped bucket claim accepted")
		}
	}
}

func TestInstanceAttachedResourceRetained(t *testing.T) {
	b, _ := resourceAccessFixture()
	if !b.PhysicalDeletionAllowed("instance", 1) {
		t.Fatal("owned resource lost ownership")
	}
	for _, ownership := range []string{"attached", "shared", "", "unknown"} {
		b.Ownership = ownership
		if b.PhysicalDeletionAllowed("instance", 1) {
			t.Fatal("external resource eligible for deletion")
		}
	}
	b.Ownership = "owned"
	if b.PhysicalDeletionAllowed("neighbor", 1) || b.PhysicalDeletionAllowed("instance", 2) {
		t.Fatal("stale owner can delete resource")
	}
}
