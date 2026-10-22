package catalogconnect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
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

func engineTitle(engine string) string {
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
// check that can't start.
func (c *Checker) Check(ctx context.Context, orgID, endpointID string, req recipes.Requirement, dial DialFunc) (*Report, error) {
	if dial == nil {
		return nil, errors.New("check an endpoint: no dialer")
	}
	e, dsn, err := c.Endpoints.Resolve(ctx, orgID, endpointID)
	if err != nil {
		return nil, err
	}
	if req.Engine != "" && req.Engine != e.Engine {
		return &Report{Problems: []string{fmt.Sprintf("%s is a %s endpoint, and this needs %s", e.Name, engineTitle(e.Engine), engineTitle(req.Engine))}}, nil
	}
	prober, ok := c.Probers[e.Engine]
	if !ok {
		return nil, fmt.Errorf("check an endpoint: Loomplane has no prober for %s", e.Engine)
	}
	report, err := prober.Probe(ctx, dial, dsn, req)
	if err != nil {
		return &Report{Problems: []string{problem(e.address(), dsn, err)}}, nil
	}
	return report, nil
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
		r.Problems = append(r.Problems, fmt.Sprintf("%s reports version %q, which can't be compared with %s", engineTitle(engine), r.Version, req.MinVersion))
	case old:
		r.Problems = append(r.Problems, fmt.Sprintf("%s %s is older than %s", engineTitle(engine), r.Version, req.MinVersion))
	}
}
