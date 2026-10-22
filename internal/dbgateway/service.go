package dbgateway

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xraph/loomplane/internal/dbgateway/plane"
)

var (
	// ErrUnauthorized is every request that isn't signed by a known gateway.
	// It never says which check failed.
	ErrUnauthorized = errors.New("the request isn't signed by a known database gateway")
	// ErrNotConfigured means no signing key is configured, so the service
	// can't sign snapshots.
	ErrNotConfigured = errors.New("the database gateway signing key is not configured")
	// ErrNoHoldHandler is every hold in Phase 1: nothing brings a route up
	// on demand yet.
	ErrNoHoldHandler = errors.New("no hold handler")
	// ErrNoURL is a resource whose address isn't stored yet.
	ErrNoURL = errors.New("no address stored for that resource")
)

const (
	// requestContext and the header names match plane.SignRequest.
	requestContext  = "loomplane/dbgateway/request/v1\x00"
	headerGateway   = "Loomplane-Gateway"
	headerTimestamp = "Loomplane-Timestamp"
	headerSignature = "Loomplane-Signature"
	// maxSkew is how far a request's timestamp may be from the clock.
	maxSkew = 300 * time.Second
	// gatewayPath is where the gateway endpoints live; the next path
	// segment is the gateway id.
	gatewayPath = "/api/v1/db-gateways/"
)

// ResourceURLs reads the stored address of an environment's resource. A
// resource with none gives ErrNoURL.
type ResourceURLs interface {
	URL(ctx context.Context, org, environmentID, resource string) (string, error)
}

// storage is what the service needs from Store.
type storage interface {
	GatewayByID(ctx context.Context, id string) (*Gateway, error)
	Routes(ctx context.Context, org, gatewayID string) ([]RouteRecord, error)
	BumpSnapshot(ctx context.Context, org, gatewayID, digest string) (uint64, bool, error)
	LatestSnapshot(ctx context.Context, org, gatewayID string) (uint64, string, error)
	RaiseSnapshotVersion(ctx context.Context, org, gatewayID, digest string, atLeast uint64) (uint64, error)
	Report(ctx context.Context, org, gatewayID string, r plane.Report, now time.Time) (int, error)
}

// Service is the control plane's side of the gateway protocol: it compiles
// and signs each gateway's snapshot, takes its reports and checks that its
// requests are its own.
type Service struct {
	store storage
	urls  ResourceURLs
	key   ed25519.PrivateKey
	trust plane.TrustKey
	now   func() time.Time
	// verifiers caches route verifiers between compiles (R74).
	verifiers *verifierCache

	// OnSkip, when set, hears about the routes a snapshot left out each
	// time a gateway's snapshot changes.
	OnSkip func(gatewayID string, skipped []Skipped)
}

// NewService returns the service. A nil key leaves it unconfigured: it signs
// nothing, and Configured reports false.
func NewService(store *Store, urls ResourceURLs, key ed25519.PrivateKey, trust plane.TrustKey, now func() time.Time) *Service {
	return newService(store, urls, key, trust, now)
}

func newService(store storage, urls ResourceURLs, key ed25519.PrivateKey, trust plane.TrustKey, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, urls: urls, key: key, trust: trust, now: now, verifiers: newVerifierCache()}
}

// Configured reports whether the service has a key to sign snapshots with.
func (s *Service) Configured() bool { return len(s.key) == ed25519.PrivateKeySize }

// TrustKeys returns the keys a gateway should trust for this control plane's
// snapshots, or none when no signing key is configured.
func (s *Service) TrustKeys() []plane.TrustKey {
	if !s.Configured() {
		return []plane.TrustKey{}
	}
	return []plane.TrustKey{s.trust}
}

// Snapshot returns gw's signed snapshot, or nil when its version is not newer
// than after. The version moves only when the content does: the digest that
// decides leaves out the sealed credentials, which are sealed afresh each
// time, and covers them through the route verifiers, which are derived from
// the same user and password. Waiting for something new is the caller's job.
func (s *Service) Snapshot(ctx context.Context, gw *Gateway, after uint64) (*plane.Snapshot, error) {
	if !s.Configured() {
		return nil, ErrNotConfigured
	}
	if after >= math.MaxInt64 {
		return nil, fmt.Errorf("%w: snapshot version %d", ErrInvalid, after)
	}
	// R71: sign only a version whose stored digest is the one compiled
	// here. A concurrent writer that stored other content gets one more
	// compile and bump; after that the stored pair is used only if a fresh
	// compile matches it.
	for attempt := range 3 {
		snap, skipped, digest, err := s.compile(ctx, gw)
		if err != nil {
			return nil, err
		}
		if attempt < 2 {
			_, changed, berr := s.store.BumpSnapshot(ctx, gw.OrgID, gw.ID, digest)
			if berr != nil {
				return nil, berr
			}
			if changed && len(skipped) > 0 && s.OnSkip != nil {
				s.OnSkip(gw.ID, skipped)
			}
		}
		version, stored, err := s.store.LatestSnapshot(ctx, gw.OrgID, gw.ID)
		if err != nil {
			return nil, err
		}
		if stored != digest {
			continue
		}
		if after > version {
			// R75: the gateway holds a version this store never reached,
			// as after a database restore. Raise the stored version past
			// it, so the gateway takes the snapshot rather than refusing
			// it as stale.
			version, err = s.store.RaiseSnapshotVersion(ctx, gw.OrgID, gw.ID, digest, after+1)
			if errors.Is(err, ErrNotFound) {
				continue // another writer stored other content meanwhile
			}
			if err != nil {
				return nil, err
			}
		}
		return s.sign(snap, version, after)
	}
	return nil, fmt.Errorf("gateway %s: its stored snapshot keeps changing under another writer", gw.ID)
}

// compile builds gw's snapshot from its routes and their addresses and
// returns it with its digest.
func (s *Service) compile(ctx context.Context, gw *Gateway) (plane.Snapshot, []Skipped, string, error) {
	routes, err := s.store.Routes(ctx, gw.OrgID, gw.ID)
	if err != nil {
		return plane.Snapshot{}, nil, "", err
	}
	urls := make(map[string]string, len(routes))
	for _, r := range routes {
		u, uerr := s.urls.URL(ctx, gw.OrgID, gw.EnvironmentID, r.Resource)
		switch {
		case errors.Is(uerr, ErrNoURL):
			continue
		case uerr != nil:
			// A route left out on a read failure would vanish from every
			// gateway, so a failed read fails the snapshot.
			return plane.Snapshot{}, nil, "", fmt.Errorf("read the address of %s: %w", r.Resource, uerr)
		}
		urls[r.Resource] = u
	}
	verify, done := s.verifiers.compileFor(gw.ID)
	snap, skipped, err := compile(*gw, routes, urls, verify)
	done()
	if err != nil {
		return plane.Snapshot{}, nil, "", err
	}
	digest, err := snapshotDigest(snap)
	if err != nil {
		return plane.Snapshot{}, nil, "", err
	}
	return snap, skipped, digest, nil
}

// snapshotDigest is the hex SHA-256 of the snapshot's signed payload with no
// version, issue time or key, and with the sealed credentials cleared.
func snapshotDigest(snap plane.Snapshot) (string, error) {
	snap.Version, snap.IssuedAt, snap.KeyID = 0, time.Time{}, ""
	snap.Routes = append([]plane.Route(nil), snap.Routes...)
	for i := range snap.Routes {
		snap.Routes[i].Backend.Credential = plane.Sealed{}
	}
	payload, err := plane.SnapshotPayload(snap)
	if err != nil {
		return "", fmt.Errorf("encode the snapshot: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) sign(snap plane.Snapshot, version, after uint64) (*plane.Snapshot, error) {
	if version <= after {
		return nil, nil
	}
	snap.Version = version
	snap.IssuedAt = s.now().UTC()
	signed, err := plane.SignSnapshot(snap, s.key, s.trust.ID())
	if err != nil {
		return nil, err
	}
	return &signed, nil
}

// Report stores one instance's report and says how many of gw's instances
// are live.
func (s *Service) Report(ctx context.Context, gw *Gateway, r plane.Report) (plane.ReportResponse, error) {
	live, err := s.store.Report(ctx, gw.OrgID, gw.ID, r, s.now())
	if err != nil {
		return plane.ReportResponse{}, err
	}
	return plane.ReportResponse{LiveInstances: live}, nil
}

// Hold would bring the route for l up. Phase 1 has nothing that does, so it
// always returns ErrNoHoldHandler.
func (s *Service) Hold(context.Context, *Gateway, plane.Label) error {
	return ErrNoHoldHandler
}

// VerifyRequest returns the gateway that signed r, whose body is body. The
// Loomplane-Gateway header must name the gateway in r's path, the
// Loomplane-Timestamp must be within 300 seconds of now, and the
// Loomplane-Signature must be the gateway's Ed25519 signature over the
// method, the request URI as received, the timestamp and the body's SHA-256,
// as plane.SignRequest makes it. Every failed check is ErrUnauthorized.
// Failing to read the gateway is returned as it is.
func (s *Service) VerifyRequest(r *http.Request, body []byte, now time.Time) (*Gateway, error) {
	id := r.Header.Get(headerGateway)
	if id == "" || !idPattern.MatchString(id) || pathGatewayID(r.URL.Path) != id {
		return nil, ErrUnauthorized
	}
	ts := r.Header.Get(headerTimestamp)
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil, ErrUnauthorized
	}
	if skew := now.Sub(time.Unix(unix, 0)); skew > maxSkew || skew < -maxSkew {
		return nil, ErrUnauthorized
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get(headerSignature))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, ErrUnauthorized
	}
	gw, err := s.store.GatewayByID(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	uri := r.RequestURI
	if uri == "" {
		uri = r.URL.RequestURI()
	}
	sum := sha256.Sum256(body)
	msg := requestContext + r.Method + "\n" + uri + "\n" + ts + "\n" + hex.EncodeToString(sum[:])
	if !ed25519.Verify(gw.SigningPublicKey, []byte(msg), sig) {
		return nil, ErrUnauthorized
	}
	return gw, nil
}

// pathGatewayID returns the gateway id in a /api/v1/db-gateways/<id>/... path,
// or "".
func pathGatewayID(path string) string {
	rest, ok := strings.CutPrefix(path, gatewayPath)
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}
