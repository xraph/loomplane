package fabriqloom

import "net/url"

// DSN is what a consuming service gets as FABRIQ_URL:
// fabriq://<key>@<hostport>/<tenant>?tls=false. The tenant is the
// service's default; its key may use any tenant. Traffic stays on the
// composition's private network, hence tls=false.
func DSN(key, hostport, tenant string) string {
	u := url.URL{Scheme: "fabriq", User: url.User(key), Host: hostport, Path: "/" + tenant, RawQuery: "tls=false"}
	return u.String()
}
