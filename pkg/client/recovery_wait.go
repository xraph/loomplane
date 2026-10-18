package client

import "context"

// WaitServiceBackup follows the persisted backup until it succeeds or fails.
func (c *Client) WaitServiceBackup(ctx context.Context, id string) (*ServiceBackup, error) {
	return wait(ctx, func() (*ServiceBackup, bool, error) {
		b, err := c.ServiceBackup(ctx, id)
		if err != nil {
			return nil, false, err
		}
		return b, b.Status == "succeeded" || b.Status == "failed", nil
	})
}

// WaitServiceRestore follows a copy through verification or cleanup.
func (c *Client) WaitServiceRestore(ctx context.Context, id string) (*ServiceRestore, error) {
	return wait(ctx, func() (*ServiceRestore, bool, error) {
		r, err := c.ServiceRestore(ctx, id)
		if err != nil {
			return nil, false, err
		}
		return r, r.Status == "succeeded" || r.Status == "failed" || r.Status == "cleanup_failed" || r.Status == "deleted", nil
	})
}
