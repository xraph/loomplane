package client

import (
	"context"
	"net/http"
)

// ChooseEnvironmentConnection selects the connected cloud account an
// environment deploys with. An empty connection clears the explicit choice.
func (c *Client) ChooseEnvironmentConnection(ctx context.Context, project, environment, cloud, connection string) error {
	return c.do(ctx, http.MethodPut, "/api/v1/projects/"+esc(project)+"/environments/"+esc(environment)+"/connections/"+esc(cloud), map[string]string{"connection_id": connection}, nil)
}
