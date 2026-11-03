package realtime

import (
	"testing"

	"github.com/xraph/loomplane/internal/security"
)

// A node event belongs to the org, not a project, so it asks for nodes:read.
func TestNodeEventsNeedNodeRead(t *testing.T) {
	if got := permission("node.updated"); got != security.NodeRead {
		t.Fatalf("node.updated asks for %v, want nodes:read", got)
	}
}
