package identity

import "github.com/xraph/forge/extensions/dashboard/contract"

// coreContributor is the Forge dashboard's own contributor. Its intents only
// read: health, metrics, traces, the dashboard's audit log.
const coreContributor = "core-contract"

type consoleIntent struct{ contributor, intent string }

// consoleIntents is everything the console may call outside the core
// contributor, with the kind each is called as. It's a positive list:
// authsome registers every intent it has with no principal check (apps.delete
// would delete Loomplane's own app), so an intent a later authsome release
// adds stays blocked until someone adds it here.
var consoleIntents = func() map[consoleIntent]contract.Kind {
	out := map[consoleIntent]contract.Kind{}
	add := func(contributor string, kind contract.Kind, intents ...string) {
		for _, in := range intents {
			out[consoleIntent{contributor, in}] = kind
		}
	}
	add("authsome", contract.KindQuery,
		"apps.context", "apps.detail", "apps.list",
		"auth.config", "auth.featureToggles",
		"credentials.detail",
		"devices.detail", "devices.list",
		"environments.detail", "environments.list",
		"formConfigs.list", "formConfigs.signup",
		"overview.recentSignups", "overview.stats",
		"plugins.list",
		"roles.detail", "roles.list",
		"sessions.detail", "sessions.list",
		"settings.detail", "settings.list", "settings.namespace", "settings.namespaces",
		"users.detail", "users.list",
	)
	add("authsome", contract.KindCommand,
		"auth.logout",
		"users.ban", "users.unban",
		"sessions.revoke", "sessions.bulkRevoke",
	)
	add("waitlist", contract.KindQuery, "waitlist.list", "waitlist.detail", "waitlist.counts")
	add("waitlist", contract.KindCommand, "waitlist.approve", "waitlist.reject", "waitlist.delete")
	return out
}()

// consoleAllowed reports whether the console may send req. The core
// contributor's reads (queries and subscriptions) are all allowed; anything
// else must be on consoleIntents under the kind it's listed with.
func consoleAllowed(req contract.Request) bool {
	if req.Contributor == coreContributor {
		return req.Kind == contract.KindQuery || req.Kind == contract.KindSubscribe
	}
	kind, ok := consoleIntents[consoleIntent{req.Contributor, req.Intent}]
	return ok && req.Kind == kind
}
