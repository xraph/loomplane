// Package infrastructure is Loomplane's side of Ctrl Plane: the identity
// bridge Ctrl Plane authenticates and authorizes through, and the read
// model of what Ctrl Plane can deploy to.
//
// Ctrl Plane owns the lifecycle state of the instances, deployments and
// releases it manages. Loomplane calls its services in-process with claims
// built from the caller's principal (ClaimsFor), so Ctrl Plane's tenant is
// always the caller's organization.
package infrastructure

import (
	"context"
	"errors"
	"strings"
	"sync"

	cpauth "github.com/xraph/ctrlplane/auth"

	"github.com/xraph/loomplane/internal/security"
)

// SessionResolver turns a bearer token into the user and the organization
// the session acts in. The identity extension provides it from authsome.
type SessionResolver func(ctx context.Context, token string) (userID, orgID string, err error)

// Authorizer is the part of security.Authorizer the bridge needs.
type Authorizer interface {
	Require(ctx context.Context, p security.Principal, perm security.Permission, resourceID string) error
}

// AuthProvider is Ctrl Plane's auth.Provider, backed by authsome sessions
// and Warden roles instead of Ctrl Plane's development NoopProvider (which
// accepts any token as a system admin).
//
// Ctrl Plane takes its provider when its extension is built, before the
// identity extension has an Authorizer, so the two halves are bound later
// with Bind. Until then every call is refused.
type AuthProvider struct {
	mu      sync.RWMutex
	resolve SessionResolver
	authz   Authorizer
}

var _ cpauth.Provider = (*AuthProvider)(nil)

// NewAuthProvider returns an unbound provider.
func NewAuthProvider() *AuthProvider { return &AuthProvider{} }

// Bind supplies the session resolver and the authorizer.
func (a *AuthProvider) Bind(resolve SessionResolver, authz Authorizer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolve, a.authz = resolve, authz
}

func (a *AuthProvider) bound() (SessionResolver, Authorizer) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.resolve, a.authz
}

// Authenticate implements cpauth.Provider. Ctrl Plane's middleware passes
// the raw Authorization header, "Bearer " prefix included.
func (a *AuthProvider) Authenticate(ctx context.Context, token string) (*cpauth.Claims, error) {
	resolve, _ := a.bound()
	if resolve == nil {
		return nil, cpauth.ErrUnauthorized
	}
	token = strings.TrimSpace(token)
	if len(token) > len("bearer ") && strings.EqualFold(token[:len("bearer ")], "bearer ") {
		token = strings.TrimSpace(token[len("bearer "):])
	}
	if token == "" {
		return nil, cpauth.ErrUnauthorized
	}
	userID, orgID, err := resolve(ctx, token)
	if err != nil || userID == "" || orgID == "" {
		return nil, cpauth.ErrUnauthorized
	}
	// No session acts as Loomplane automation.
	if _, system := security.IsSystemSubject(userID); system {
		return nil, cpauth.ErrUnauthorized
	}
	return ClaimsFor(security.Principal{UserID: userID, OrgID: orgID}), nil
}

// Authorize implements cpauth.Provider by asking Warden: reading anything
// needs infrastructure:read, any other action infrastructure:modify. Claims
// naming the organization's Loomplane automation come only from
// WithPrincipal (Authenticate refuses them), so they're checked as it.
func (a *AuthProvider) Authorize(ctx context.Context, req cpauth.AuthzRequest) (bool, error) {
	_, authz := a.bound()
	if authz == nil {
		return false, nil
	}
	perm := security.InfrastructureModify
	switch strings.ToLower(req.Action) {
	case "read", "get", "list", "watch", "view":
		perm = security.InfrastructureRead
	}
	err := authz.Require(ctx, security.ActorPrincipal(req.TenantID, req.SubjectID), perm, req.ResourceID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, security.ErrForbidden), errors.Is(err, security.ErrUnauthenticated):
		return false, nil
	}
	return false, err
}

// GetTenantID implements cpauth.Provider.
func (a *AuthProvider) GetTenantID(ctx context.Context) string {
	if c := cpauth.ClaimsFrom(ctx); c != nil {
		return c.TenantID
	}
	return ""
}

// ClaimsFor is the Ctrl Plane identity of a Loomplane principal. It never
// carries system:admin: Ctrl Plane's tenant is the caller's organization.
func ClaimsFor(p security.Principal) *cpauth.Claims {
	return &cpauth.Claims{SubjectID: p.UserID, TenantID: p.OrgID}
}

// WithPrincipal returns ctx carrying p's Ctrl Plane claims, for calling Ctrl
// Plane's services on p's behalf.
func WithPrincipal(ctx context.Context, p security.Principal) context.Context {
	return cpauth.WithClaims(ctx, ClaimsFor(p))
}
