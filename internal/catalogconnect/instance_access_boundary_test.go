package catalogconnect

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/xraph/loomplane/internal/fleets"
)

func instanceBoundaryVerifier(t *testing.T) (*InstanceAccessVerifier, fleets.ResourceBinding) {
	t.Helper()
	endpoints := NewService(newMemStore(), memSecrets{}, roleAuthz{})
	endpoint, err := endpoints.Create(t.Context(), admin, CreateInput{Name: "Instance assets", Engine: EngineS3, DSN: testBucketJSON})
	if err != nil {
		t.Fatal(err)
	}
	binding := fleets.ResourceBinding{Scope: fleets.FleetScope{OrgID: "org"}, ResourceID: "resource", Incarnation: "physical", InstanceID: "instance", ConnectionGeneration: 1, Credential: fleets.ResourceCredentialRef{EndpointID: endpoint.ID, Version: 1}, PrincipalID: "key", AccessScope: "application-assets"}
	verifier := &InstanceAccessVerifier{
		Endpoints: endpoints,
		Pins:      instancePinsFunc(func(context.Context, fleets.ResourceBinding) error { return nil }),
		Dial: func(context.Context, fleets.ResourceBinding) (DialFunc, error) {
			t.Fatal("bucket policy probe dialed a database")
			return nil, nil
		},
		Buckets: instanceBucketFunc(func(context.Context, fleets.ResourceBinding, BucketAccess) (fleets.BucketAccessScope, error) {
			return fleets.BucketAccessScope{Bucket: binding.AccessScope, PolicyRevision: "policy-1", ServerEnforced: true, CanRead: true, CanWrite: true}, nil
		}),
	}
	return verifier, binding
}

func TestInstanceAccessRejectsCancellationAtProbeBoundaries(t *testing.T) {
	for _, boundary := range []string{"before pins", "after pins", "after policy", "after final pins"} {
		t.Run(boundary, func(t *testing.T) {
			verifier, binding := instanceBoundaryVerifier(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if boundary == "before pins" {
				cancel()
			}
			pinsCalls := 0
			verifier.Pins = instancePinsFunc(func(context.Context, fleets.ResourceBinding) error {
				pinsCalls++
				if boundary == "before pins" {
					t.Fatal("cancelled request reached physical identity probe")
				}
				if boundary == "after pins" || boundary == "after final pins" && pinsCalls == 2 {
					cancel()
				}
				return nil
			})
			verifier.Buckets = instanceBucketFunc(func(context.Context, fleets.ResourceBinding, BucketAccess) (fleets.BucketAccessScope, error) {
				if boundary == "after pins" {
					t.Fatal("cancelled request reached credential policy probe")
				}
				if boundary == "after policy" {
					cancel()
				}
				return fleets.BucketAccessScope{Bucket: binding.AccessScope, PolicyRevision: "policy-1", ServerEnforced: true, CanRead: true, CanWrite: true}, nil
			})
			observation, err := verifier.VerifyInstanceResourceAccess(ctx, binding)
			if !errors.Is(err, context.Canceled) || observation.Protocol != "" {
				t.Fatalf("cancelled probe returned access evidence: %+v, %v", observation, err)
			}
		})
	}
}

func TestInstanceAccessReturnsScopedObservation(t *testing.T) {
	verifier, binding := instanceBoundaryVerifier(t)
	observation, err := verifier.VerifyInstanceResourceAccess(t.Context(), binding)
	if err != nil || observation.Protocol != "bucket-policy-v1" || observation.ScopeID != binding.AccessScope || observation.PrincipalID != binding.PrincipalID || observation.Credential != binding.Credential {
		t.Fatalf("scoped policy not accepted: %+v, %v", observation, err)
	}
}

type instanceBoundaryStore struct {
	Store
	cancel   context.CancelFunc
	reads    int
	cancelAt int
}

func (s *instanceBoundaryStore) Get(ctx context.Context, org, id string) (*Endpoint, error) {
	s.reads++
	if s.reads == s.cancelAt {
		s.cancel()
	}
	return s.Store.Get(ctx, org, id)
}

func TestInstanceAccessCancellationAfterEndpointRead(t *testing.T) {
	verifier, binding := instanceBoundaryVerifier(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	verifier.Endpoints.store = &instanceBoundaryStore{Store: verifier.Endpoints.store, cancel: cancel, cancelAt: 2}
	verifier.Pins = instancePinsFunc(func(ctx context.Context, _ fleets.ResourceBinding) error {
		if ctx.Err() != nil {
			t.Fatal("cancelled endpoint check reached another identity probe")
		}
		return nil
	})
	observation, err := verifier.VerifyInstanceResourceAccess(ctx, binding)
	if !errors.Is(err, context.Canceled) || observation.Protocol != "" {
		t.Fatalf("cancelled endpoint read returned access evidence: %+v, %v", observation, err)
	}
}

func TestInstanceAccessCancellationAfterDialResolution(t *testing.T) {
	verifier, binding := instanceBoundaryVerifier(t)
	endpoint, err := verifier.Endpoints.Create(t.Context(), admin, CreateInput{Name: "Instance database", Engine: EnginePostgres, DSN: "postgres://u:secret@database.example.test:5432/app?sslmode=verify-full"})
	if err != nil {
		t.Fatal(err)
	}
	binding.Credential = fleets.ResourceCredentialRef{EndpointID: endpoint.ID, Version: 1}
	binding.PrincipalID, binding.AccessScope = "u", "app/private"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	verifier.Dial = func(context.Context, fleets.ResourceBinding) (DialFunc, error) {
		cancel()
		return func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("cancelled target resolution opened a database connection")
			return nil, nil
		}, nil
	}
	observation, err := verifier.VerifyInstanceResourceAccess(ctx, binding)
	if !errors.Is(err, context.Canceled) || observation.Protocol != "" {
		t.Fatalf("cancelled dial resolution entered database probe: %+v, %v", observation, err)
	}
}

type instanceBoundarySecrets struct {
	Secrets
	reads  int
	cancel context.CancelFunc
}

func (s *instanceBoundarySecrets) Get(ctx context.Context, key string) ([]byte, error) {
	s.reads++
	if s.cancel != nil {
		s.cancel()
	}
	return s.Secrets.Get(ctx, key)
}

func TestInstanceAccessCancellationBeforeVaultRead(t *testing.T) {
	verifier, binding := instanceBoundaryVerifier(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	vault := &instanceBoundarySecrets{Secrets: verifier.Endpoints.secrets}
	verifier.Endpoints.secrets = vault
	verifier.Endpoints.store = &instanceBoundaryStore{Store: verifier.Endpoints.store, cancel: cancel, cancelAt: 1}
	observation, err := verifier.VerifyInstanceResourceAccess(ctx, binding)
	if vault.reads != 0 {
		t.Fatalf("cancelled metadata lookup opened Vault %d times", vault.reads)
	}
	if !errors.Is(err, context.Canceled) || observation.Protocol != "" {
		t.Fatalf("cancelled metadata lookup returned access evidence: %+v, %v", observation, err)
	}
}

func TestInstancePostgresProbeRejectsCancelledDial(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	dial := func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, context.Canceled
	}
	_, err := probeInstancePostgres(ctx, dial, "postgres://u:secret@database.example.test:5432/app?sslmode=verify-full", "app", "private", "u")
	if calls != 0 {
		t.Fatalf("cancelled database probe made %d network attempts", calls)
	}
	if !errors.Is(err, ErrInstanceAccess) {
		t.Fatalf("probe did not preserve its redacted error: %v", err)
	}
}

func TestInstanceEndpointResolveCancellation(t *testing.T) {
	for _, boundary := range []string{"before metadata", "after metadata", "after vault"} {
		t.Run(boundary, func(t *testing.T) {
			verifier, binding := instanceBoundaryVerifier(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			store := &instanceBoundaryStore{Store: verifier.Endpoints.store, cancel: cancel}
			vault := &instanceBoundarySecrets{Secrets: verifier.Endpoints.secrets}
			wantMetadata, wantVault := 1, 0
			switch boundary {
			case "before metadata":
				wantMetadata = 0
				cancel()
			case "after metadata":
				store.cancelAt = 1
			case "after vault":
				wantVault = 1
				vault.cancel = cancel
			}
			verifier.Endpoints.store, verifier.Endpoints.secrets = store, vault
			endpoint, dsn, err := verifier.Endpoints.Resolve(ctx, binding.Scope.OrgID, binding.Credential.EndpointID)
			if store.reads != wantMetadata || vault.reads != wantVault {
				t.Fatalf("cancelled resolution continued reads: metadata=%d, vault=%d", store.reads, vault.reads)
			}
			if !errors.Is(err, context.Canceled) || endpoint != nil || dsn != "" {
				t.Fatal("cancelled resolution returned endpoint credentials or lost cancellation")
			}
		})
	}
}

func TestInstanceAccessProviderFailureRedaction(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		verifier, binding := instanceBoundaryVerifier(t)
		ctx, cancel := context.WithCancel(t.Context())
		providerFailure := errors.New("provider response contains private access material")
		verifier.Buckets = instanceBucketFunc(func(context.Context, fleets.ResourceBinding, BucketAccess) (fleets.BucketAccessScope, error) {
			if cancelled {
				cancel()
			}
			return fleets.BucketAccessScope{}, providerFailure
		})
		observation, err := verifier.VerifyInstanceResourceAccess(ctx, binding)
		cancel()
		want := ErrInstanceAccess
		if cancelled {
			want = context.Canceled
		}
		if !errors.Is(err, want) || observation.Protocol != "" {
			t.Fatalf("callback failure was not safely classified (cancelled=%v)", cancelled)
		}
	}
}
