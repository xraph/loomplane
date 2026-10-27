package catalogconnect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/recipes"
)

// DialFunc opens a connection. Checks dial only through the one they're
// given, so the caller decides which network an endpoint is reached from.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Report is what a check found. Problems are reasons a service can't use
// the endpoint; Missing are required extensions it doesn't offer.
type Report struct {
	Version  string   `json:"version,omitempty"`
	Missing  []string `json:"missing,omitempty"`
	Problems []string `json:"problems,omitempty"`
}

// Prober checks one engine's endpoint. Its errors never carry the DSN.
type Prober interface {
	Probe(ctx context.Context, dial DialFunc, dsn string, req recipes.Requirement) (*Report, error)
}

// probeTimeout bounds one probe, connecting included.
const probeTimeout = 10 * time.Second

// Checker checks a stored endpoint against what a kind requires.
type Checker struct {
	Endpoints *Service
	// Probers maps an engine to its prober: "postgres": PostgresProber{},
	// "redis": RedisProber{}.
	Probers map[string]Prober
}

var engineTitles = map[string]string{EnginePostgres: "PostgreSQL", EngineRedis: "Redis"}

// EngineTitle names an engine the way checks word it: "PostgreSQL",
// "Redis".
func EngineTitle(engine string) string {
	if t, ok := engineTitles[engine]; ok {
		return t
	}
	return engine
}

// Endpoint returns the endpoint Check would probe, without its DSN, or
// ErrNotFound.
func (c *Checker) Endpoint(ctx context.Context, orgID, id string) (*Endpoint, error) {
	return c.Endpoints.Get(ctx, orgID, id)
}

// Check probes an endpoint through dial. An endpoint that can't be reached
// or refuses the check is a Report problem naming its host and port, never
// an error; errors are an endpoint that isn't there (ErrNotFound) or a
// check that can't start. A problem that names the kind the endpoint is
// for takes it from the context's Consumer.
func (c *Checker) Check(ctx context.Context, orgID, endpointID string, req recipes.Requirement, dial DialFunc) (*Report, error) {
	if dial == nil {
		return nil, errors.New("check an endpoint: no dialer")
	}
	e, dsn, err := c.Endpoints.Resolve(ctx, orgID, endpointID)
	if err != nil {
		return nil, err
	}
	if req.Engine != "" && req.Engine != e.Engine {
		return &Report{Problems: []string{fmt.Sprintf("%s is a %s endpoint, and this needs %s", e.Name, EngineTitle(e.Engine), EngineTitle(req.Engine))}}, nil
	}
	return c.probe(ctx, e, dsn, req, dial)
}

// CheckDSN checks a DSN that isn't stored, such as one about to be: it
// reads the DSN as an endpoint of req's engine and probes it through dial,
// touching neither the store nor Vault. A DSN that can't be read is
// ErrInvalid. The report and errors never hold the DSN.
func (c *Checker) CheckDSN(ctx context.Context, dsn string, req recipes.Requirement, dial DialFunc) (*Report, error) {
	if dial == nil {
		return nil, errors.New("check a DSN: no dialer")
	}
	if req.Engine != EnginePostgres && req.Engine != EngineRedis {
		return nil, fmt.Errorf("%w: Loomplane checks postgres and redis endpoints, not %q", ErrInvalid, req.Engine)
	}
	dsn = strings.TrimSpace(dsn)
	e := &Endpoint{Engine: req.Engine}
	if err := parseDSN(e, dsn); err != nil {
		return nil, err
	}
	return c.probe(ctx, e, dsn, req, dial)
}

// probe runs the engine's prober, then the rules that read the endpoint
// itself rather than the server.
func (c *Checker) probe(ctx context.Context, e *Endpoint, dsn string, req recipes.Requirement, dial DialFunc) (*Report, error) {
	prober, ok := c.Probers[e.Engine]
	if !ok {
		return nil, fmt.Errorf("check an endpoint: Loomplane has no prober for %s", e.Engine)
	}
	report, err := prober.Probe(ctx, dial, dsn, req)
	if err != nil {
		report = &Report{Problems: []string{problem(e.address(), dsn, err)}}
	}
	if p := tlsProblem(ctx, e, req); p != "" {
		report.Problems = append(report.Problems, p)
	}
	for i, p := range report.Problems {
		report.Problems[i] = redact(p, dsn)
	}
	return report, nil
}

// tlsProblem is why the kind can't use the endpoint's TLS mode, or "".
func tlsProblem(ctx context.Context, e *Endpoint, req recipes.Requirement) string {
	if len(req.TLS) == 0 || slices.Contains(req.TLS, e.TLS) {
		return ""
	}
	who := ConsumerFrom(ctx).title()
	if e.TLS != TLSDisable && !slices.Contains(req.TLS, TLSRequire) && !slices.Contains(req.TLS, TLSVerifyFull) {
		return fmt.Sprintf("%s can't reach %s over TLS yet", who, EngineTitle(e.Engine))
	}
	return fmt.Sprintf("%s reaches %s with TLS %s, and this endpoint uses %s", who, EngineTitle(e.Engine), strings.Join(req.TLS, " or "), e.TLS)
}

// Consumer is the catalog kind an endpoint is checked for, so a problem
// can say who needs what.
type Consumer struct {
	Title string // "Fabriq"
	// Role is the role the kind creates on the endpoint, "fabriq_app", or
	// "" when its recipe doesn't name one.
	Role string
}

// title is how a problem names the consumer.
func (c Consumer) title() string {
	if c.Title == "" {
		return "This service"
	}
	return c.Title
}

// ConsumerOf is the consumer a kind's dependency is checked for. The role
// is the user in the kind's own connection template for the dependency,
// fabriq_app in
// "postgres://fabriq_app:${secret:app-password}@${dep:postgres:hostport}/...".
func ConsumerOf(kind recipes.Kind, dependency string) Consumer {
	c := Consumer{Title: kind.Title}
	if c.Title == "" {
		c.Title = kind.Name
	}
	var templates []string
	for _, component := range kind.Components {
		for _, env := range component.Env {
			templates = append(templates, env.Template)
		}
	}
	for _, out := range kind.Outputs {
		templates = append(templates, out.Template)
	}
	for _, t := range templates {
		if role := templateUser(t, dependency); role != "" {
			c.Role = role
			break
		}
	}
	return c
}

// templateUser is the literal user of a URL template whose host is the
// dependency's, or "".
func templateUser(template, dependency string) string {
	_, rest, ok := strings.Cut(template, "://")
	if !ok {
		return ""
	}
	userinfo, host, ok := strings.Cut(rest, "@")
	if !ok || strings.Contains(userinfo, "/") || !strings.HasPrefix(host, "${dep:"+dependency+":") {
		return ""
	}
	user, _, _ := strings.Cut(userinfo, ":")
	if user == "" || strings.ContainsAny(user, "${}") {
		return ""
	}
	return user
}

type consumerKey struct{}

// WithConsumer returns a context whose checks are for c.
func WithConsumer(ctx context.Context, c Consumer) context.Context {
	return context.WithValue(ctx, consumerKey{}, c)
}

// ConsumerFrom is the consumer WithConsumer set, or the zero Consumer.
func ConsumerFrom(ctx context.Context) Consumer {
	if c, ok := ctx.Value(consumerKey{}).(Consumer); ok {
		return c
	}
	return Consumer{}
}

// CheckInput is a DSN to check before it's stored: the endpoint, the
// target it's reached from, and the kind and dependency that would use it.
type CheckInput struct {
	Engine string `json:"engine"` // "postgres" or "redis"
	// DSN is checked and dropped. It's never stored or returned.
	DSN        string `json:"dsn"`
	Target     string `json:"target"` // the connection the check dials from
	Kind       string `json:"kind"`
	Dependency string `json:"dependency"`
}

// CheckResult is what checking a DSN found. OK is true when nothing is
// missing and there are no problems.
type CheckResult struct {
	OK       bool     `json:"ok"`
	Version  string   `json:"version,omitempty"`
	Missing  []string `json:"missing,omitempty"`
	Problems []string `json:"problems,omitempty"`
	// Notes are caveats that don't fail the check, such as a cluster's
	// check running from Loomplane.
	Notes []string `json:"notes,omitempty"`
}

// Result is the report as a CheckResult. A report that found no version
// and nothing wrong never reached a usable endpoint, so it fails.
func (r *Report) Result() CheckResult {
	out := CheckResult{Version: r.Version, Missing: slices.Clone(r.Missing), Problems: slices.Clone(r.Problems)}
	if r.Version == "" && len(r.Problems) == 0 && len(r.Missing) == 0 {
		out.Problems = []string{"the endpoint didn't say which version it runs"}
	}
	out.OK = len(out.Missing) == 0 && len(out.Problems) == 0
	return out
}

// probeError is a probe that didn't finish. Its message is safe to show:
// probers build it from the dialer's error or the server's answer, never
// from the DSN.
type probeError struct {
	// answered is true when the endpoint was reached and refused.
	answered bool
	msg      string
}

func (e *probeError) Error() string { return e.msg }

func unreachable(err error) error { return &probeError{msg: err.Error()} }

func refused(msg string) error { return &probeError{answered: true, msg: msg} }

// errTimedOut is a probe that ran out of time.
var errTimedOut = &probeError{msg: "no answer within " + probeTimeout.String()}

// problem words a failed probe for the report: host and port, then why.
func problem(address, dsn string, err error) string {
	var pe *probeError
	if !errors.As(err, &pe) {
		pe = &probeError{msg: err.Error()}
	}
	msg := redact(pe.msg, dsn)
	if pe.answered {
		return fmt.Sprintf("%s refused the check: %s", address, msg)
	}
	return fmt.Sprintf("%s isn't reachable from the target: %s", address, msg)
}

// minSecret is the shortest password redact removes from a message. A
// shorter one would take ordinary letters out of every word.
const minSecret = 4

// redact removes the DSN and its password from a message. Probers don't
// put either in one; this is the second line.
func redact(msg, dsn string) string {
	if dsn == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, dsn, "[redacted]")
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && len(pw) >= minSecret {
			msg = strings.ReplaceAll(msg, pw, "[redacted]")
		}
	}
	return msg
}

var leadingVersion = regexp.MustCompile(`^\d+(\.\d+)*`)

// version is the leading dotted number of a server's version string:
// "16.4" from "16.4 (Debian 16.4-1.pgdg120+1)".
func version(s string) string {
	return leadingVersion.FindString(strings.TrimSpace(s))
}

// olderThan reports whether version have is older than want, comparing
// dotted numbers; a part left out is 0, so "17" is "17.0".
func olderThan(have, want string) (bool, error) {
	h, err := versionParts(have)
	if err != nil {
		return false, err
	}
	w, err := versionParts(want)
	if err != nil {
		return false, err
	}
	for i := range max(len(h), len(w)) {
		var a, b int
		if i < len(h) {
			a = h[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			return a < b, nil
		}
	}
	return false, nil
}

func versionParts(s string) ([]int, error) {
	if s == "" || version(s) != s {
		return nil, fmt.Errorf("%q isn't a version number", s)
	}
	fields := strings.Split(s, ".")
	out := make([]int, len(fields))
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("%q isn't a version number", s)
		}
		out[i] = n
	}
	return out, nil
}

// checkVersion adds a problem when the endpoint's version is older than
// the requirement's minimum.
func checkVersion(r *Report, engine string, req recipes.Requirement) {
	if req.MinVersion == "" {
		return
	}
	old, err := olderThan(r.Version, req.MinVersion)
	switch {
	case err != nil:
		r.Problems = append(r.Problems, fmt.Sprintf("%s reports version %q, which can't be compared with %s", EngineTitle(engine), r.Version, req.MinVersion))
	case old:
		r.Problems = append(r.Problems, fmt.Sprintf("%s %s is older than %s", EngineTitle(engine), r.Version, req.MinVersion))
	}
}
