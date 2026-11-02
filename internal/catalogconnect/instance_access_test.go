package catalogconnect

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
)

type instancePinsFunc func(context.Context, fleets.ResourceBinding) error

func (f instancePinsFunc) VerifyInstanceResourcePins(ctx context.Context, b fleets.ResourceBinding) error {
	return f(ctx, b)
}

type instanceBucketFunc func(context.Context, fleets.ResourceBinding, BucketAccess) (fleets.BucketAccessScope, error)

func (f instanceBucketFunc) VerifyInstanceBucketPolicy(ctx context.Context, b fleets.ResourceBinding, a BucketAccess) (fleets.BucketAccessScope, error) {
	return f(ctx, b, a)
}

func TestInstanceResourceProbeRequiresProviderPolicy(t *testing.T) {
	store := newMemStore()
	vault := memSecrets{}
	endpoints := NewService(store, vault, roleAuthz{})
	endpoint, err := endpoints.Create(t.Context(), admin, CreateInput{Name: "Instance assets", Engine: EngineS3, DSN: testBucketJSON})
	if err != nil {
		t.Fatal(err)
	}
	b := fleets.ResourceBinding{Scope: fleets.FleetScope{OrgID: "org"}, ResourceID: "resource", Incarnation: "physical", InstanceID: "instance", ConnectionGeneration: 1, Credential: fleets.ResourceCredentialRef{EndpointID: endpoint.ID, Version: 1}, PrincipalID: "key", AccessScope: "application-assets"}
	calls := 0
	verifier := &InstanceAccessVerifier{Endpoints: endpoints, Pins: instancePinsFunc(func(context.Context, fleets.ResourceBinding) error { calls++; return nil }), Dial: func(context.Context, fleets.ResourceBinding) (DialFunc, error) {
		t.Fatal("bucket policy must not open a database connection")
		return nil, nil
	}}
	if _, err = verifier.VerifyInstanceResourceAccess(t.Context(), b); !errors.Is(err, ErrInstanceAccess) {
		t.Fatal("bucket name was treated as credential policy", err)
	}
	verifier.Buckets = instanceBucketFunc(func(_ context.Context, got fleets.ResourceBinding, a BucketAccess) (fleets.BucketAccessScope, error) {
		if got.Credential != b.Credential || a.AccessKeyID != "AKIAEXAMPLEAPPLICATION" {
			t.Fatal("wrong credential reached provider reader")
		}
		return fleets.BucketAccessScope{Bucket: a.Bucket, PolicyRevision: "provider-policy", ServerEnforced: true, CanRead: true, CanWrite: true}, nil
	})
	observation, err := verifier.VerifyInstanceResourceAccess(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "application-secret") || strings.Contains(string(raw), "AKIAEXAMPLE") {
		t.Fatal("provider credentials escaped in evidence")
	}
	if calls != 3 {
		t.Fatal("provider identity was not checked after the live probe", calls)
	}
	verifier.Buckets = instanceBucketFunc(func(context.Context, fleets.ResourceBinding, BucketAccess) (fleets.BucketAccessScope, error) {
		e := store.rows[endpoint.ID]
		e.Revision++
		store.rows[endpoint.ID] = e
		return fleets.BucketAccessScope{}, nil
	})
	if _, err = verifier.VerifyInstanceResourceAccess(t.Context(), b); !errors.Is(err, ErrInstanceAccess) {
		t.Fatal("credential rotation during probe accepted", err)
	}
}

func TestInstanceResourceProbeRejectsUnverifiedTLS(t *testing.T) {
	endpoints := NewService(newMemStore(), memSecrets{}, roleAuthz{})
	endpoint, err := endpoints.Create(t.Context(), admin, CreateInput{Name: "Instance database", Engine: EnginePostgres, DSN: testDSN})
	if err != nil {
		t.Fatal(err)
	}
	verifier := &InstanceAccessVerifier{Endpoints: endpoints, Pins: instancePinsFunc(func(context.Context, fleets.ResourceBinding) error { return nil }), Dial: func(context.Context, fleets.ResourceBinding) (DialFunc, error) {
		t.Fatal("unverified TLS reached dialer")
		return nil, nil
	}}
	b := fleets.ResourceBinding{Scope: fleets.FleetScope{OrgID: "org"}, Credential: fleets.ResourceCredentialRef{EndpointID: endpoint.ID, Version: 1}, PrincipalID: "u", AccessScope: "app/private"}
	if _, err = verifier.VerifyInstanceResourceAccess(t.Context(), b); !errors.Is(err, ErrInstanceAccess) {
		t.Fatal(err)
	}
}
