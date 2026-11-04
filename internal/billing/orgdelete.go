//go:build cloud

package billing

import (
	"context"

	authid "github.com/xraph/authsome/id"
	"github.com/xraph/authsome/plugin"
	"github.com/xraph/grove"
	"github.com/xraph/ledger"
)

// orgDeleteHook runs after authsome deletes a workspace. Identity registers
// it before authsome's subscription plugin, whose own hook cancels the
// workspace's subscriptions, because authsome runs those hooks in
// registration order and the overage has to be invoiced first.
type orgDeleteHook struct{}

var (
	_ plugin.Plugin         = orgDeleteHook{}
	_ plugin.AfterOrgDelete = orgDeleteHook{}
)

// NewOrgDeleteHook is an authsome plugin: after a workspace is deleted it
// invoices what the workspace owes, before authsome's subscription plugin
// cancels its subscriptions. For now it does nothing; registering it fixes
// its place ahead of the subscription plugin.
func NewOrgDeleteHook(*grove.DB, *ledger.Ledger, string) plugin.Plugin {
	return orgDeleteHook{}
}

// Name names the hook in authsome's plugin registry.
func (orgDeleteHook) Name() string { return "loomplane-billing-org-delete" }

// OnAfterOrgDelete does nothing yet.
func (orgDeleteHook) OnAfterOrgDelete(context.Context, authid.OrgID) error { return nil }
