package cloudinfra

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestNormalizeDomainClaims(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{" Example.COM. ", "example.com"}, {"APP.Example.com", "app.example.com"}, {"BÜCHER.example", "xn--bcher-kva.example"},
	} {
		got, err := NormalizeHostname(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("%q: %q %v", tc.input, got, err)
		}
	}
	for _, input := range []string{"*.example.com", "example.com..", "https://example.com", "example.com:443", "127.0.0.1", "example", "-bad.example", "bad_.example", "example.com/path", "evil\n.example"} {
		if _, err := NormalizeHostname(input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid exact hostname %q accepted: %v", input, err)
		}
	}
	for _, tc := range []struct{ host, zone, want string }{{"example.com", "example.com", ""}, {"a.example.com", "example.com", "a"}, {"a.b.example.com", "example.com", "a.b"}} {
		got, err := zoneRelative(tc.host, tc.zone)
		if err != nil || got != tc.want {
			t.Fatalf("zone containment: %q %v", got, err)
		}
	}
	if _, err := zoneRelative("otherexample.com", "example.com"); err == nil {
		t.Fatal("suffix collision accepted")
	}
}

type proofResolver struct{ values []string }

func (r proofResolver) LookupTXT(context.Context, string) ([]string, error)      { return r.values, nil }
func (proofResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) { return nil, nil }

func TestDomainProofExpirationAndContinuedControl(t *testing.T) {
	now := time.Now().UTC()
	svc := NewService(Deps{Resolver: proofResolver{[]string{"expected"}}})
	svc.now = func() time.Time { return now }
	zone := &Zone{Mode: "external", ProofValue: "expected", ProofExpiresAt: now.Add(-time.Second)}
	if err := svc.verifyZoneControl(t.Context(), zone); !errors.Is(err, ErrConflict) {
		t.Fatal("expired unverified proof accepted", err)
	}
	zone.ProofExpiresAt = now.Add(time.Minute)
	if err := svc.verifyZoneControl(t.Context(), zone); err != nil {
		t.Fatal(err)
	}
	zone.VerifiedAt, zone.ProofExpiresAt = now.Add(-time.Hour), now.Add(-time.Minute)
	if err := svc.verifyZoneControl(t.Context(), zone); err != nil {
		t.Fatal("established control should remain valid while TXT exists", err)
	}
	svc.Resolver = proofResolver{[]string{"another-organization-proof"}}
	if err := svc.verifyZoneControl(t.Context(), zone); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign TXT established control", err)
	}
}
