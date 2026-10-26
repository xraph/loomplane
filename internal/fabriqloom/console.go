package fabriqloom

import "github.com/xraph/loomplane/internal/catalogproxy"

// ConsoleBasePath is the path fabriq-admin serves under. It is fixed at
// "/fabriq/" because the console is told its base path when it starts, and a
// proxy session's prefix changes per session. The proxy's URL rewriting
// turns "/fabriq/" into the session's prefix in index.html's <base href> and
// in config.js's basePath and apiUrl, and StripPrefix removes it again
// before a request reaches the console's nginx.
const ConsoleBasePath = "/fabriq/"

// AdminTarget opens the console through the admin proxy. It sends no
// credential: fabriq-admin's nginx adds the operator key itself, from
// FABRIQ_ADMIN_UPSTREAM_KEY, and the proxy drops any Authorization header the
// browser sends. The admin component's port serves nothing but the console
// and its /admin/ API, which is why StripPrefix may be a whole allowed prefix.
var AdminTarget = catalogproxy.Target{
	Component: "admin", Port: "http", Root: ConsoleBasePath,
	AllowedPrefixes: []string{ConsoleBasePath}, StripPrefix: "/fabriq",
}

func init() { catalogproxy.Register("fabriq", AdminTarget) }
