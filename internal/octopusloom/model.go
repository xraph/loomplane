// Package octopusloom keeps what Loomplane knows about the Octopus gateways
// it runs: which gateway each environment has, the manual routes people add
// to it, and how far the last sync to Octopus got. The package holds the
// model, the validation a route goes through and the Postgres store. The
// config renderer, the admin API syncer and the HTTP API build on these types.
package octopusloom

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Providers a gateway runs on.
const (
	ProviderServer     = "server"
	ProviderKubernetes = "kubernetes"
)

// GatewayRef names a gateway: the Octopus resource of one environment.
type GatewayRef struct {
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	Resource      string `json:"resource"`
}

// Gateway is the desired and last-synced state of one Octopus gateway.
type Gateway struct {
	GatewayRef
	Provider  string `json:"provider"` // "server" | "kubernetes"
	Namespace string `json:"namespace,omitempty"`
	// FarpHostnames empty means FARP routes answer on any host.
	FarpHostnames     []string  `json:"farp_hostnames"`
	Generation        int64     `json:"generation"` // bumped by every desired change
	AppliedGeneration int64     `json:"applied_generation"`
	State             SyncState `json:"state"`
	Revision          int64     `json:"revision"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// SyncState is what the syncer saw the last time it ran.
type SyncState struct {
	StartTime      string                `json:"start_time,omitempty"` // Octopus system/info start_time last seen
	SyncedAt       time.Time             `json:"synced_at"`
	Error          string                `json:"error,omitempty"`
	OwnedUpstreams []string              `json:"owned_upstreams,omitempty"`
	RouteErrors    map[string]string     `json:"route_errors,omitempty"`  // manual route id -> message
	Registrations  map[string]Registered `json:"registrations,omitempty"` // service -> state (Docker only)
}

// Registered is one service's FARP registration on a Docker gateway.
type Registered struct {
	InstanceID   string    `json:"instance_id"` // "lp-svc-<service>"
	Revision     string    `json:"revision"`    // service binding revision last registered
	RegisteredAt time.Time `json:"registered_at"`
	HeartbeatAt  time.Time `json:"heartbeat_at"`
	Error        string    `json:"error,omitempty"`
}

// Route is a manual route: a hostname and path sent to one service port.
type Route struct {
	ID string `json:"id" forge:"id"` // typeid prefix "octoroute"
	GatewayRef
	Hostname    string    `json:"hostname"` // "" is any host
	Path        string    `json:"path"`     // "/", "/x", "/*", "/x/*"
	Methods     []string  `json:"methods"`  // empty is any
	Service     string    `json:"service"`
	Port        int       `json:"port"`
	StripPrefix bool      `json:"strip_prefix"`
	Note        string    `json:"note,omitempty"`
	Revision    int64     `json:"revision"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// RouteInput is what a person submits to create or change a route.
type RouteInput struct {
	Hostname    string   `json:"hostname"`
	Path        string   `json:"path"`
	Methods     []string `json:"methods"`
	Service     string   `json:"service"`
	Port        int      `json:"port"`
	StripPrefix bool     `json:"strip_prefix"`
	Note        string   `json:"note,omitempty"`
	Revision    int64    `json:"revision,omitempty"` // required on update
}

const (
	maxPathLength = 512
	maxNoteLength = 120
	wildcardTail  = "/*"
)

// methodOrder is the order Methods come back in.
var methodOrder = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

var (
	hostLabel    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	serviceLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

// NormalizeRouteInput validates in and returns it in the form the store
// keeps: a lower-case hostname, a checked path, methods upper-cased,
// de-duplicated and in a fixed order (never nil), and StripPrefix cleared on
// the paths where there is no prefix to strip. Errors are ErrInvalid with a
// message for the person who submitted the route.
func NormalizeRouteInput(in RouteInput) (RouteInput, error) {
	out := in
	out.Hostname = strings.ToLower(strings.TrimSpace(in.Hostname))
	if err := checkHostname(out.Hostname); err != nil {
		return RouteInput{}, err
	}
	if err := checkPath(in.Path); err != nil {
		return RouteInput{}, err
	}
	methods, err := normalizeMethods(in.Methods)
	if err != nil {
		return RouteInput{}, err
	}
	out.Methods = methods
	out.Service = strings.TrimSpace(in.Service)
	if out.Service == "" {
		return RouteInput{}, invalidf("Pick the service this route sends traffic to.")
	}
	if len(out.Service) > 63 || !serviceLabel.MatchString(out.Service) {
		return RouteInput{}, invalidf("%q isn't a service name. Use lower-case letters, digits and hyphens.", out.Service)
	}
	if in.Port < 1 || in.Port > 65535 {
		return RouteInput{}, invalidf("The port must be between 1 and 65535.")
	}
	if utf8.RuneCountInString(in.Note) > maxNoteLength {
		return RouteInput{}, invalidf("Keep the note to %d characters or fewer.", maxNoteLength)
	}
	if strings.ContainsAny(in.Note, "\r\n") {
		return RouteInput{}, invalidf("A note is a single line.")
	}
	if in.Path == "/" || in.Path == "/*" {
		out.StripPrefix = false
	}
	return out, nil
}

// checkHostname accepts "" (any host), an exact hostname, or a wildcard
// "*.zone" with a single leading "*." and at least two labels after it.
func checkHostname(h string) error {
	if h == "" {
		return nil
	}
	bad := invalidf("%q isn't a hostname. Use a name like api.acme.dev or *.acme.dev, without a scheme, port or path.", h)
	if len(h) > 253 {
		return bad
	}
	name, wildcard := strings.CutPrefix(h, "*.")
	labels := strings.Split(name, ".")
	if wildcard && len(labels) < 2 {
		return bad
	}
	for _, label := range labels {
		if len(label) > 63 || !hostLabel.MatchString(label) {
			return bad
		}
	}
	return nil
}

func checkPath(p string) error {
	switch {
	case !strings.HasPrefix(p, "/"):
		return invalidf("A path starts with /, like /auth or /auth/*.")
	case len(p) > maxPathLength:
		return invalidf("A path is at most %d characters.", maxPathLength)
	case strings.ContainsAny(p, "?#"):
		return invalidf("A path can't contain ? or #. Query strings and fragments aren't part of a route.")
	case strings.IndexFunc(p, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return invalidf("A path can't contain spaces or control characters.")
	case strings.Contains(p, "//"):
		return invalidf("A path can't contain //.")
	case p != "/" && strings.HasSuffix(p, "/"):
		return invalidf("Drop the trailing slash, or end with /* to match everything under it.")
	}
	segments := strings.Split(p[1:], "/")
	for i, seg := range segments {
		switch {
		case seg == "." || seg == "..":
			return invalidf("A path can't contain . or .. segments.")
		case strings.Contains(seg, "*") && (seg != "*" || i != len(segments)-1):
			return invalidf("A * is only allowed as the last segment, as /*.")
		}
	}
	return nil
}

func normalizeMethods(in []string) ([]string, error) {
	out := []string{}
	for _, m := range in {
		m = strings.ToUpper(strings.TrimSpace(m))
		if !slices.Contains(methodOrder, m) {
			return nil, invalidf("%s isn't an HTTP method Octopus routes", m)
		}
		if !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(methodOrder, a) - slices.Index(methodOrder, b) })
	return out, nil
}

func hostLabelFor(h string) string {
	if h == "" {
		return "any host"
	}
	return h
}

// CheckRouteConflicts reports whether in, as the route id (empty for a new
// route), would clash with a route in existing on the same hostname. A route
// is never in conflict with itself. Errors are ErrConflict with a message
// for the person who submitted the route.
func CheckRouteConflicts(existing []*Route, id string, in RouteInput) error {
	host := hostLabelFor(in.Hostname)
	candidate := Route{Path: in.Path}
	for _, r := range existing {
		if r.Hostname != in.Hostname || (id != "" && r.ID == id) {
			continue
		}
		switch {
		case r.Path == in.Path:
			return conflictf("A route for %s on %s already exists. Edit it instead.", in.Path, host)
		case r.Wildcard() && !candidate.Wildcard() && r.Prefix() == in.Path:
			return conflictf("%s already answers %s on %s. Edit that route instead.", r.Path, in.Path, host)
		case candidate.Wildcard() && !r.Wildcard() && candidate.Prefix() == r.Path:
			return conflictf("%s already has its own route on %s, and %s would answer it too. Remove or edit %s first.",
				r.Path, host, in.Path, r.Path)
		}
	}
	return nil
}

// Prefix is the path without a trailing "/*", and "/" for "/" and "/*".
func (r Route) Prefix() string {
	if p := strings.TrimSuffix(r.Path, wildcardTail); p != "" {
		return p
	}
	return "/"
}

// Wildcard reports whether the path ends in "/*".
func (r Route) Wildcard() bool { return strings.HasSuffix(r.Path, wildcardTail) }

// OctopusRouteIDs are the ids the route has in Octopus: one for the route,
// and a "-base" one for the bare prefix when the path is a wildcard, since
// Octopus's "/x/*" doesn't answer "/x".
func OctopusRouteIDs(id string, wildcard bool) []string {
	ids := []string{"lp-" + id}
	if wildcard {
		ids = append(ids, "lp-"+id+"-base")
	}
	return ids
}

// UpstreamName is the name of the upstream a manual route's service port has
// in Octopus.
func UpstreamName(service string, port int) string {
	return "lp-" + service + "-" + strconv.Itoa(port)
}

// FarpInstanceID is the FARP instance id Loomplane registers a service as.
func FarpInstanceID(service string) string { return "lp-svc-" + service }
