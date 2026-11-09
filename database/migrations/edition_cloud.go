//go:build cloud

package migrations

import (
	"github.com/xraph/grove/migrate"
	ledgerpg "github.com/xraph/ledger/store/postgres"
)

// editionGroups are the migration groups only this edition runs: ledger's
// store, then Loomplane's billing tables, which follow it.
func editionGroups() []*migrate.Group {
	return []*migrate.Group{ledgerpg.Migrations, billingGroup()}
}
