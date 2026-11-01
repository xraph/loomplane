package server

import (
	"context"
	"io"
	"time"

	"github.com/xraph/loomplane/internal/clouds/owned"
	"github.com/xraph/loomplane/internal/engineprobe"
)

// catalogReady runs only a registered checker, discarding all process output.
func catalogReady(ctx context.Context, c *Client, container string, labels map[string]string) error {
	args, err := engineprobe.Command(labels[owned.LabelCatalogKind], labels[owned.LabelHealthChecker])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return ExecOutput(ctx, c, container, args, io.Discard)
}
