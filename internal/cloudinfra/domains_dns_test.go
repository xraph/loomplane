package cloudinfra

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/xraph/loomplane/internal/security"
	"github.com/xraph/loomplane/pkg/provider"
)

type dnsProofResolver struct {
	DomainResolver
	values []string
	err    error
}

func (r *dnsProofResolver) LookupTXT(context.Context, string) ([]string, error) {
	return r.values, r.err
}

type dnsProofStore struct {
	DomainStore
	zone  Zone
	saves int
}

func (s *dnsProofStore) Zone(context.Context, string, string) (*Zone, error) {
	z := s.zone
	return &z, nil
}
func (s *dnsProofStore) SaveZone(_ context.Context, z *Zone) error {
	s.zone = *z
	s.saves++
	return nil
}

func dnsProofFixture() (*Service, *dnsProofStore, *dnsProofResolver) {
	st := &dnsProofStore{zone: Zone{ID: "zone", OrgID: "org", Mode: "external", Name: "example.com", ProofName: "_loomplane.example.com", ProofValue: "expected", ProofExpiresAt: time.Now().Add(time.Hour), Revision: 1}}
	resolver := &dnsProofResolver{}
	return NewService(Deps{Domains: st, Resolver: resolver, Authz: cleanupTestAuth{}}), st, resolver
}

// checkedOnly reports whether a failed check left the zone as it was, apart
// from the time it was checked. VerifyZone records that on every check.
func checkedOnly(before, after Zone) bool {
	if after.ProofCheckedAt.IsZero() {
		return false
	}
	after.ProofCheckedAt = before.ProofCheckedAt
	return reflect.DeepEqual(before, after)
}
func TestDNSProofAbsentThenVisible(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprint(wrapped), func(t *testing.T) {
			svc, st, r := dnsProofFixture()
			before := st.zone
			r.err = &net.DNSError{Err: "no such host", Name: st.zone.ProofName, IsNotFound: true}
			if wrapped {
				r.err = fmt.Errorf("lookup: %w", r.err)
			}
			p := security.Principal{OrgID: "org", UserID: "manager"}
			if _, err := svc.VerifyZone(t.Context(), p, "zone"); !errors.Is(err, ErrConflict) {
				t.Fatalf("missing proof must be conflict: %v", err)
			}
			if st.saves != 1 || !checkedOnly(before, st.zone) {
				t.Fatal("missing proof changed verification")
			}
			r.err = nil
			r.values = []string{"unrelated", st.zone.ProofValue}
			verified, err := svc.VerifyZone(t.Context(), p, "zone")
			if err != nil || verified == nil || verified.VerifiedAt.IsZero() || st.saves != 2 {
				t.Fatalf("visible proof: %+v %v saves=%d", verified, err, st.saves)
			}
		})
	}
}
func TestDNSProofFailuresPreserveVerification(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		values   []string
		conflict bool
	}{
		{name: "empty", conflict: true}, {name: "mismatch", values: []string{"other-proof"}, conflict: true},
		{name: "not_found", err: &net.DNSError{IsNotFound: true}, conflict: true},
		{name: "timeout", err: &net.DNSError{IsTimeout: true}},
		{name: "temporary", err: &net.DNSError{IsTemporary: true}},
		{name: "ambiguous_not_found_timeout", err: &net.DNSError{IsNotFound: true, IsTimeout: true}},
		{name: "ambiguous_not_found_temporary", err: &net.DNSError{IsNotFound: true, IsTemporary: true}},
		{name: "wrapped_cancellation", err: errors.Join(&net.DNSError{IsNotFound: true}, context.Canceled)},
		{name: "wrapped_deadline", err: errors.Join(&net.DNSError{IsNotFound: true}, context.DeadlineExceeded)},
		{name: "backend", err: errors.New("resolver unavailable")},
		{name: "canceled", err: context.Canceled}, {name: "deadline", err: context.DeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, r := dnsProofFixture()
			st.zone.VerifiedAt = time.Now().Add(-time.Hour)
			before := st.zone
			r.err, r.values = tc.err, tc.values
			_, err := svc.VerifyZone(t.Context(), security.Principal{OrgID: "org", UserID: "manager"}, "zone")
			if tc.conflict {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("want conflict, got %v", err)
				}
			} else if !errors.Is(err, tc.err) || errors.Is(err, ErrConflict) {
				t.Fatalf("resolver failure changed: %v", err)
			}
			if st.saves != 1 || !checkedOnly(before, st.zone) {
				t.Fatal("failure changed stored verification")
			}
		})
	}
}

type dnsProviderFailure struct {
	routeDNSAdapter
	err error
}

func (a *dnsProviderFailure) GetZone(ctx context.Context, c provider.InfrastructureCredential, name string) (provider.DNSZone, error) {
	if a.err != nil {
		return provider.DNSZone{}, a.err
	}
	return a.routeDNSAdapter.GetZone(ctx, c, name)
}
func TestDNSManagedProofRequiresProviderAndPublicControl(t *testing.T) {
	svc, st, r := dnsProofFixture()
	registry := NewAdapters()
	adapter := &dnsProviderFailure{}
	if err := registry.RegisterDNS("vercel", adapter); err != nil {
		t.Fatal(err)
	}
	svc.Adapters, svc.Connections = registry, routeDNSCredentials{"vercel"}
	st.zone.Mode, st.zone.ConnectionID, st.zone.Provider, st.zone.AccountID, st.zone.ConnectionGeneration = "managed", "dns", "vercel", "account", 1
	st.zone.ProviderZone = provider.DNSZone{ID: "zone", Name: "example.com", AccountID: "account"}
	before := st.zone
	p := security.Principal{OrgID: "org", UserID: "manager"}
	for _, failure := range []error{errors.New("provider unavailable"), &net.DNSError{IsNotFound: true}} {
		adapter.err = failure
		r.values = []string{st.zone.ProofValue}
		if _, err := svc.VerifyZone(t.Context(), p, "zone"); !errors.Is(err, failure) || errors.Is(err, ErrConflict) {
			t.Fatalf("provider failure remapped: %v", err)
		}
	}
	adapter.err = nil
	r.err = &net.DNSError{IsNotFound: true}
	if _, err := svc.VerifyZone(t.Context(), p, "zone"); !errors.Is(err, ErrConflict) {
		t.Fatalf("public absence: %v", err)
	}
	if !checkedOnly(before, st.zone) {
		t.Fatal("failed managed proof changed verification")
	}
	r.err = nil
	saved := st.saves
	if _, err := svc.VerifyZone(t.Context(), p, "zone"); err != nil || st.saves != saved+1 {
		t.Fatalf("managed visible proof: %v saves=%d", err, st.saves)
	}
}
