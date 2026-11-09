package agentauthz

import (
	"context"

	"github.com/xraph/cortex"
	"github.com/xraph/cortex/llm"
)

// ShieldClass is the risk class Layered tells shield a call has.
func (a *Authorizer) ShieldClass(ctx context.Context, s cortex.Subject, tc llm.ToolCall) string {
	return a.shieldClass(ctx, s, tc)
}
