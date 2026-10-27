package cloudinfra

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xraph/forge"

	infra "github.com/xraph/loomplane/internal/cloudinfra"
	"github.com/xraph/loomplane/internal/security"
)

type dnsContextBase = forge.Context

type dnsVerifyContext struct {
	dnsContextBase
	request *http.Request
	status  int
}

func (c *dnsVerifyContext) Request() *http.Request       { return c.request }
func (*dnsVerifyContext) Param(string) string            { return "zone" }
func (c *dnsVerifyContext) JSON(status int, _ any) error { c.status = status; return nil }

type dnsVerifyAuth struct{}

func (dnsVerifyAuth) Require(context.Context, security.Principal, security.Permission, string) error {
	return nil
}

type dnsVerifyResolver struct {
	infra.DomainResolver
	values []string
	err    error
}

func (r *dnsVerifyResolver) LookupTXT(context.Context, string) ([]string, error) {
	return r.values, r.err
}

type dnsVerifyStore struct {
	infra.DomainStore
	zone  infra.Zone
	saves int
}

func (s *dnsVerifyStore) Zone(context.Context, string, string) (*infra.Zone, error) {
	z := s.zone
	return &z, nil
}
func (s *dnsVerifyStore) SaveZone(_ context.Context, z *infra.Zone) error {
	s.zone = *z
	s.saves++
	return nil
}

func TestDNSVerificationHTTPPropagation(t *testing.T) {
	st := &dnsVerifyStore{zone: infra.Zone{Mode: "external", Name: "example.com", ProofName: "_loomplane.example.com", ProofValue: "expected", ProofExpiresAt: time.Now().Add(time.Hour)}}
	resolver := &dnsVerifyResolver{}
	e := &Extension{svc: infra.NewService(infra.Deps{Domains: st, Authz: dnsVerifyAuth{}, Resolver: resolver})}
	for _, tc := range []struct {
		name   string
		err    error
		values []string
		status int
	}{
		{name: "not_found", err: &net.DNSError{Err: "synthetic-sensitive-error", IsNotFound: true}, status: http.StatusConflict},
		{name: "mismatch", values: []string{"wrong"}, status: http.StatusConflict},
		{name: "timeout", err: &net.DNSError{Err: "synthetic-sensitive-error", IsTimeout: true}, status: http.StatusInternalServerError},
		{name: "backend", err: errors.New("synthetic-sensitive-error"), status: http.StatusInternalServerError},
		{name: "visible", values: []string{"expected"}, status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver.err, resolver.values = tc.err, tc.values
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/dns-zones/zone/verify", nil)
			req = req.WithContext(security.WithPrincipal(req.Context(), security.Principal{OrgID: "org", UserID: "manager"}))
			c := &dnsVerifyContext{request: req}
			err := e.verifyDomainZone(c)
			if err != nil {
				var httpErr interface {
					error
					StatusCode() int
				}
				if !errors.As(err, &httpErr) {
					t.Fatalf("non-HTTP error: %v", err)
				}
				c.status = httpErr.StatusCode()
				if tc.status == http.StatusInternalServerError && httpErr.Error() != "Infrastructure operation failed" {
					t.Fatal("raw backend error exposed")
				}
			}
			if c.status != tc.status {
				t.Fatalf("status=%d want=%d", c.status, tc.status)
			}
			if tc.status != http.StatusOK && !st.zone.VerifiedAt.IsZero() {
				t.Fatal("failed proof persisted verification")
			}
		})
	}
	// Every check is saved, failed ones included, so the page can say when
	// the record was last looked for.
	if st.saves != 5 || st.zone.VerifiedAt.IsZero() {
		t.Fatal("visible proof not persisted")
	}
}
