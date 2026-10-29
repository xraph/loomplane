//go:build integration

package identity

// SetSetupOrgHook replaces how setup creates the first organization, so a
// test can make that step fail and watch setup roll back. Call it before
// the app starts. It exists only in integration builds.
func (e *Extension) SetSetupOrgHook(hook setupOrgHook) {
	e.createOrgHook = hook
}
