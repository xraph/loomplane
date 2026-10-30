package identity

import "github.com/xraph/forge/extensions/dashboard/contract"

// coreContributor is the Forge dashboard's own contributor. Its intents only
// read: health, metrics, traces, the dashboard's audit log.
const coreContributor = "core-contract"

type consoleIntent struct{ contributor, intent string }

// consoleCommands is every command the console may send. It's a positive
// list: authsome registers every intent it has with no principal check
// (apps.delete would delete Loomplane's own app), and the libraries' commands
// write to engines Loomplane runs, so a command a later release adds stays
// blocked until someone adds it here.
var consoleCommands = func() map[consoleIntent]bool {
	out := map[consoleIntent]bool{}
	add := func(contributor string, intents ...string) {
		for _, in := range intents {
			out[consoleIntent{contributor, in}] = true
		}
	}
	add("authsome",
		"auth.logout",
		"users.ban", "users.unban",
		"sessions.revoke", "sessions.bulkRevoke",
	)
	add("waitlist", "waitlist.approve", "waitlist.reject", "waitlist.delete")
	return out
}()

// consoleBlockedQueries are the reads the console never sends, though every
// other read of every registered contributor is open. Each says why.
var consoleBlockedQueries = map[consoleIntent]string{
	// authsome's own setup and sign-up pages read these. Loomplane serves
	// its own pages for both, and the console has no use for either.
	{"authsome", "auth.setupStatus"}:   "authsome's hosted setup page",
	{"authsome", "auth.dynamicConfig"}: "authsome's hosted sign-up page",
	// These hand out a link that downloads stored content: Trove holds
	// database backups and build artifacts, and a link works for whoever
	// holds it. Opening them is the owner's call.
	{"trove", "objects.contentUrl"}:   "a download link to any stored object",
	{"dispatch", "artifacts.presign"}: "a download link to a job's artifact",
}

// consoleAllowed reports whether the console may send req. reg is the
// dashboard's contract registry, nil until the dashboard hands it over.
//
// A query is allowed when reg has it as a read under the version the
// dashboard will resolve, for any contributor registered there, unless it's
// on consoleBlockedQueries. Subscriptions are the core contributor's alone,
// and a command must be on consoleCommands. Forge's own `requires` rules and
// each library's authorizer still run after this.
func consoleAllowed(reg contract.Registry, req contract.Request) bool {
	key := consoleIntent{req.Contributor, req.Intent}
	switch req.Kind {
	case contract.KindQuery:
		if _, blocked := consoleBlockedQueries[key]; blocked {
			return false
		}
		return registeredRead(reg, req)
	case contract.KindSubscribe:
		return req.Contributor == coreContributor
	case contract.KindCommand:
		return consoleCommands[key]
	}
	return false
}

// registeredRead reports whether reg has req's intent as a read, at the
// version the dashboard's transport will run: the one asked for, or the
// highest when none is. A command sent as a query is no read.
func registeredRead(reg contract.Registry, req contract.Request) bool {
	if reg == nil || req.Contributor == "" || req.Intent == "" {
		return false
	}
	version := req.IntentVersion
	if version == 0 {
		v, ok := reg.HighestVersion(req.Contributor, req.Intent)
		if !ok {
			return false
		}
		version = v
	}
	in, ok := reg.Intent(req.Contributor, req.Intent, version)
	return ok && in.Kind == contract.IntentKindQuery && in.Capability == contract.CapRead
}
