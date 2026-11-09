//go:build !cloud

package migrations

// editionWantGroups are the groups only this edition adds after the
// libraries' groups. The public build adds none.
func editionWantGroups() []string { return nil }
