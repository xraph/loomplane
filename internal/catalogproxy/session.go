// Package catalogproxy opens a catalog service's own admin UI through
// Loomplane. A kind registers where its admin lives (Register); someone
// who may modify the project's infrastructure opens a short-lived session
// (Service.Open), and Loomplane serves the admin under
// /catalog-admin/{token}/..., reaching the component over its target's
// private channel with the kind's credential. The browser never holds the
// credential, and the admin's pages run sandboxed in an opaque origin, so
// they can't read Loomplane's cookies or call its API.
package catalogproxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xraph/loomplane/internal/deployments"
	"github.com/xraph/loomplane/internal/planning"
	"github.com/xraph/loomplane/internal/security"
)

// How long a session lasts, and how often the proxy asks again whether
// its creator may still modify the project's infrastructure.
const (
	// IdleTTL ends a session nobody has used for this long.
	IdleTTL = 30 * time.Minute
	// MaxTTL ends a session this long after it was opened, however busy.
	MaxTTL = 8 * time.Hour
	// RecheckEvery is how often, at most, a session's creator's
	// permission is checked again while it's used.
	RecheckEvery = time.Minute
)

// touchEvery is how often, at most, a busy session's last use is written.
// An admin page loads many assets; one write each would be wasted.
const touchEvery = 30 * time.Second

// Errors the service returns.
var (
	// ErrNotFound is a resource the environment doesn't have, or a session
	// that doesn't exist.
	ErrNotFound = errors.New("catalog admin not found")
	// ErrInvalid is a request that can't be served, such as opening the
	// admin of a kind that has none.
	ErrInvalid = errors.New("invalid catalog admin request")
)

// invalidError is ErrInvalid with a message of its own.
type invalidError struct{ msg string }

func (e *invalidError) Error() string        { return e.msg }
func (e *invalidError) Is(target error) bool { return target == ErrInvalid }

// Target is where a kind's admin UI lives on one of its components, and
// how the proxy authenticates to it.
type Target struct {
	// Component and Port name a control port of one of the kind's
	// process components, such as "gateway" and "admin".
	Component, Port string
	// Root is the admin's landing path, such as "/admin". A session opens
	// there.
	Root string
	// AllowedPrefixes are the paths, as the browser asks for them under
	// the session, that the proxy sends on. Any other path is a 404 that
	// never reaches the component. They're checked before StripPrefix is
	// removed.
	AllowedPrefixes []string
	// StripPrefix is removed from the path after the allowed-prefix
	// check, for an admin that's served under a prefix the component
	// itself doesn't use. When it is a whole allowed prefix, such as
	// "/fabriq" for "/fabriq/", every path on the port is reachable: the
	// allowed prefix then limits nothing upstream. A kind may only do that
	// when the port serves nothing but its admin. The proxy only ever
	// reaches a control port, never a routable one.
	StripPrefix string
	// Header builds the headers that authenticate the proxy to the admin,
	// from the composition's generated secrets. A component channel never
	// carries Authorization, so use a header of the kind's own, such as
	// X-Octopus-Admin-Token. Nil sends none.
	Header func(ctx context.Context, secret func(name string) ([]byte, error)) (http.Header, error)
}

var (
	targetsMu sync.RWMutex
	targets   = map[string]Target{}
)

// Register makes kind's admin openable through the proxy. Call it from
// init() in the kind's own package. It panics on a malformed target (see
// validTarget) or a kind registered twice.
//
// What a session can reach on the component is the allowed prefixes,
// less StripPrefix. A target that strips a whole allowed prefix exposes
// its entire control port to whoever holds a session, so register one
// only for a port that serves nothing but the admin.
func Register(kind string, t Target) {
	if err := validTarget(t); err != nil {
		panic(fmt.Sprintf("catalogproxy: %s's admin target: %v", kind, err))
	}
	t.AllowedPrefixes = slices.Clone(t.AllowedPrefixes)
	targetsMu.Lock()
	defer targetsMu.Unlock()
	if _, dup := targets[kind]; dup {
		panic("catalogproxy: " + kind + "'s admin target is registered twice")
	}
	targets[kind] = t
}

func targetOf(kind string) (Target, bool) {
	targetsMu.RLock()
	defer targetsMu.RUnlock()
	t, ok := targets[kind]
	return t, ok
}

// validTarget refuses a target whose paths aren't clean absolute paths,
// "/" as an allowed prefix, a root outside the allowed prefixes, and a
// strip prefix that isn't a leading part of every allowed prefix. It
// can't tell what a port serves, so it allows a strip prefix that is a
// whole allowed prefix (see Target.StripPrefix).
func validTarget(t Target) error {
	if t.Component == "" || t.Port == "" {
		return errors.New("name the component and its control port")
	}
	if len(t.AllowedPrefixes) == 0 {
		return errors.New("allow at least one path prefix")
	}
	clean := func(p string) bool {
		return strings.HasPrefix(p, "/") && path.Clean(p) == strings.TrimSuffix(p, "/") && !strings.ContainsAny(p, "%?#\\")
	}
	for _, p := range t.AllowedPrefixes {
		if !clean(p) || p == "/" {
			return fmt.Errorf("allowed prefix %q must be a clean absolute path below /", p)
		}
		if t.StripPrefix != "" && !underPrefix(strings.TrimSuffix(p, "/"), t.StripPrefix) {
			return fmt.Errorf("strip prefix %q isn't a leading part of allowed prefix %q", t.StripPrefix, p)
		}
	}
	if t.StripPrefix != "" && (!clean(t.StripPrefix) || strings.HasSuffix(t.StripPrefix, "/")) {
		return fmt.Errorf("strip prefix %q must be a clean absolute path without a trailing slash", t.StripPrefix)
	}
	if !clean(t.Root) || !slices.ContainsFunc(t.AllowedPrefixes, func(p string) bool { return underPrefix(t.Root, p) }) {
		return fmt.Errorf("root %q must be a clean absolute path under an allowed prefix", t.Root)
	}
	return nil
}

// underPrefix reports whether the clean path p is prefix or below it, on
// a segment boundary: /admin is under /admin and /admin/, /administrator
// is under neither.
func underPrefix(p, prefix string) bool {
	base := strings.TrimSuffix(prefix, "/")
	if p == base || p == base+"/" {
		return true
	}
	return strings.HasPrefix(p, base+"/")
}

// Session is an opened admin: the URL to open it at, on Loomplane's own
// origin, and when it expires unless used.
type Session struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Record is a session as it's kept. The token itself is never kept, only
// its SHA-256.
type Record struct {
	TokenHash                                               string
	OrgID, ProjectID, EnvironmentID, Resource, Kind, UserID string
	CreatedAt, LastUsedAt                                   time.Time
	// ExpiresAt is when the session ends unless it's used again: IdleTTL
	// after its last use, and never later than MaxTTL after it was opened.
	ExpiresAt time.Time
}

// Store keeps sessions. PgStore is the real one.
type Store interface {
	Create(ctx context.Context, r *Record) error
	// Get returns ErrNotFound for a session that isn't kept.
	Get(ctx context.Context, tokenHash string) (*Record, error)
	Touch(ctx context.Context, tokenHash string, usedAt, expiresAt time.Time) error
	Delete(ctx context.Context, tokenHash string) error
	DeleteResource(ctx context.Context, orgID, environmentID, resource string) error
	// Prune deletes sessions that expired before before.
	Prune(ctx context.Context, before time.Time) error
}

// Deps is what the service reads.
type Deps struct {
	Store  Store
	Access *deployments.CatalogAccess
	Authz  deployments.Authorizer
	// Bindings lists what runs in an environment.
	Bindings func(ctx context.Context, orgID, environmentID string) ([]*planning.Binding, error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Warn hears why the proxy couldn't reach or read an admin. The
	// browser gets a fixed message. Nil drops it.
	Warn func(msg string, err error)
}

// Service opens admin sessions and serves them.
type Service struct {
	d Deps
	// checked is when each session's creator's permission was last
	// checked here, by token hash. Each replica keeps its own, so a
	// replica rechecks a session the first time it serves it.
	mu      sync.Mutex
	checked map[string]time.Time
	// ticker paces the checks of a response that's still streaming; a
	// test replaces it.
	ticker func() (<-chan time.Time, func())
	// checkedStream, when set, hears that a tick's check is done.
	checkedStream func()
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d, checked: map[string]time.Time{}, ticker: func() (<-chan time.Time, func()) {
		t := time.NewTicker(RecheckEvery)
		return t.C, t.Stop
	}}
}

// prunedAfter is how long an expired session is kept, so its link still
// says it expired rather than that it doesn't exist.
const prunedAfter = 24 * time.Hour

// Open opens a session on a composed resource's admin, for someone who
// may modify the project's infrastructure. The session's URL is a path on
// Loomplane's origin, and its token is the only credential it needs.
func (s *Service) Open(ctx context.Context, p security.Principal, ref deployments.ResourceRef) (*Session, error) {
	if err := s.d.Authz.Require(ctx, p, security.InfrastructureModify, ref.ProjectID); err != nil {
		return nil, err
	}
	bindings, err := s.d.Bindings(ctx, p.OrgID, ref.EnvironmentID)
	if err != nil {
		return nil, err
	}
	kind := resourceKind(bindings, p.OrgID, ref)
	if kind == "" {
		return nil, fmt.Errorf("%w: this environment has no catalog resource %s", ErrNotFound, ref.Resource)
	}
	target, ok := targetOf(kind)
	if !ok {
		return nil, &invalidError{kind + " has no admin to open"}
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	now := s.d.Now().UTC()
	r := &Record{
		TokenHash: hashToken(token), OrgID: p.OrgID, ProjectID: ref.ProjectID, EnvironmentID: ref.EnvironmentID,
		Resource: ref.Resource, Kind: kind, UserID: p.UserID, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(IdleTTL),
	}
	if err := s.d.Store.Create(ctx, r); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.checked[r.TokenHash] = now
	for hash, at := range s.checked {
		if now.Sub(at) > MaxTTL {
			delete(s.checked, hash)
		}
	}
	s.mu.Unlock()
	// Sessions long expired go. Failing to prune leaves rows nobody can
	// use, which is no reason to refuse this one.
	_ = s.d.Store.Prune(ctx, now.Add(-prunedAfter)) //nolint:errcheck // see above
	return &Session{URL: sessionPrefix(token) + target.Root, ExpiresAt: r.ExpiresAt}, nil
}

// RevokeResource ends every session on a resource's admin, such as when
// the resource is removed. Their links answer 404 from then on.
func (s *Service) RevokeResource(ctx context.Context, orgID, environmentID, resource string) error {
	return s.d.Store.DeleteResource(ctx, orgID, environmentID, resource)
}

// resourceKind is the kind of a resource in an environment's bindings, or
// "" when it has none.
func resourceKind(bindings []*planning.Binding, orgID string, ref deployments.ResourceRef) string {
	for _, b := range bindings {
		if b.OrgID != orgID || b.ProjectID != ref.ProjectID || b.EnvironmentID != ref.EnvironmentID || b.Subject != planning.ResourceSubject || b.Name != ref.Resource {
			continue
		}
		var cfg planning.ResourceConfig
		if err := json.Unmarshal(b.Config, &cfg); err != nil {
			continue
		}
		if cfg.Composition != nil && cfg.Composition.Kind != "" {
			return cfg.Composition.Kind
		}
		return cfg.Kind
	}
	return ""
}

// tokenBytes is how much randomness a session token carries.
const tokenBytes = 32

// tokenLen is a token's length in unpadded base64url.
var tokenLen = base64.RawURLEncoding.EncodedLen(tokenBytes)

func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("make an admin session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// wellFormed reports whether s could be a token, before anything looks it
// up.
func wellFormed(s string) bool {
	if len(s) != tokenLen {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// pathPrefix is where every session is served.
const pathPrefix = "/catalog-admin/"

func sessionPrefix(token string) string { return pathPrefix + token }
