package providerinventory

import (
	"context"
	"errors"

	"github.com/xraph/loomplane/internal/clouds/render"
	"github.com/xraph/loomplane/internal/connections"
	"github.com/xraph/loomplane/pkg/provider"
)

// SupportsEndpointVerification reports installed readers that can prove a
// hostname absent from ordinary secret-free discovery metadata.
func (c *Clients) SupportsEndpointVerification(cloud string) bool {
	return c != nil && c.Render != nil && cloud == connections.Render
}

// VerifyEndpoint is reserved for authorized attachment checks. Render's native
// connection strings stay inside its client and are never returned to callers.
func (c *Clients) VerifyEndpoint(ctx context.Context, conn *connections.Connection, credential []byte, selected provider.ResourceObservation, host string, port int) (provider.ResourceObservation, error) {
	invalid := errors.New("provider could not verify the selected resource endpoint")
	if conn == nil || !c.SupportsEndpointVerification(conn.Cloud) || render.CheckKey(string(credential)) != nil {
		return provider.ResourceObservation{}, invalid
	}
	identity := connections.AccountIdentity(conn.Cloud, conn.Account, conn.Details)
	if identity == "" || conn.AccountIdentity != "" && conn.AccountIdentity != identity {
		return provider.ResourceObservation{}, invalid
	}
	return c.Render.VerifyResourceEndpoint(ctx, render.Key{Value: string(credential), OwnerID: conn.Details["owner_id"]}, selected, host, port)
}
