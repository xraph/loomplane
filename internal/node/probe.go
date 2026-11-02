package node

import (
	"context"
	"encoding/json"
	"net"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// ProbeHandler replies with the JSON Inventory from Detect and returns, which
// closes the stream. opts is called per request so it sees the current config.
func ProbeHandler(opts func() DetectOptions) Handler {
	return func(ctx context.Context, _ nodeproto.StreamHeader, stream net.Conn) error {
		inv, err := Detect(ctx, opts())
		if err != nil {
			return err
		}
		return json.NewEncoder(stream).Encode(inv)
	}
}
