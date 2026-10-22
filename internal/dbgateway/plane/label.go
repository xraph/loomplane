// Package plane is the database gateway's data plane: it routes Postgres connections from WebSocket and HTTP clients to pooled backend connections, following a signed routing snapshot.
package plane

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// ErrNoRoute means a connection carries no usable route label: the host is
// outside the gateway's suffix, and neither the startup options nor the user
// name name a label, or the one they name is malformed.
var ErrNoRoute = errors.New("no route")

const (
	// labelSep separates the database from the branch in a route label.
	labelSep = "--"
	// maxLabelLen is the DNS limit for one label, and so for the whole route label.
	maxLabelLen = 63
)

var (
	dbPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	branchPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

// Label names one database, or one branch of it: `<db>` or `<db>--<branch>`.
type Label struct {
	DB     string
	Branch string
}

// ParseLabel parses `<db>[--<branch>]`. The whole label is at most 63
// characters. The database starts with a lowercase letter, runs to at most 40
// characters of lowercase letters, digits and single hyphens, and the branch is
// lowercase letters, digits and hyphens that start and end with a letter or digit.
func ParseLabel(s string) (Label, error) {
	if s == "" {
		return Label{}, errors.New("empty route label")
	}
	if len(s) > maxLabelLen {
		return Label{}, fmt.Errorf("route label is %d characters, the limit is %d", len(s), maxLabelLen)
	}
	db, branch, hasBranch := strings.Cut(s, labelSep)
	if !dbPattern.MatchString(db) {
		return Label{}, fmt.Errorf("invalid database name %q in route label", db)
	}
	if !hasBranch {
		return Label{DB: db}, nil
	}
	if strings.Contains(branch, labelSep) {
		return Label{}, errors.New("route label has more than one \"--\"")
	}
	if !branchPattern.MatchString(branch) {
		return Label{}, fmt.Errorf("invalid branch name %q in route label", branch)
	}
	return Label{DB: db, Branch: branch}, nil
}

// String returns the label in the form ParseLabel accepts.
func (l Label) String() string {
	if l.Branch == "" {
		return l.DB
	}
	return l.DB + labelSep + l.Branch
}

// Suffix is the DNS zone below which the gateway's logical hosts live, for
// example "us-east-1.db.example.com".
type Suffix string

// LabelFromHost reads the route label out of a logical host
// `<label>.<suffix>[:port]`. The match on the suffix is case-insensitive, and
// the label must be the whole left part: exactly one DNS label. A host outside
// the suffix returns ErrNoRoute. A host inside it with a malformed label
// returns the parse error.
func (s Suffix) LabelFromHost(host string) (Label, error) {
	suffix := strings.ToLower(string(s))
	if suffix == "" {
		return Label{}, ErrNoRoute
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	left, ok := strings.CutSuffix(host, "."+suffix)
	if !ok || left == "" || strings.Contains(left, ".") {
		return Label{}, ErrNoRoute
	}
	return ParseLabel(left)
}

// RouteKey finds the route label and the role for a connection. It tries, in
// order: the logical host, an `endpoint=<label>` in the startup `options`
// (bare, or after `-c`), and a `user` of the form `role@label`. The first
// source that names a label decides, so a malformed label there is an error
// rather than a reason to try the next source. Every error wraps ErrNoRoute.
//
// The role is the startup `user`. When it ends in `@<label>` and that label is
// the one that was routed to, the suffix is dropped.
func RouteKey(suffix Suffix, host string, startup map[string]string) (Label, string, error) {
	user := startup["user"]

	label, err := suffix.LabelFromHost(host)
	switch {
	case err == nil:
		return label, stripLabel(user, label), nil
	case !errors.Is(err, ErrNoRoute):
		return Label{}, "", fmt.Errorf("%w: host: %v", ErrNoRoute, err)
	}

	if raw, ok := endpointOption(startup["options"]); ok {
		label, err := ParseLabel(raw)
		if err != nil {
			return Label{}, "", fmt.Errorf("%w: endpoint option: %v", ErrNoRoute, err)
		}
		return label, stripLabel(user, label), nil
	}

	if i := strings.LastIndex(user, "@"); i > 0 && i < len(user)-1 {
		label, err := ParseLabel(user[i+1:])
		if err != nil {
			return Label{}, "", fmt.Errorf("%w: user: %v", ErrNoRoute, err)
		}
		return label, user[:i], nil
	}

	return Label{}, "", ErrNoRoute
}

// stripLabel removes a trailing "@<label>" from the user name, unless nothing
// would be left.
func stripLabel(user string, label Label) string {
	if role, ok := strings.CutSuffix(user, "@"+label.String()); ok && role != "" {
		return role
	}
	return user
}

// endpointOption returns the value of the first `endpoint=` setting in a
// startup `options` string. The string is space-separated and carries Postgres
// `-c key=value` pairs, and Neon's convention puts a bare `endpoint=<label>`
// token alongside them. Both forms count. ok is false when there is none; the
// value may still be empty.
func endpointOption(options string) (value string, ok bool) {
	fields := strings.Fields(options)
	for i := 0; i < len(fields); i++ {
		tok := fields[i]
		switch {
		case tok == "-c":
			if i+1 == len(fields) {
				return "", false
			}
			i++
			tok = fields[i]
		case strings.HasPrefix(tok, "-c"):
			tok = tok[len("-c"):]
		}
		if v, found := strings.CutPrefix(tok, "endpoint="); found {
			return v, true
		}
	}
	return "", false
}
