package cloudinfra

import (
	"testing"
	"time"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestDeriveRouteStatus(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	verified := &Zone{Mode: "managed", VerifiedAt: now}
	external := &Zone{Mode: "external", VerifiedAt: now}
	cases := []struct {
		name  string
		route Route
		zone  *Zone
		op    *RouteOperation
		want  [3]DomainState
	}{
		{"unverified", Route{Status: "pending"}, &Zone{Mode: "external"}, nil, [3]DomainState{DomainAwaitingVerification, DomainAwaitingVerification, DomainAwaitingVerification}},
		{"zone gone", Route{Status: "ready"}, nil, nil, [3]DomainState{DomainAwaitingVerification, DomainAwaitingVerification, DomainAwaitingVerification}},
		{"writing", Route{Status: "pending"}, verified, &RouteOperation{Kind: "apply", Status: "running"}, [3]DomainState{DomainProvisioning, DomainProvisioning, DomainProvisioning}},
		{"external record missing", Route{Status: "waiting"}, external, &RouteOperation{Kind: "apply", Status: "waiting"}, [3]DomainState{DomainAwaitingVerification, DomainProvisioning, DomainProvisioning}},
		{"external pending", Route{Status: "pending"}, external, &RouteOperation{Kind: "apply", Status: "pending"}, [3]DomainState{DomainAwaitingVerification, DomainProvisioning, DomainProvisioning}},
		{"ready", Route{Status: "ready", Verification: RouteVerification{DNS: true, TLS: true, Backend: true, CertExpires: now.AddDate(0, 2, 0)}}, verified, nil, [3]DomainState{DomainReady, DomainReady, DomainReady}},
		{"verify running", Route{Status: "ready", Receipt: &provider.DNSRecord{ID: "r"}, Verification: RouteVerification{DNS: true, TLS: true, Backend: true, CertExpires: now.AddDate(0, 2, 0)}}, verified, &RouteOperation{Kind: "verify", Status: "running", Receipt: &provider.DNSRecord{ID: "r"}}, [3]DomainState{DomainReady, DomainReady, DomainReady}},
		{"renewing", Route{Status: "ready", Verification: RouteVerification{DNS: true, TLS: true, Backend: true, CertExpires: now.AddDate(0, 0, 10)}}, verified, nil, [3]DomainState{DomainReady, DomainRenewing, DomainReady}},
		{"reissuing", Route{Status: "ready", Verification: RouteVerification{DNS: true, TLS: true, Backend: true, CertExpires: now.AddDate(0, 2, 0), CertState: "issuing"}}, verified, nil, [3]DomainState{DomainReady, DomainRenewing, DomainReady}},
		{"certificate issuing", Route{Status: "waiting", Verification: RouteVerification{DNS: true}}, verified, &RouteOperation{Kind: "apply", Status: "waiting", Receipt: &provider.DNSRecord{ID: "r"}}, [3]DomainState{DomainReady, DomainProvisioning, DomainProvisioning}},
		{"tls failed", Route{Status: "waiting", Verification: RouteVerification{DNS: true, TLSError: "x"}}, verified, &RouteOperation{Kind: "verify", Status: "failed"}, [3]DomainState{DomainReady, DomainFailed, DomainFailed}},
		{"dns failed", Route{Status: "waiting", Verification: RouteVerification{DNSError: "x"}}, verified, &RouteOperation{Kind: "apply", Status: "failed"}, [3]DomainState{DomainFailed, DomainProvisioning, DomainFailed}},
		{"backend failed", Route{Status: "waiting", Verification: RouteVerification{DNS: true, TLS: true, CertExpires: now.AddDate(0, 2, 0), BackendError: "x"}}, verified, &RouteOperation{Kind: "verify", Status: "failed"}, [3]DomainState{DomainReady, DomainReady, DomainFailed}},
		{"removing", Route{Status: "ready"}, verified, &RouteOperation{Kind: "remove", Status: "pending"}, [3]DomainState{DomainCleanupPending, DomainCleanupPending, DomainCleanupPending}},
		{"removal failed", Route{Status: "ready"}, verified, &RouteOperation{Kind: "remove", Status: "failed"}, [3]DomainState{DomainCleanupPending, DomainCleanupPending, DomainCleanupPending}},
	}
	for _, c := range cases {
		got := DeriveRouteStatus(c.route, c.zone, c.op, now)
		if [3]DomainState{got.DNS, got.Certificate, got.Route} != c.want {
			t.Fatalf("%s: %+v", c.name, got)
		}
	}
}

func TestDeriveRouteStatusDetails(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	zone := &Zone{Mode: "managed", VerifiedAt: now}
	expires := now.AddDate(0, 2, 0)
	route := Route{ID: "route", OperationID: "op_old", Status: "waiting", Input: RouteInput{Hostname: "*.acme.dev"}, Binding: RouteBinding{Backend: RouteBackendKubernetes},
		Verification: RouteVerification{DNS: true, TLS: true, CertExpires: expires, CheckedAt: now, BackendError: "no backend"}}
	got := DeriveRouteStatus(route, zone, &RouteOperation{ID: "op_new", Kind: "verify", Status: "failed", Message: "the check failed"}, now)
	want := RouteStatus{RouteID: "route", Hostname: "*.acme.dev", Wildcard: true, Backend: RouteBackendKubernetes, DNS: DomainReady, Certificate: DomainReady, Route: DomainFailed,
		Message: "the check failed", CertificateExpiresAt: expires, CheckedAt: now, OperationID: "op_new", Retryable: true}
	if got != want {
		t.Fatalf("got %+v", got)
	}
	got = DeriveRouteStatus(route, zone, nil, now)
	if got.Message != "no backend" || got.OperationID != "op_old" || got.Retryable {
		t.Fatalf("without an operation: %+v", got)
	}
	if (RouteBinding{}).BackendName() != RouteBackendServer || DeriveRouteStatus(Route{}, zone, nil, now).Backend != RouteBackendServer {
		t.Fatal("an empty backend must read as server")
	}
}
