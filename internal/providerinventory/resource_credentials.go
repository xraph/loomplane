package providerinventory

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/clouds/vercel"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/internal/providerresources"
)

// ResolveResourceAccess returns only the selected store's existing application credentials.
// It never establishes a project connection or copies the account token into a workload.
func (c *Clients) ResolveResourceAccess(ctx context.Context, conn *connections.Connection, credential []byte, selected *providerresources.ProviderResource, projectID, environment string) (catalogconnect.APIAccess, error) {
	invalid := errors.New("provider could not verify application access for this resource and project environment")
	if c == nil || c.Vercel == nil || conn == nil || selected == nil || conn.Cloud != connections.Vercel || selected.Provider != connections.Vercel || selected.Kind != "vercel-integration" || projectID == "" || environment == "" || conn.OrgID != selected.OrgID || conn.ID != selected.ConnectionID || conn.Generation != selected.ConnectionGeneration {
		return catalogconnect.APIAccess{}, invalid
	}
	identity := connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)
	if identity == "" || identity != selected.AccountIdentity || conn.AccountIdentity != "" && conn.AccountIdentity != identity {
		return catalogconnect.APIAccess{}, invalid
	}
	tok := vercel.Token{Value: strings.TrimSpace(string(credential)), TeamID: conn.Details["team_id"]}
	who, err := c.Vercel.Check(ctx, tok)
	if err != nil {
		return catalogconnect.APIAccess{}, invalid
	}
	account := who.TeamID
	if account == "" {
		account = who.UserID
	}
	expected := conn.Details["team_id"]
	if expected == "" {
		expected = conn.Details["user_id"]
	}
	if account == "" || account != expected {
		return catalogconnect.APIAccess{}, invalid
	}
	credentials, err := c.Vercel.ReadStorageResourceCredentials(ctx, tok, selected.ExternalID, projectID, environment)
	if err != nil {
		return catalogconnect.APIAccess{}, invalid
	}
	resource, err := c.Vercel.GetStorageResource(ctx, tok, selected.ExternalID)
	if err != nil || resource.ID != selected.ExternalID || resource.Type != "integration" {
		return catalogconnect.APIAccess{}, invalid
	}
	scope := "teams/" + account + "/integrations/" + url.PathEscape(resource.Product.Integration.Slug) + "/" + url.PathEscape(resource.Product.Slug) + "/" + url.PathEscape(resource.ExternalResourceID)
	if scope != selected.Scope || selected.Incarnation != resource.ID {
		return catalogconnect.APIAccess{}, invalid
	}
	return catalogconnect.APIAccess{Provider: "vercel", Kind: "vercel-integration", AccountID: account, ResourceID: resource.ID, Name: resource.Name, ProjectID: projectID, Environment: environment, Values: credentials.Values}, nil
}
