// Package security holds Loomplane's authorization model: who a request is
// for, which organization it acts in, and what each role may do there.
//
// Identity comes from authsome. An organization is an authsome
// organization, and the one a request acts in is the session's active org,
// which authsome only lets a member switch to. Permissions are Warden roles
// scoped to that organization as the Warden tenant, so Warden decides every
// check and the organization's members never share roles across orgs.
package security

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Errors a request can fail with before it reaches a handler.
var (
	ErrUnauthenticated = errors.New("sign in first")
	ErrNoOrganization  = errors.New("this session has no active organization; create or switch to one first")
	ErrOrgMismatch     = errors.New("this tab's organization and the session's organization differ; reload")
	ErrForbidden       = errors.New("you don't have permission to do that in this organization")
	ErrNotMember       = errors.New("you aren't a member of this session's organization any more; switch to another")
)

// HeaderOrganization is the header a dashboard tab sends with the
// organization it believes it's in. It never selects an organization; it
// only lets a stale tab find out it has drifted (409).
const HeaderOrganization = "X-Loomplane-Org"

// Principal is the caller of a request.
type Principal struct {
	UserID string
	OrgID  string
	// KeyID is the API key the request came with, if it came with one. A
	// key acts as the member who made it.
	KeyID string
	// ReadOnly limits the request to reading, whatever the member's role.
	ReadOnly bool
	// Kind is who the principal is: a person (the zero value) or Loomplane
	// automation. Only SystemPrincipal and ActorPrincipal set it.
	Kind Kind `json:",omitempty"`
}

type principalKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored in ctx.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Resolve decides the principal from the authenticated user, the session's
// active org and the tab's header, which may be empty.
func Resolve(userID, sessionOrg, header string) (Principal, error) {
	switch {
	case userID == "":
		return Principal{}, ErrUnauthenticated
	case strings.HasPrefix(userID, systemPrefix):
		// authsome never issues such an ID; refusing it anyway means no
		// session can ever act as Loomplane automation.
		return Principal{}, ErrUnauthenticated
	case sessionOrg == "":
		return Principal{}, ErrNoOrganization
	case header != "" && header != sessionOrg:
		return Principal{}, ErrOrgMismatch
	}
	return Principal{UserID: userID, OrgID: sessionOrg}, nil
}

// StatusFor maps an error from this package to its HTTP status.
func StatusFor(err error) int {
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, ErrNoOrganization), errors.Is(err, ErrForbidden), errors.Is(err, ErrNotMember):
		return http.StatusForbidden
	case errors.Is(err, ErrOrgMismatch):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}
