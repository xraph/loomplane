package providerresources

import (
	"context"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/security"
)

func apiResourceKind(r *ProviderResource) string {
	if r.Provider == connections.Cloudflare && r.Kind == "object-storage" {
		return "cloudflare-r2"
	}
	if catalogconnect.IsAPIEngine(r.Kind) {
		return r.Kind
	}
	return ""
}

// API account IDs are public, but must still match the connection's verified fingerprint.
func apiAccountID(r *ProviderResource) string {
	if r == nil || apiResourceKind(r) == "" {
		return ""
	}
	parts := strings.Split(r.Scope, "/")
	if len(parts) < 3 || parts[1] == "" {
		return ""
	}
	id := parts[1]
	switch r.Provider {
	case connections.Cloudflare:
		if parts[0] == "accounts" && connections.AccountIdentity(connections.Cloudflare, "", map[string]string{"account_id": id}) == r.AccountIdentity {
			return id
		}
	case connections.Vercel:
		if parts[0] == "teams" && (connections.AccountIdentity(connections.Vercel, "", map[string]string{"team_id": id}) == r.AccountIdentity || connections.AccountIdentity(connections.Vercel, "", map[string]string{"user_id": id}) == r.AccountIdentity) {
			return id
		}
	}
	return ""
}
func (s *Service) decorateAPI(r *ProviderResource) {
	r.APIAccountID = ""
	r.AttachmentNote = ""
	kind := apiResourceKind(r)
	if kind == "" {
		return
	}
	r.CredentialMethods = []string{}
	r.APIAccountID = apiAccountID(r)
	if r.APIAccountID == "" || !AttachableIdentity(r) || r.Status == "missing" || s.deps.Endpoints == nil {
		return
	}
	switch {
	case r.Provider == connections.Cloudflare:
		r.CredentialMethods = append(r.CredentialMethods, "native_binding")
		r.AttachmentNote = "Native bindings require a Cloudflare Worker in the same verified account. Other targets need supported application API credentials."
		switch kind {
		case "cloudflare-hyperdrive", "cloudflare-durable-object":
			if r.EndpointID == "" {
				r.ConnectionState = "native_binding_required"
			}
			r.AttachmentNote = "This resource requires a native Cloudflare Workers binding in the same verified account."
		case "cloudflare-r2":
			r.CredentialMethods = append(r.CredentialMethods, "bucket_access")
		default:
			r.CredentialMethods = append(r.CredentialMethods, "api_access")
		}
	case kind != "vercel-integration":
		r.CredentialMethods = []string{"api_access"}
	default:
		r.AttachmentNote = "Use the Marketplace integration's approved application connection values. The deployment review preserves the backing provider and product identity."
		if _, ok := s.deps.Reader.(resourceAccessResolver); ok {
			r.CredentialMethods = []string{"provider_credentials"}
		}
	}
}
func normalizeAPIAccess(r *ProviderResource, a catalogconnect.APIAccess) (string, error) {
	kind := apiResourceKind(r)
	accountID := apiAccountID(r)
	if kind == "" || accountID == "" || !AttachableIdentity(r) {
		return "", ErrInvalid
	}
	if a.Provider == "" {
		a.Provider = r.Provider
	}
	if a.Kind == "" {
		a.Kind = kind
	}
	if a.AccountID == "" {
		a.AccountID = accountID
	}
	if a.ResourceID == "" {
		a.ResourceID = r.ExternalID
	}
	if a.Provider != r.Provider || a.Kind != kind || a.AccountID != accountID || a.ResourceID != r.ExternalID {
		return "", ErrInvalid
	}
	if a.Incarnation != "" && a.Incarnation != r.Incarnation {
		return "", ErrInvalid
	}
	a.Incarnation = r.Incarnation
	if a.Name != "" && a.Name != r.Name {
		return "", ErrInvalid
	}
	a.Name = r.Name
	parts := strings.Split(r.Scope, "/")
	if kind == "cloudflare-r2" {
		if len(parts) != 4 || parts[2] != "r2" {
			return "", ErrInvalid
		}
		if a.Jurisdiction != "" && a.Jurisdiction != parts[3] {
			return "", ErrInvalid
		}
		a.Jurisdiction = parts[3]
	}
	if kind == "cloudflare-durable-object" {
		if len(parts) != 7 || parts[2] != "durable-objects" || parts[3] != "scripts" || parts[5] != "classes" {
			return "", ErrInvalid
		}
		if a.ScriptName != "" && a.ScriptName != parts[4] || a.ClassName != "" && a.ClassName != parts[6] {
			return "", ErrInvalid
		}
		a.ScriptName, a.ClassName = parts[4], parts[6]
	} else if a.ScriptName != "" || a.ClassName != "" {
		return "", ErrInvalid
	}
	raw, err := catalogconnect.EncodeAPIAccess(a)
	if err != nil {
		return "", ErrInvalid
	}
	return raw, nil
}
func apiAccessMatches(r *ProviderResource, a catalogconnect.APIAccess) bool {
	raw, err := normalizeAPIAccess(r, a)
	if err != nil {
		return false
	}
	canonical, err := catalogconnect.ParseAPIAccess(raw)
	return err == nil && a.Name == canonical.Name && a.Jurisdiction == canonical.Jurisdiction && a.ClassName == canonical.ClassName && a.ScriptName == canonical.ScriptName && a.Incarnation == canonical.Incarnation
}
func apiEndpointMatches(r *ProviderResource, e *catalogconnect.Endpoint) bool {
	if e == nil {
		return false
	}
	bridgeMatches := r.Kind == "vercel-integration" && catalogconnect.APIEndpointBridge(e) != "" && len(catalogconnect.APIEndpointOutputs(e)) > 0
	usernameMatches := e.Username == "" || e.Username == "native-binding" || bridgeMatches
	if e.OrgID != r.OrgID || e.Engine != apiResourceKind(r) || e.Database != r.ExternalID || e.Port != 443 || e.TLS != catalogconnect.TLSVerifyFull || !usernameMatches || apiAccountID(r) == "" || !AttachableIdentity(r) {
		return false
	}

	host := "api.cloudflare.com"
	if r.Provider == connections.Vercel {
		switch r.Kind {
		case "vercel-blob":
			host = "vercel.com"
		case "vercel-global-config":
			host = "global-config.vercel.com"
		default:
			host = "api.vercel.com"
		}
	}
	return e.Host == host || r.Kind == "vercel-global-config" && e.Host == "edge-config.vercel.com"
}

type resourceAccessResolver interface {
	ResolveResourceAccess(context.Context, *connections.Connection, []byte, *ProviderResource, string, string) (catalogconnect.APIAccess, error)
}

func (s *Service) providerResourceAccess(ctx context.Context, p security.Principal, r *ProviderResource, projectID, environment string) (catalogconnect.APIAccess, error) {
	resolver, ok := s.deps.Reader.(resourceAccessResolver)
	if !ok || r.Kind != "vercel-integration" || projectID == "" || environment == "" {
		return catalogconnect.APIAccess{}, ErrInvalid
	}
	conn, credential, err := s.deps.Connections.Credential(ctx, p.OrgID, r.ConnectionID)
	if err != nil {
		return catalogconnect.APIAccess{}, ErrUnverified
	}
	defer clear(credential)
	if conn == nil || conn.OrgID != r.OrgID || conn.Generation != r.ConnectionGeneration || account(conn) != r.AccountIdentity || conn.Status != connections.StatusReady {
		return catalogconnect.APIAccess{}, ErrChanged
	}
	access, err := resolver.ResolveResourceAccess(ctx, conn, credential, r, projectID, environment)
	if err != nil {
		return catalogconnect.APIAccess{}, ErrUnverified
	}
	if err := s.currentConnection(ctx, p, conn); err != nil {
		return catalogconnect.APIAccess{}, err
	}
	if err := s.require(ctx, p, security.SecretManage); err != nil {
		return catalogconnect.APIAccess{}, err
	}
	if access.Provider != "vercel" || access.Kind != "vercel-integration" || access.AccountID != apiAccountID(r) || access.ResourceID != r.ExternalID || access.Native || access.Token != "" || access.ProjectID != projectID || access.Environment != environment {
		return catalogconnect.APIAccess{}, ErrInvalid
	}
	return access, nil
}
