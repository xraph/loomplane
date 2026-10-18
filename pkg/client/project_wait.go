package client

import "context"

// WaitProjectReady waits for the project's environment setup to complete.
func (c *Client) WaitProjectReady(ctx context.Context, id string) (*Project, error) {
	return wait(ctx, func() (*Project, bool, error) {
		p, err := c.Project(ctx, id)
		if err != nil {
			return nil, false, err
		}
		return p, p.Status == "ready", nil
	})
}
