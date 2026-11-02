package providerresources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

func TestFreshSelectedPartialObservationAllowsAdminAndApprovedProjectVerification(t *testing.T) {
	for _, projectAccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "admin", true: "approved-project"}[projectAccess], func(t *testing.T) {
			s, _, reader, auth := fixture(t)
			at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return at }
			initial := reader.fn
			reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
				out, err := initial(c)
				out.Resources[0].Scope = "account/postgres"
				return out, err
			}
			s.deps.Endpoints = catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, endpointSecrets{}, auth)
			_, _ = s.Sync(t.Context(), principal, "one")
			list, _ := s.List(t.Context(), principal)
			connected, err := s.Connect(t.Context(), principal, list.Resources[0].ID, ConnectInput{DSN: "postgres://app:private@db.example/app?sslmode=require"})
			if err != nil {
				t.Fatal(err)
			}
			original := reader.fn
			reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
				out, err := original(c)
				out.Complete = false
				return out, err
			}
			at = at.Add(time.Minute)
			if !projectAccess {
				got, verifyErr := s.Verified(t.Context(), principal, connected.ID)
				if verifyErr != nil || got == nil || got.ObservationState != "observed" || !got.ObservedAt.Equal(at) {
					t.Fatalf("fresh direct partial observation was rejected: %v", verifyErr)
				}
				return
			}
			binding := ResourceBinding{ID: "attachment", ProjectID: "project", EnvironmentID: "production", Provider: connected.Provider, AccountIdentity: connected.AccountIdentity, ExternalID: connected.ExternalID, Incarnation: connected.Incarnation, InventoryResourceID: connected.ID, Revision: connected.Revision, EndpointID: connected.EndpointID, EndpointRevision: connected.EndpointRevision}
			s.deps.Usage = func(context.Context, string) ([]ResourceBinding, error) { return []ResourceBinding{binding}, nil }
			s.deps.Authorizer = projectOnlyAuth{}
			_, endpoint, raw, resolveErr := s.ResolveProject(t.Context(), security.Principal{OrgID: "org", UserID: "developer"}, "project", "production", connected.ID, connected.Revision)
			if resolveErr != nil || endpoint == nil || raw == "" {
				t.Fatalf("approved project could not resolve a directly observed partial resource: %v", resolveErr)
			}
		})
	}
}

type observationRaceStore struct {
	Store
	gets           int
	afterSecondGet func()
}

func (s *observationRaceStore) Get(ctx context.Context, org, id string) (*ProviderResource, error) {
	row, err := s.Store.Get(ctx, org, id)
	s.gets++
	if s.gets == 2 && s.afterSecondGet != nil {
		s.afterSecondGet()
	}
	return row, err
}

func TestPartialVerificationRejectsUnseenFailedReplacedAndSupersededObservations(t *testing.T) {
	for _, scenario := range []string{"unseen", "failed-sync", "failed-resource", "missing-resource", "unknown-incarnation", "outside-scope", "superseded-sequence", "changed-generation", "stale-finished-observation"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, reader, _ := fixture(t)
			at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return at }
			_, _ = s.Sync(t.Context(), principal, "one")
			list, _ := s.List(t.Context(), principal)
			selected := list.Resources[0]
			original := reader.fn
			reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
				out, err := original(c)
				out.Complete = false
				switch scenario {
				case "unseen":
					out.Resources = nil
				case "failed-sync":
					return provider.ResourceInventory{}, errors.New("provider unavailable")
				case "failed-resource":
					out.Resources[0].Status = "failed"
				case "missing-resource":
					out.Resources[0].Status = "missing"
				case "unknown-incarnation":
					out.Resources[0].Incarnation = ""
				case "outside-scope":
					out.Scope = "another-scope"
					out.Resources[0].Scope = "outside/postgres"
				}
				return out, err
			}
			if scenario == "superseded-sequence" || scenario == "changed-generation" || scenario == "stale-finished-observation" {
				underlying := s.deps.Store
				s.deps.Store = &observationRaceStore{Store: underlying, afterSecondGet: func() {
					if scenario == "stale-finished-observation" {
						at = at.Add(31 * time.Minute)
						return
					}
					_ = underlying.Update(t.Context(), selected.OrgID, selected.Provider, selected.AccountIdentity, "", 0, func(state *AccountState) error {
						if scenario == "superseded-sequence" {
							state.Sequence++
						} else {
							for _, row := range state.Resources {
								row.ConnectionGeneration++
							}
						}
						return nil
					})
				}}
			}
			at = at.Add(time.Minute)
			if got, err := s.Verified(t.Context(), principal, selected.ID); err == nil || got != nil {
				t.Fatal("an observation without current direct selected-resource proof was accepted")
			}
		})
	}
}

type resolutionObservationRace struct {
	Endpoints
	afterResolve func()
}

func (e *resolutionObservationRace) Resolve(ctx context.Context, org, id string) (*catalogconnect.Endpoint, string, error) {
	endpoint, raw, err := e.Endpoints.Resolve(ctx, org, id)
	if err == nil && e.afterResolve != nil {
		e.afterResolve()
	}
	return endpoint, raw, err
}

func TestResolutionRechecksLatestObservationAfterVaultRead(t *testing.T) {
	for _, projectAccess := range []bool{false, true} {
		for _, scenario := range []string{"partial-unseen", "failed-sync", "fresh-direct", "revoked-grant"} {
			name := map[bool]string{false: "admin", true: "approved-project"}[projectAccess] + "/" + scenario
			t.Run(name, func(t *testing.T) {
				s, _, reader, auth := fixture(t)
				at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
				s.now = func() time.Time { return at }
				s.deps.Endpoints = catalogconnect.NewService(&endpointRows{rows: map[string]*catalogconnect.Endpoint{}}, endpointSecrets{}, auth)
				_, _ = s.Sync(t.Context(), principal, "one")
				list, _ := s.List(t.Context(), principal)
				resource, err := s.Connect(t.Context(), principal, list.Resources[0].ID, ConnectInput{DSN: "postgres://app:private@db.example/app?sslmode=require"})
				if err != nil {
					t.Fatal(err)
				}
				original := reader.fn
				reads := 0
				reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) { reads++; return original(c) }
				globalAuth := &revocableSecretsAuth{}
				projectAuth := &revocableProjectAuth{}
				if projectAccess {
					binding := ResourceBinding{ID: "attachment", ProjectID: "project", EnvironmentID: "production", Provider: resource.Provider, AccountIdentity: resource.AccountIdentity, ExternalID: resource.ExternalID, Incarnation: resource.Incarnation, InventoryResourceID: resource.ID, Revision: resource.Revision, EndpointID: resource.EndpointID, EndpointRevision: resource.EndpointRevision}
					s.deps.Usage = func(context.Context, string) ([]ResourceBinding, error) { return []ResourceBinding{binding}, nil }
					s.deps.Authorizer = projectAuth
				} else {
					s.deps.Authorizer = globalAuth
				}
				s.deps.Endpoints = &resolutionObservationRace{Endpoints: s.deps.Endpoints, afterResolve: func() {
					at = at.Add(time.Minute)
					if scenario == "revoked-grant" {
						globalAuth.revoked = true
						projectAuth.denied = true
						return
					}
					reader.fn = func(c *connections.Connection) (provider.ResourceInventory, error) {
						reads++
						out, readErr := original(c)
						out.Complete = false
						switch scenario {
						case "partial-unseen":
							out.Resources = nil
						case "failed-sync":
							return provider.ResourceInventory{}, errors.New("provider failed")
						}
						return out, readErr
					}
					_, _ = s.sync(t.Context(), principal, "one", false)
				}}
				var got *ProviderResource
				var raw string
				if projectAccess {
					got, _, raw, err = s.ResolveProject(t.Context(), security.Principal{OrgID: "org", UserID: "developer"}, "project", "production", resource.ID, resource.Revision)
				} else {
					got, _, raw, err = s.Resolve(t.Context(), principal, resource.ID, resource.Revision)
				}
				if scenario == "fresh-direct" {
					if err != nil || got == nil || raw == "" || !got.ObservedAt.Equal(at) || reads != 2 {
						t.Fatalf("newer direct observation was not used without another sync: err=%v reads=%d", err, reads)
					}
				} else if err == nil || got != nil || raw != "" {
					t.Fatal("credentials escaped after the selected observation or grant became invalid")
				}
			})
		}
	}
}
