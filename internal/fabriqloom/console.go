package fabriqloom

import (
	"context"
	"net/http"

	"github.com/xraph/loomplane/internal/catalogproxy"
)

// ConsoleBasePath is the path fabriq-admin serves under. It is fixed at
// "/fabriq/" because the console is told its base path when it starts, and a
// proxy session's prefix changes per session. The proxy's URL rewriting
// turns "/fabriq/" into the session's prefix in index.html's <base href> and
// in config.js's basePath and apiUrl, and StripPrefix removes it again
// before a request reaches the console's nginx.
const ConsoleBasePath = "/fabriq/"

// AdminTarget opens the console through the admin proxy. The proxy supplies
// the resource's control token, and nginx adds its own operator key upstream.
// The console and its /admin/ API share the admin component's port.
var AdminTarget = catalogproxy.Target{
	Component: "admin", Port: "http", Root: ConsoleBasePath,
	AllowedPrefixes: []string{ConsoleBasePath}, StripPrefix: "/fabriq",
	Header: func(_ context.Context, read func(name string) ([]byte, error)) (http.Header, error) {
		credential, err := resolveControlCredential(read)
		if err != nil {
			return nil, err
		}
		header := http.Header{controlTokenHeader: nil}
		if credential.token != "" {
			header.Set(controlTokenHeader, credential.token)
		}
		return header, nil
	},
}

func init() { catalogproxy.Register("fabriq", AdminTarget) }
