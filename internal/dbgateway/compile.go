package dbgateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

const (
	// scramIterations is the PBKDF2 work factor of every route verifier.
	scramIterations = 4096
	// idleTimeout is how long a gateway keeps an unused backend connection.
	idleTimeout = 5 * time.Minute
	// defaultPort is Postgres's, for an address that doesn't name one.
	defaultPort = 5432

	noAddress = "no address stored yet"
)

// Skipped is a route left out of a snapshot, and why. The reason never holds
// the address or its password.
type Skipped struct {
	Resource string
	Reason   string
}

// backend is what a route's stored address says about reaching it.
type backend struct {
	host, database, sslmode, user, password string
	port                                    int
}

// Compile builds g's routing table from its routes and the addresses of
// their resources, keyed by resource name. Each route's backend comes from
// its address, its credential is sealed to the gateway, and its one verifier
// takes the backend's own user and password, so a client logs in to the
// gateway with what it would have used on the database. A route Compile
// can't serve is left out and listed in the result. The snapshot has no
// version, issue time or signature: the service adds those.
func Compile(g Gateway, routes []RouteRecord, urls map[string]string) (plane.Snapshot, []Skipped, error) {
	return compile(g, routes, urls, deriveVerifier)
}

// verifierFunc returns route r's verifier for the backend's user and
// password: plane.NewVerifier over r's salt with scramIterations. The
// service passes one that caches.
type verifierFunc func(r RouteRecord, user, password string) plane.Verifier

func deriveVerifier(r RouteRecord, user, password string) plane.Verifier {
	return plane.NewVerifier(user, password, r.Salt, scramIterations)
}

func compile(g Gateway, routes []RouteRecord, urls map[string]string, verify verifierFunc) (plane.Snapshot, []Skipped, error) {
	s := plane.Snapshot{GatewayID: g.ID, Routes: []plane.Route{}}
	var skipped []Skipped
	for _, r := range routes {
		raw, ok := urls[r.Resource]
		if !ok {
			skipped = append(skipped, Skipped{Resource: r.Resource, Reason: noAddress})
			continue
		}
		rt, reason, err := compileRoute(g, r, raw, verify)
		if err != nil {
			return plane.Snapshot{}, nil, err
		}
		if reason != "" {
			skipped = append(skipped, Skipped{Resource: r.Resource, Reason: reason})
			continue
		}
		s.Routes = append(s.Routes, rt)
	}
	slices.SortFunc(s.Routes, func(a, b plane.Route) int { return strings.Compare(a.ID, b.ID) })
	return s, skipped, nil
}

// compileRoute returns the route, or the reason it is skipped. Only sealing
// fails with an error.
func compileRoute(g Gateway, r RouteRecord, raw string, verify verifierFunc) (rt plane.Route, reason string, err error) {
	label, err := plane.ParseLabel(r.Resource)
	if err != nil || label.Branch != "" {
		return plane.Route{}, "its name can't be a route label: lowercase letters, digits and single hyphens, starting with a letter", nil
	}
	state := plane.RouteState(r.State)
	if state != plane.RouteStateActive && state != plane.RouteStatePaused {
		return plane.Route{}, "its route is in an unknown state", nil
	}
	b, reason := parseAddress(raw)
	if reason != "" {
		return plane.Route{}, reason, nil
	}
	// This exact shape is what the router opens.
	secret, err := json.Marshal(struct { //nolint:gosec // G117: marshaled only to be sealed to the gateway, never stored or logged
		User     string `json:"user"`
		Password string `json:"password"`
	}{b.user, b.password})
	if err != nil {
		return plane.Route{}, "", errors.New("encode a backend credential")
	}
	sealed, err := plane.Seal(g.SealingPublicKey, secret, []byte("backend-credential:"+r.ID))
	clear(secret)
	if err != nil {
		return plane.Route{}, "", fmt.Errorf("seal the credential of route %s: %w", r.ID, err)
	}
	return plane.Route{
		ID:    r.ID,
		Label: label,
		State: state,
		Backend: plane.Backend{
			Kind: "postgres", Host: b.host, Port: b.port, Database: b.database,
			SNI: b.host, SSLMode: b.sslmode, Credential: sealed,
		},
		Verifiers:   []plane.Verifier{verify(r, b.user, b.password)},
		Params:      map[string]string{},
		IdleTimeout: idleTimeout,
	}, "", nil
}

// parseAddress reads a postgres:// or postgresql:// address. It returns a
// reason instead when the gateway can't reach the database that way. The
// reasons are fixed strings: url.Parse errors quote the address, so none of
// them is passed on.
func parseAddress(raw string) (b backend, reason string) {
	u, err := url.Parse(raw)
	if err != nil {
		return backend{}, "its stored address isn't a URL"
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return backend{}, "its stored address isn't a Postgres URL"
	}
	q := u.Query()
	if strings.Contains(u.Host, ",") {
		return backend{}, "its stored address names several hosts"
	}
	host := u.Hostname()
	if h := q.Get("host"); h != "" {
		host = h
	}
	switch {
	case strings.HasPrefix(host, "/"):
		return backend{}, "its stored address is a Unix socket"
	case strings.Contains(host, ","):
		return backend{}, "its stored address names several hosts"
	case host == "":
		return backend{}, "its stored address has no host"
	}
	port := defaultPort
	if p := u.Port(); p != "" {
		n, perr := strconv.Atoi(p)
		if perr != nil || n < 1 || n > 65535 {
			return backend{}, "its stored address has a bad port"
		}
		port = n
	}
	database := strings.TrimPrefix(u.Path, "/")
	if database == "" {
		return backend{}, "its stored address names no database"
	}
	sslmode := q.Get("sslmode")
	switch sslmode {
	case "":
		sslmode = "require"
	case "disable", "require", "verify-full":
	default:
		return backend{}, "its stored address asks for an sslmode other than disable, require or verify-full"
	}
	if u.User == nil || u.User.Username() == "" {
		return backend{}, "its stored address has no user"
	}
	password, ok := u.User.Password()
	if !ok || password == "" {
		// A route's clients log in with the backend's password, so an
		// address without one would open the route to an empty password.
		return backend{}, "its stored address has no password"
	}
	return backend{host: host, port: port, database: database, sslmode: sslmode, user: u.User.Username(), password: password}, ""
}
