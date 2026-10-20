// Package access compiles and verifies scoped federation authorization.
package access

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xraph/loomplane/internal/workloadidentity"
	domain "github.com/xraph/loomplane/pkg/federation"
)

var ErrSnapshot = errors.New("federation snapshot is not trusted")

const snapshotSignatureContext = "loomplane/federation/snapshot/v1\x00"
const maxSnapshotKeys = 32

// SnapshotKey is public verification material delivered by trusted configuration.
// A key ID is its SHA256 fingerprint; snapshots cannot supply new trust keys.
type SnapshotKey struct {
	PublicKey ed25519.PublicKey
	NotBefore time.Time
	NotAfter  time.Time
}

func (k SnapshotKey) ID() string {
	digest := sha256.Sum256(k.PublicKey)
	return hex.EncodeToString(digest[:])
}
func (k SnapshotKey) valid() bool {
	return len(k.PublicKey) == ed25519.PublicKeySize && !k.NotBefore.IsZero() && k.NotAfter.After(k.NotBefore)
}
func (k SnapshotKey) active(now time.Time) bool {
	return k.valid() && !now.Before(k.NotBefore) && now.Before(k.NotAfter)
}
func (k SnapshotKey) clone() SnapshotKey { k.PublicKey = bytes.Clone(k.PublicKey); return k }

// SnapshotSigner signs complete configuration for exactly one tenant scope.
// A signature attests provenance; policy compilation and enforcement are separate.
type SnapshotSigner struct {
	scope   domain.Scope
	key     SnapshotKey
	private ed25519.PrivateKey
}

func NewSnapshotSigner(scope domain.Scope, private ed25519.PrivateKey, notBefore, notAfter time.Time) (*SnapshotSigner, error) {
	if !scope.Valid() || len(private) != ed25519.PrivateKeySize {
		return nil, ErrSnapshot
	}
	canonical := ed25519.NewKeyFromSeed(private.Seed())
	if !bytes.Equal(canonical, private) {
		return nil, ErrSnapshot
	}
	key := SnapshotKey{PublicKey: bytes.Clone(canonical[ed25519.SeedSize:]), NotBefore: notBefore, NotAfter: notAfter}
	if !key.valid() {
		return nil, ErrSnapshot
	}
	return &SnapshotSigner{scope: scope, key: key, private: canonical}, nil
}
func (s *SnapshotSigner) Key() SnapshotKey { return s.key.clone() }
func (s *SnapshotSigner) Sign(snapshot domain.Snapshot, now time.Time) (domain.Snapshot, error) {
	if s == nil || snapshot.Scope != s.scope || !s.key.active(now) || snapshot.KeyID != "" || len(snapshot.Signature) != 0 || snapshot.ExpiresAt.After(s.key.NotAfter) {
		return domain.Snapshot{}, ErrSnapshot
	}
	snapshot.KeyID = s.key.ID()
	payload, err := snapshotPayload(snapshot, now)
	if err != nil {
		return domain.Snapshot{}, err
	}
	var signed domain.Snapshot
	if err = json.Unmarshal(payload, &signed); err != nil {
		return domain.Snapshot{}, ErrSnapshot
	}
	signed.Signature = ed25519.Sign(s.private, signatureMessage(payload))
	return signed, nil
}

// SnapshotTrust replaces scoped key sets atomically and rejects older updates.
// Malformed newer updates clear stale keys; foreign-scope updates have no effect.
type SnapshotTrust struct {
	mu         sync.RWMutex
	scope      domain.Scope
	generation uint64
	keys       map[string]SnapshotKey
}

func NewSnapshotTrust(scope domain.Scope, generation uint64, keys []SnapshotKey) (*SnapshotTrust, error) {
	if !scope.Valid() {
		return nil, ErrSnapshot
	}
	trust := &SnapshotTrust{scope: scope}
	if err := trust.Replace(scope, generation, keys); err != nil {
		return nil, err
	}
	return trust, nil
}
func (t *SnapshotTrust) Replace(scope domain.Scope, generation uint64, keys []SnapshotKey) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if scope != t.scope || generation <= t.generation {
		return ErrSnapshot
	}
	t.generation = generation
	t.keys = nil
	if len(keys) > maxSnapshotKeys {
		return ErrSnapshot
	}
	next := make(map[string]SnapshotKey, len(keys))
	for _, key := range keys {
		if !key.valid() {
			return ErrSnapshot
		}
		id := key.ID()
		if _, duplicate := next[id]; duplicate {
			return ErrSnapshot
		}
		next[id] = key.clone()
	}
	t.keys = next
	return nil
}

// VerifiedSnapshot retains immutable signed bytes, never caller-owned slices.
// It cannot be reconstructed from disk. Apply must recheck ValidAt and enforce
// monotonic sequence/epoch and grant policy before opening any routes.
type VerifiedSnapshot struct {
	payload    string
	signature  []byte
	trust      *SnapshotTrust
	keyID      string
	receivedAt time.Time
	deadline   time.Time
}

func (t *SnapshotTrust) Verify(snapshot domain.Snapshot, agent domain.AgentIdentity, now time.Time) (VerifiedSnapshot, error) {
	denied := VerifiedSnapshot{}
	parsed, err := workloadidentity.ParseAgentURI(agent.URI)
	if err != nil || parsed != agent || snapshot.Scope != t.scope || snapshot.Scope != agent.Scope || snapshot.DestinationID != agent.DestinationID || snapshot.Epoch != agent.Epoch || len(snapshot.Signature) != ed25519.SignatureSize {
		return denied, ErrSnapshot
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	key, ok := t.keys[snapshot.KeyID]
	if !ok || !key.active(now) {
		return denied, ErrSnapshot
	}
	signature := bytes.Clone(snapshot.Signature)
	payload, err := snapshotPayload(snapshot, now)
	if err != nil || !ed25519.Verify(key.PublicKey, signatureMessage(payload), signature) {
		return denied, ErrSnapshot
	}
	expiry := snapshot.ExpiresAt
	if snapshot.Policy.ExpiresAt.Before(expiry) {
		expiry = snapshot.Policy.ExpiresAt
	}
	if key.NotAfter.Before(expiry) {
		expiry = key.NotAfter
	}
	// Subtract the accepted wall-clock error and anchor the remaining lifetime
	// to the receiver's monotonic clock so a wall-clock rollback cannot extend it.
	remaining := expiry.Sub(now) - domain.MaxClockSkew
	if remaining <= 0 {
		return denied, ErrSnapshot
	}
	return VerifiedSnapshot{payload: string(payload), signature: signature, trust: t, keyID: snapshot.KeyID, receivedAt: now, deadline: now.Add(remaining)}, nil
}
func (v VerifiedSnapshot) Snapshot() (domain.Snapshot, error) {
	if v.trust == nil || v.payload == "" {
		return domain.Snapshot{}, ErrSnapshot
	}
	var snapshot domain.Snapshot
	if err := json.Unmarshal([]byte(v.payload), &snapshot); err != nil {
		return domain.Snapshot{}, ErrSnapshot
	}
	snapshot.Signature = bytes.Clone(v.signature)
	return snapshot, nil
}

// ValidAt also observes key removal or shortening after the initial verification.
func (v VerifiedSnapshot) ValidAt(now time.Time) error {
	if v.trust == nil || !v.deadline.After(now) || now.Before(v.receivedAt.Add(-domain.MaxClockSkew)) {
		return ErrSnapshot
	}
	v.trust.mu.RLock()
	defer v.trust.mu.RUnlock()
	key, ok := v.trust.keys[v.keyID]
	if !ok || !key.active(now) || !key.NotAfter.Add(-domain.MaxClockSkew).After(now) {
		return ErrSnapshot
	}
	return nil
}

// Deadline is a conservative authorization ceiling, not permission to use routes.
func (v VerifiedSnapshot) Deadline() time.Time { return v.deadline }

// Version 1 signs compact encoding/json output of Snapshot with Signature=nil.
// The fixed schema and domain separator are shared by both Go protocol peers.
func snapshotPayload(snapshot domain.Snapshot, now time.Time) ([]byte, error) {
	if !snapshot.Valid() || !domain.ValidIdentitySegment(snapshot.DestinationID) || snapshot.Epoch == 0 || snapshot.Sequence == 0 || !snapshot.Full || snapshot.NextCursor != "" || !validRevision(snapshot.Revision) || !validRevision(snapshot.Policy.Revision) || len(snapshot.Endpoints)+len(snapshot.Tombstones) > domain.MaxSnapshotRecords || len(snapshot.Policy.Grants) > domain.MaxSnapshotRecords {
		return nil, ErrSnapshot
	}
	if !validSnapshotWindow(snapshot.IssuedAt, snapshot.ExpiresAt, now) || !validSnapshotWindow(snapshot.IssuedAt, snapshot.Policy.ExpiresAt, now) {
		return nil, ErrSnapshot
	}
	sameOrganization := func(scope domain.Scope) bool { return scope.Valid() && scope.OrgID == snapshot.OrgID }
	for _, endpoint := range snapshot.Endpoints {
		if !sameOrganization(endpoint.Scope) {
			return nil, ErrSnapshot
		}
	}
	for _, tombstone := range snapshot.Tombstones {
		if !sameOrganization(tombstone.Scope) {
			return nil, ErrSnapshot
		}
	}
	for _, grant := range snapshot.Policy.Grants {
		if !sameOrganization(grant.Scope) || !sameOrganization(grant.Source.Scope) {
			return nil, ErrSnapshot
		}
	}

	snapshot.Signature = nil
	payload, err := json.Marshal(snapshot)
	// An Ed25519 signature adds fewer than 100 bytes to the unsigned JSON form.
	if err != nil || len(payload) > domain.MaxSnapshotBytes-100 {
		return nil, ErrSnapshot
	}
	return payload, nil
}
func validRevision(value string) bool {
	return value != "" && len(value) <= 512 && utf8.ValidString(value)
}
func signatureMessage(payload []byte) []byte {
	return append([]byte(snapshotSignatureContext), payload...)
}

func validSnapshotWindow(issuedAt, expiresAt, now time.Time) bool {
	return !issuedAt.IsZero() && !issuedAt.After(now.Add(domain.MaxClockSkew)) && expiresAt.After(now) && expiresAt.After(issuedAt) && expiresAt.Sub(issuedAt) <= domain.MaxPolicyLease
}
