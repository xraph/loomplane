package catalogconnect

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/internal/security"
)

// Audited actions.
const (
	AuditCreate = "catalog_endpoint.create"
	AuditDelete = "catalog_endpoint.delete"
	AuditCheck  = "catalog_endpoint.check"
)

// Audited outcomes.
const (
	OutcomeSuccess = "success"
	// OutcomeFailure is a change that was refused, such as deleting an
	// endpoint in use, or a check that found a problem.
	OutcomeFailure = "failure"
	// OutcomeDenied is a principal without the permission.
	OutcomeDenied = "denied"
	// OutcomeInvalid is a request that couldn't be read, such as a DSN
	// Loomplane doesn't take.
	OutcomeInvalid = "invalid"
	// OutcomeRateLimited is a check refused for coming too often.
	OutcomeRateLimited = "rate_limited"
)

// Auditor records endpoint changes and checks. Recording must not fail
// the change, so it returns nothing; the implementation logs its own
// failures.
type Auditor interface {
	Record(ctx context.Context, e AuditEvent)
}

// AuditEvent is one audited change or check. It never holds a DSN, its
// password or its query string: an endpoint is named by its ID and its
// host:port.
type AuditEvent struct {
	Action    string
	Principal security.Principal
	// EndpointID is the endpoint created or deleted, once it has one.
	EndpointID string
	// Target is the connection a check dialed from.
	Target  string
	Engine  string
	Address string // host:port
	Outcome string
}

// RecordCheck audits a check of a DSN that isn't stored, as
// POST /catalog/endpoints/checks makes one.
func (s *Service) RecordCheck(ctx context.Context, p security.Principal, in CheckInput, outcome string) {
	s.record(ctx, AuditEvent{Action: AuditCheck, Principal: p, Target: in.Target, Engine: in.Engine, Address: dsnAddress(in.DSN), Outcome: outcome})
}

func (s *Service) record(ctx context.Context, e AuditEvent) {
	if s.Audit != nil {
		s.Audit.Record(ctx, e)
	}
}

// outcomeOf is the audited outcome of a change that returned err.
func outcomeOf(err error) string {
	switch {
	case err == nil:
		return OutcomeSuccess
	case errors.Is(err, security.ErrForbidden), errors.Is(err, security.ErrNotMember), errors.Is(err, security.ErrNoOrganization), errors.Is(err, security.ErrUnauthenticated):
		return OutcomeDenied
	case errors.Is(err, ErrInvalid):
		return OutcomeInvalid
	}
	return OutcomeFailure
}

// dsnAddress is the host:port a DSN names, with its scheme's port when it
// names none, or "" for a DSN that isn't a URL with a host. It never
// holds more of the DSN: no user, password, path or query. An @ where
// the parsed address has no user info, or in its path, query or
// fragment, means a password with an unescaped ?, / or # ended the
// address early, so what parsed as host:port may be user:password: it
// names nothing then.
func dsnAddress(dsn string) string {
	dsn = strings.TrimSpace(dsn)
	u, err := url.Parse(dsn)
	if err != nil || u.Opaque != "" || u.Hostname() == "" {
		return ""
	}
	if (u.User == nil && strings.Contains(dsn, "@")) || strings.Contains(u.Path+u.RawPath+u.RawQuery+u.Fragment+u.RawFragment, "@") {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "postgres", "postgresql":
			port = "5432"
		case "redis", "rediss":
			port = "6379"
		default:
			return ""
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}
