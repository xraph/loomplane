//go:build !cloud

package migrations

import "github.com/xraph/grove/migrate"

// editionGroups are the migration groups only this edition runs. The public
// build has none.
func editionGroups() []*migrate.Group { return nil }
