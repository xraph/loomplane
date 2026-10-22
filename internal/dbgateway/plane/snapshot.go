package plane

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"time"
)

var (
	// ErrSnapshot is the one error VerifySnapshot returns. An unknown key, an
	// expired key, a bad signature, the wrong gateway and an invalid route
	// all look the same, so a failure says nothing about why it failed.
	ErrSnapshot = errors.New("snapshot is not trusted")
	// ErrStaleSnapshot means Swap was given a snapshot that is not newer than
	// the one being served.
	ErrStaleSnapshot = errors.New("snapshot is not newer than the current one")
)

// snapshotContext separates snapshot signatures from every other signature
// made with the same key. It is prepended to the payload before signing.
const snapshotContext = "loomplane/dbgateway/snapshot/v1\x00"

// RouteState is what a gateway does with new connections to a route.
type RouteState string

const (
	// RouteStateActive serves connections.
	RouteStateActive RouteState = "active"
	// RouteStatePaused queues new connections until the route resumes.
	RouteStatePaused RouteState = "paused"
	// RouteStateDraining refuses new connections while existing ones finish.
	RouteStateDraining RouteState = "draining"
	// RouteStateHolding holds new connections while the backend wakes.
	RouteStateHolding RouteState = "holding"
)

func (s RouteState) valid() bool {
	switch s {
	case RouteStateActive, RouteStatePaused, RouteStateDraining, RouteStateHolding:
		return true
	}
	return false
}

// Verifier lets the gateway check a client's SCRAM-SHA-256 proof for one role
// without holding the password.
type Verifier struct {
	Role       string `json:"role"`
	Salt       []byte `json:"salt"`
	Iterations int    `json:"iterations"`
	StoredKey  []byte `json:"stored_key"`
	ServerKey  []byte `json:"server_key"`
}

// Backend is the Postgres server behind a route. Credential is the connection
// secret, sealed to the gateway's own key.
type Backend struct {
	Kind       string `json:"kind"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Database   string `json:"database"`
	SNI        string `json:"sni"`
	SSLMode    string `json:"ssl_mode"`
	Credential Sealed `json:"credential"`
}

// Route sends connections for one label to one backend. IdleTimeout encodes
// as integer nanoseconds, the encoding/json default for a time.Duration.
type Route struct {
	ID          string            `json:"id"`
	Label       Label             `json:"label"`
	State       RouteState        `json:"state"`
	Backend     Backend           `json:"backend"`
	Verifiers   []Verifier        `json:"verifiers"`
	Params      map[string]string `json:"params"`
	IdleTimeout time.Duration     `json:"idle_timeout"`
}

// Snapshot is the control plane's routing table for one gateway. The control
// plane signs it into a SignedSnapshot; the gateway verifies that before
// serving from it. KeyID names the signing key inside the signed bytes, so
// the signature covers which key made it.
type Snapshot struct {
	GatewayID string    `json:"gateway_id"`
	Version   uint64    `json:"version"`
	IssuedAt  time.Time `json:"issued_at"`
	Routes    []Route   `json:"routes"`
	KeyID     string    `json:"key_id"`
}

// SignedSnapshot is what the control plane serves and what the gateway keeps
// as its last-good copy. Payload is the snapshot's JSON exactly as it was
// signed, and the gateway verifies those bytes before it decodes them, so a
// newer control plane can add fields an older gateway doesn't know without
// breaking its signature check. Payload and Signature encode as standard
// base64, the encoding/json default for []byte.
type SignedSnapshot struct {
	Payload   []byte `json:"payload"`
	KeyID     string `json:"key_id"`
	Signature []byte `json:"signature"`
}

// TrustKey is a control-plane signing key the gateway accepts, and the window
// in which it accepts it: NotBefore is inclusive, NotAfter exclusive.
type TrustKey struct {
	PublicKey ed25519.PublicKey `json:"public_key"`
	NotBefore time.Time         `json:"not_before"`
	NotAfter  time.Time         `json:"not_after"`
}

// ID is the hex SHA-256 of the public key. Snapshots name their signing key
// by it.
func (k TrustKey) ID() string {
	sum := sha256.Sum256(k.PublicKey)
	return hex.EncodeToString(sum[:])
}

// SnapshotPayload returns the bytes that are signed: the snapshot as JSON
// with the routes sorted by ID. encoding/json writes struct fields in
// declaration order and sorts map keys, which makes the output canonical.
// The input is not changed.
func SnapshotPayload(s Snapshot) ([]byte, error) {
	s.Routes = slices.Clone(s.Routes)
	slices.SortStableFunc(s.Routes, func(a, b Route) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	return json.Marshal(s)
}

// SignSnapshot sets s's key ID to keyID, encodes it with SnapshotPayload and
// signs those bytes with private. The input is not changed.
func SignSnapshot(s Snapshot, private ed25519.PrivateKey, keyID string) (SignedSnapshot, error) {
	if len(private) != ed25519.PrivateKeySize {
		return SignedSnapshot{}, errors.New("sign snapshot: bad private key")
	}
	s.KeyID = keyID
	payload, err := SnapshotPayload(s)
	if err != nil {
		return SignedSnapshot{}, err
	}
	return SignedSnapshot{
		Payload:   payload,
		KeyID:     keyID,
		Signature: ed25519.Sign(private, append([]byte(snapshotContext), payload...)),
	}, nil
}

// VerifySnapshot checks that env was signed by the key it names and that the
// key is among keys and valid at now, and only then decodes the payload. The
// snapshot must name the same key, be for gatewayID and have every route in a
// known state. Fields in the payload that Snapshot doesn't have are ignored.
// Every failure returns ErrSnapshot.
func VerifySnapshot(env SignedSnapshot, keys []TrustKey, gatewayID string, now time.Time) (Snapshot, error) {
	i := slices.IndexFunc(keys, func(k TrustKey) bool { return k.ID() == env.KeyID })
	if i < 0 {
		return Snapshot{}, ErrSnapshot
	}
	key := keys[i]
	if len(key.PublicKey) != ed25519.PublicKeySize || now.Before(key.NotBefore) || !now.Before(key.NotAfter) {
		return Snapshot{}, ErrSnapshot
	}
	if !ed25519.Verify(key.PublicKey, append([]byte(snapshotContext), env.Payload...), env.Signature) {
		return Snapshot{}, ErrSnapshot
	}
	var s Snapshot
	if err := json.Unmarshal(env.Payload, &s); err != nil {
		return Snapshot{}, ErrSnapshot
	}
	if s.KeyID != env.KeyID || s.GatewayID != gatewayID {
		return Snapshot{}, ErrSnapshot
	}
	for _, r := range s.Routes {
		if !r.State.valid() {
			return Snapshot{}, ErrSnapshot
		}
	}
	return s, nil
}

// Store holds the snapshot the gateway is serving. The zero value is empty and
// ready to use. Readers never block, and see either the old snapshot or the
// new one whole.
type Store struct {
	cur atomic.Pointer[Snapshot]
}

// Current returns the snapshot being served, or nil before the first Swap. The
// result is shared with other readers and must not be modified.
func (s *Store) Current() *Snapshot {
	return s.cur.Load()
}

// Swap makes next the current snapshot. It does not verify next: the caller
// does that first. It returns ErrStaleSnapshot when next.Version is not
// greater than the current version, so a replayed or reordered snapshot cannot
// roll the gateway back.
func (s *Store) Swap(next Snapshot) error {
	for {
		old := s.cur.Load()
		if old != nil && next.Version <= old.Version {
			return ErrStaleSnapshot
		}
		if s.cur.CompareAndSwap(old, &next) {
			return nil
		}
	}
}
