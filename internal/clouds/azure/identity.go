package azure

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// AcrPull is the built-in role that lets a principal pull from a
// registry.
const AcrPull = "7f951dda-4ed3-4680-a7ca-43fe172d538d"

// PullIdentity is the user-assigned managed identity that container apps
// pull images with, one per resource group.
const PullIdentity = "loomplane-pull"

// How a connection's container apps pull from the registry: with the
// managed identity, or with the registry's admin user.
const (
	PullModeIdentity = "identity"
	PullModeAdmin    = "admin"
)

// API versions.
const (
	identityVersion       = "2023-01-31"
	roleAssignmentVersion = "2022-04-01"
)

// pullActions are what pulling with an identity takes beyond Contributor:
// making the identity, handing it to an app, and giving it AcrPull.
var pullActions = []string{
	"Microsoft.Authorization/roleAssignments/write",
	"Microsoft.ManagedIdentity/userAssignedIdentities/write",
	"Microsoft.ManagedIdentity/userAssignedIdentities/assign/action",
}

// PullMode reports how container apps will pull images: PullModeIdentity
// when the principal may assign roles, in the resource group or in the
// subscription while the group isn't there, and PullModeAdmin when it
// may not. A Contributor may not, which is why this is a capability and
// never something a connection is missing.
func (s *Session) PullMode(ctx context.Context) (string, error) {
	scope, _, err := s.checkScope(ctx)
	if err != nil {
		return "", err
	}
	perms, err := s.permissionsOrNone(ctx, scope)
	if err != nil {
		return "", err
	}
	for _, action := range pullActions {
		if !allowed(perms, action) {
			return PullModeAdmin, nil
		}
	}
	return PullModeIdentity, nil
}

// Identity is a user-assigned managed identity.
type Identity struct {
	// ID is what an app names it by, and PrincipalID is what a role is
	// assigned to.
	ID, PrincipalID string
}

func (s *Session) pullIdentityPath() string {
	return s.group() + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/" + PullIdentity
}

// EnsurePullIdentity makes the resource group's pull identity in
// location, unless it's there.
func (s *Session) EnsurePullIdentity(ctx context.Context, location string) (*Identity, error) {
	var out struct {
		ID         string `json:"id"`
		Properties struct {
			PrincipalID string `json:"principalId"`
		} `json:"properties"`
	}
	_, err := s.do(ctx, http.MethodGet, s.pullIdentityPath(), identityVersion, nil, &out)
	if IsNotFound(err) {
		if _, perr := s.do(ctx, http.MethodPut, s.pullIdentityPath(), identityVersion, map[string]any{"location": location}, &out); perr != nil {
			return nil, fmt.Errorf("make the pull identity: %w", perr)
		}
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if out.ID == "" || out.Properties.PrincipalID == "" {
		return nil, errors.New("azure made the pull identity without a principal to give a role")
	}
	return &Identity{ID: out.ID, PrincipalID: out.Properties.PrincipalID}, nil
}

// RoleAssignmentName is the name of the assignment of a role to
// principalID at scope. Azure wants a GUID the caller picks; deriving it
// from both makes a second grant the same assignment. It's a version 5
// UUID's shape, made from a SHA-256 hash.
func RoleAssignmentName(scope, principalID string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(scope + "/" + principalID)))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// GrantPull gives principalID the AcrPull role on the registry, and
// reports whether it made the assignment: it's already there when the
// answer is false.
func (s *Session) GrantPull(ctx context.Context, registryID, principalID string) (created bool, err error) {
	body := map[string]any{"properties": map[string]any{
		"roleDefinitionId": s.subscription() + "/providers/Microsoft.Authorization/roleDefinitions/" + AcrPull,
		"principalId":      principalID,
		"principalType":    "ServicePrincipal",
	}}
	path := registryID + "/providers/Microsoft.Authorization/roleAssignments/" + RoleAssignmentName(registryID, principalID)
	_, err = s.do(ctx, http.MethodPut, path, roleAssignmentVersion, body, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusConflict && strings.EqualFold(ae.Code, "RoleAssignmentExists") {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("let the pull identity pull from the container registry: %w", err)
	}
	return true, nil
}
