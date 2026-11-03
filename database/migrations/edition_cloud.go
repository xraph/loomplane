//go:build cloud

package migrations

import "github.com/xraph/grove/migrate"

// editionGroups are the migration groups only this edition runs. The
// billing groups arrive with the billing extension.
func editionGroups() []*migrate.Group { return nil }
