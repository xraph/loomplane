// Package approvaladmission verifies the provenance of a group approval request.
// Provenance does not establish current permission or committed acceptance.
package approvaladmission

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.jetify.com/typeid/v2"

	"github.com/xraph/loomplane/internal/security"
)

const (
	Operation  = "placement-plan.accept/v1"
	KeyPurpose = "loomplane/group-approval-admission/v1"
	// MaxEnvelopeBytes includes base64 expansion of all claims. A 2,000-byte
	// note made entirely of JSON control characters needs 16,000 bytes alone.
	MaxEnvelopeBytes = 24 * 1024
	MaxNoteBytes     = 2000
	SessionSource    = "ordinary-session/v1"
	KeySource        = "ordinary-key/v1"
)

// ErrInvalid refuses missing, malformed or unsupported provenance.
var ErrInvalid = errors.New("group approval provenance is unavailable")

// Envelope is the opaque durable source certificate.
type Envelope string

// Request binds the request created by planning, never a caller's source label.
type Request struct {
	Scope
	RequestID      string
	GroupSetDigest string
	Note           string
	Actor          security.Principal
	AcceptBefore   time.Time
}

// Scope names the exact saved plan and review.
type Scope struct {
	OrgID         string `json:"org_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	PlanID        string `json:"plan_id"`
	Revision      string `json:"revision"`
}

// Admitter admits only a source captured by identity authentication.
type Admitter interface {
	AdmitPlanApproval(context.Context, Request) (Envelope, error)
}

// Verification checks provenance against installed trusted keys.
type Verification interface {
	Verify(Envelope, Expected) (Verified, error)
}

// Expected comes from trusted composition and the exact durable decision.
type Expected struct {
	AppID string
	Request
}

// Claims is a bounded transport value. Constructing it supplies no authority.
// Identity signs it only after checking its private authenticated source.
type Claims struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
	AppID     string `json:"app_id"`
	Scope
	RequestID      string    `json:"request_id"`
	GroupSetDigest string    `json:"group_set_digest"`
	Note           string    `json:"note"`
	Source         string    `json:"source"`
	UserID         string    `json:"user_id"`
	KeyID          string    `json:"key_id"`
	SessionID      string    `json:"session_id"`
	AdmittedAt     time.Time `json:"admitted_at"`
	AcceptBefore   time.Time `json:"accept_before"`
}

// Verified has no writable authority fields. Accessors return value copies.
type Verified struct{ claims Claims }

// Claims returns a copy of the authenticated transport claims.
func (v Verified) Claims() Claims { return v.claims }

// Actor returns the exact saved ordinary person or key identity.
func (v Verified) Actor() security.Principal {
	return security.Principal{UserID: v.claims.UserID, OrgID: v.claims.OrgID, KeyID: v.claims.KeyID}
}

// Verifier holds one installed trusted key. Rotation refuses old provenance.
type Verifier struct {
	key []byte
	kid string
}

// NewVerifier installs one trusted purpose-derived verification key.
func NewVerifier(key []byte) (*Verifier, error) {
	if len(key) != 32 {
		return nil, ErrInvalid
	}
	return &Verifier{key: slices.Clone(key), kid: KeyID(key)}, nil
}

// KeyID names a purpose-derived key without revealing it.
func KeyID(key []byte) string { sum := sha256.Sum256(key); return hex.EncodeToString(sum[:16]) }

// Payload validates and encodes unsigned claims. Only identity owns signing.
func Payload(c Claims) ([]byte, error) {
	if c.Version != 1 || c.Operation != Operation || !validID(c.AppID, "aapp") || !validScope(c.Scope) || !validID(c.RequestID, "dec") || !digest(c.GroupSetDigest) || !validID(c.UserID, "ausr") || len(c.Note) > MaxNoteBytes || !utf8.ValidString(c.Note) || strings.TrimSpace(c.Note) != c.Note || c.AdmittedAt.IsZero() || !c.AdmittedAt.Before(c.AcceptBefore) || c.AdmittedAt.Location() != time.UTC || c.AcceptBefore.Location() != time.UTC {
		return nil, ErrInvalid
	}
	switch c.Source {
	case SessionSource:
		if !validID(c.SessionID, "ases") || c.KeyID != "" {
			return nil, ErrInvalid
		}
	case KeySource:
		if !validID(c.KeyID, "akey") || c.SessionID != "" {
			return nil, ErrInvalid
		}
	default:
		return nil, ErrInvalid
	}
	return json.Marshal(c)
}

// Verify authenticates the envelope and compares every durable request field.
func (v *Verifier) Verify(envelope Envelope, expected Expected) (Verified, error) {
	if v == nil || len(v.key) != 32 || len(envelope) > MaxEnvelopeBytes || !ordinary(expected.Actor) {
		return Verified{}, ErrInvalid
	}
	parts := strings.Split(string(envelope), ".")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != v.kid {
		return Verified{}, ErrInvalid
	}
	mac, err := base64.RawURLEncoding.Strict().DecodeString(parts[3])
	if err != nil || len(mac) != sha256.Size || base64.RawURLEncoding.EncodeToString(mac) != parts[3] {
		return Verified{}, ErrInvalid
	}
	h := hmac.New(sha256.New, v.key)
	_, _ = h.Write([]byte(KeyPurpose + "\x00" + strings.Join(parts[:3], ".")))
	if !hmac.Equal(mac, h.Sum(nil)) {
		return Verified{}, ErrInvalid
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[2] {
		return Verified{}, ErrInvalid
	}
	var c Claims
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		return Verified{}, ErrInvalid
	}
	// Exact canonical encoding also rejects duplicate, missing and null fields,
	// alternate time/ID encodings, and trailing content.
	canonical, err := Payload(c)
	if err != nil || !bytes.Equal(canonical, payload) {
		return Verified{}, ErrInvalid
	}
	if c.AppID != expected.AppID || c.Scope != expected.Scope || c.RequestID != expected.RequestID || c.GroupSetDigest != expected.GroupSetDigest || c.Note != expected.Note || c.UserID != expected.Actor.UserID || c.OrgID != expected.Actor.OrgID || c.KeyID != expected.Actor.KeyID || !c.AcceptBefore.Equal(expected.AcceptBefore) {
		return Verified{}, ErrInvalid
	}
	return Verified{claims: c}, nil
}

func ordinary(p security.Principal) bool {
	return p.Kind == security.KindUser && p.OnBehalfOf == nil && p.Fleet == nil && !p.ReadOnly && validID(p.UserID, "ausr") && validID(p.OrgID, "aorg") && (p.KeyID == "" || validID(p.KeyID, "akey"))
}
func validID(s, prefix string) bool {
	id, err := typeid.Parse(s)
	return err == nil && id.Prefix() == prefix && id.String() == s && len(s) == len(prefix)+27
}
func digest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}
func validScope(s Scope) bool {
	return validID(s.OrgID, "aorg") && validID(s.ProjectID, "proj") && validID(s.EnvironmentID, "env") && validID(s.PlanID, "plan") && digest(s.Revision)
}

// GroupPin names one retained review and configuration in the full plan set.
type GroupPin struct {
	ID             string `json:"id"`
	Generation     int64  `json:"generation"`
	Revision       string `json:"revision"`
	ConfigRevision string `json:"config_revision"`
}

// GroupSetDigest binds the complete sorted set; it does not validate SQL facts.
func GroupSetDigest(scope Scope, groups []GroupPin) (string, error) {
	if !validScope(scope) || len(groups) == 0 {
		return "", ErrInvalid
	}
	groups = slices.Clone(groups)
	slices.SortFunc(groups, func(a, b GroupPin) int { return strings.Compare(a.ID, b.ID) })
	for i, g := range groups {
		if !validID(g.ID, "pgroup") || g.Generation < 1 || !digest(g.Revision) || !digest(g.ConfigRevision) || (i > 0 && groups[i-1].ID == g.ID) {
			return "", ErrInvalid
		}
	}
	raw, err := json.Marshal(struct {
		Version int        `json:"version"`
		Scope   Scope      `json:"scope"`
		Groups  []GroupPin `json:"groups"`
	}{1, scope, groups})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
