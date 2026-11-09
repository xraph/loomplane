//go:build integration

package identity

import "github.com/xraph/forge/extensions/dashboard/contract"

// SetSetupOrgHook replaces how setup creates the first organization, so a
// test can make that step fail and watch setup roll back. Call it before
// the app starts. It exists only in integration builds.
func (e *Extension) SetSetupOrgHook(hook setupOrgHook) {
	e.createOrgHook = hook
}

// ConsoleRegistry is the dashboard's contract registry the console gate
// reads, nil before the dashboard starts. It exists only in integration
// builds, for the test that keeps the console's reads under review.
func (e *Extension) ConsoleRegistry() contract.Registry {
	return e.consoleRegistry()
}

// ConsoleOpens reports whether the console gate lets a platform admin send
// the registered intent in, as a query or, for a subscription, as one.
func (e *Extension) ConsoleOpens(contributor string, in contract.Intent) bool {
	kind := contract.KindQuery
	if in.Kind == contract.IntentKindSubscription {
		kind = contract.KindSubscribe
	}
	return consoleAllowed(e.consoleRegistry(), contract.Request{Kind: kind, Contributor: contributor, Intent: in.Name, IntentVersion: in.Version})
}
