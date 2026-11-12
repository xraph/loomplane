// Package adoptiontransfer records ownership-transfer evidence for recovery.
// It cannot activate ownership, revoke credentials or issue provider writes.
package adoptiontransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid               = errors.New("invalid transfer record")
	ErrConflict              = errors.New("transfer revision or immutable identity conflicts")
	ErrLease                 = errors.New("current worker lease required")
	ErrUnresolved            = errors.New("original attempt outcome remains unresolved")
	ErrReceipt               = errors.New("receipt does not match pending evidence")
	ErrTransition            = errors.New("invalid evidence transition")
	ErrCanceled              = errors.New("transfer cancellation prevents new work")
	ErrExpired               = errors.New("accepted transfer deadline expired")
	ErrStaleEvidence         = errors.New("prior evidence requires a fresh review")
	ErrQualificationRequired = errors.New("durable authority and actual writer fencing remain unqualified")
)

type Direction string

const (
	Forward Direction = "forward"
	Reverse Direction = "reverse"
)

type Source struct {
	Provider, AccountIdentity, ClusterID, ClusterIncarnation string
	Namespace, NamespaceUID, AttachmentID, ConnectionID      string
	AttachmentRevision, ConnectionGeneration, TrustRevision  int64
}
type IntentFields struct {
	SchemaVersion                                                         int
	ID, OperationID, OrgID, ProjectID, FleetID, InstanceID, EnvironmentID string
	Generation, PriorEpoch, Epoch                                         int64
	Direction                                                             Direction
	PredecessorID, ActorDigest, ReviewID, ReviewDigest                    string
	ReviewRevision                                                        int64
	AuthorityID                                                           string
	AuthorityRevision                                                     int64
	OldAuthorityID, NewAuthorityID, OldWriterUID                          string
	Source                                                                Source
	ReleaseID, ReleaseDigest, InputDigest                                 string
	AcceptedAt, Deadline                                                  time.Time
}

// Intent is a value copy. Its digest identifies claims, not their truth or authority.
type Intent struct {
	fields IntentFields
	digest string
}

func (i Intent) Fields() IntentFields { return i.fields }
func (i Intent) Digest() string       { return i.digest }
func NewIntent(f IntentFields) (Intent, error) {
	if f.SchemaVersion != 1 {
		return Intent{}, ErrInvalid
	}
	for _, value := range []string{f.ID, f.OperationID, f.OrgID, f.ProjectID, f.FleetID, f.InstanceID, f.EnvironmentID, f.ReviewID, f.AuthorityID, f.OldAuthorityID, f.NewAuthorityID, f.OldWriterUID, f.ReleaseID, f.Source.Provider, f.Source.AccountIdentity, f.Source.ClusterID, f.Source.ClusterIncarnation, f.Source.Namespace, f.Source.NamespaceUID, f.Source.AttachmentID, f.Source.ConnectionID} {
		if !validID(value) {
			return Intent{}, ErrInvalid
		}
	}
	for _, value := range []string{f.ActorDigest, f.ReviewDigest, f.ReleaseDigest, f.InputDigest} {
		if !validDigest(value) {
			return Intent{}, ErrInvalid
		}
	}
	if f.Generation < 1 || f.PriorEpoch < 1 || f.Epoch <= f.PriorEpoch || f.Epoch-f.PriorEpoch != 1 || f.ReviewRevision < 1 || f.AuthorityRevision < 1 || f.Source.AttachmentRevision < 1 || f.Source.ConnectionGeneration < 1 || f.Source.TrustRevision < 1 || f.OldAuthorityID == f.NewAuthorityID || !validTime(f.AcceptedAt) || !validTime(f.Deadline) || !f.Deadline.After(f.AcceptedAt) {
		return Intent{}, ErrInvalid
	}
	switch f.Direction {
	case Forward:
		if f.PredecessorID != "" {
			return Intent{}, ErrInvalid
		}
	case Reverse:
		if !validID(f.PredecessorID) || f.PredecessorID == f.ID {
			return Intent{}, ErrInvalid
		}
	default:
		return Intent{}, ErrInvalid
	}
	f.AcceptedAt = canonicalTime(f.AcceptedAt)
	f.Deadline = canonicalTime(f.Deadline)
	if !validTime(f.AcceptedAt) || !validTime(f.Deadline) {
		return Intent{}, ErrInvalid
	}
	digest := hash("fleet-adoption-intent/v1", f)
	if digest == "" {
		return Intent{}, ErrInvalid
	}
	return Intent{fields: f, digest: digest}, nil
}

type Step string

const (
	Fence     Step = "fence"
	Inventory Step = "inventory"
	Import    Step = "import"
	Plan      Step = "plan"
)

type Outcome string

const (
	Confirmed Outcome = "confirmed"
	Unknown   Outcome = "unknown"
	NoEffect  Outcome = "no_effect"
)

// ReceiptFields carries claimed observations. A future trusted domain verifier
// must reload and qualify the proof before any effect or authority publication.
type ReceiptFields struct {
	SchemaVersion                                                    int
	ParentReceiptDigest                                              string
	ID, IntentDigest, AttemptID                                      string
	Step                                                             Step
	Outcome                                                          Outcome
	EvidenceDigest, ParentReceiptID, InventoryDigest, SnapshotDigest string
	ObservedAt                                                       time.Time
	Complete                                                         bool
	Changes                                                          int64
}
type Receipt struct {
	fields ReceiptFields
	digest string
}

func (r Receipt) Fields() ReceiptFields { return r.fields }
func (r Receipt) Digest() string        { return r.digest }
func NewReceipt(f ReceiptFields) (Receipt, error) {
	if f.SchemaVersion != 1 {
		return Receipt{}, ErrInvalid
	}
	if !validID(f.ID) || !validID(f.AttemptID) || !validDigest(f.IntentDigest) || !validDigest(f.EvidenceDigest) || !validTime(f.ObservedAt) || f.Changes < 0 {
		return Receipt{}, ErrInvalid
	}
	if (f.ParentReceiptID == "") != (f.ParentReceiptDigest == "") || (f.ParentReceiptID != "" && (!validID(f.ParentReceiptID) || !validDigest(f.ParentReceiptDigest))) {
		return Receipt{}, ErrInvalid
	}
	for _, value := range []string{f.InventoryDigest, f.SnapshotDigest} {
		if value != "" && !validDigest(value) {
			return Receipt{}, ErrInvalid
		}
	}
	if !slices.Contains([]Step{Fence, Inventory, Import, Plan}, f.Step) || !slices.Contains([]Outcome{Confirmed, Unknown, NoEffect}, f.Outcome) {
		return Receipt{}, ErrInvalid
	}
	f.ObservedAt = canonicalTime(f.ObservedAt)
	if !validTime(f.ObservedAt) {
		return Receipt{}, ErrInvalid
	}
	digest := hash("fleet-adoption-receipt/v1", f)
	if digest == "" {
		return Receipt{}, ErrInvalid
	}
	return Receipt{fields: f, digest: digest}, nil
}

type Phase string

const (
	Accepted          Phase = "accepted"
	FenceRecorded     Phase = "fence_evidence_recorded"
	InventoryRecorded Phase = "inventory_evidence_recorded"
	ImportRecorded    Phase = "import_evidence_recorded"
	EvidenceRecorded  Phase = "evidence_recorded"
)

type Lease struct {
	Token     string
	ExpiresAt time.Time
}
type Pending struct {
	Step      Step
	AttemptID string
	StartedAt time.Time
	Unknown   bool
}
type State struct {
	intent      Intent
	revision    uint64
	phase       Phase
	lease       Lease
	pending     Pending
	canceled    bool
	receipts    []Receipt
	attempts    []string
	leaseTokens []string
	lastAt      time.Time
}

func (s State) Intent() Intent      { return s.intent }
func (s State) Revision() uint64    { return s.revision }
func (s State) Phase() Phase        { return s.phase }
func (s State) Lease() Lease        { return s.lease }
func (s State) Pending() Pending    { return s.pending }
func (s State) Canceled() bool      { return s.canceled }
func (s State) Receipts() []Receipt { return append([]Receipt(nil), s.receipts...) }
func NewState(i Intent) (State, error) {
	checked, err := NewIntent(i.fields)
	if err != nil || checked.digest != i.digest {
		return State{}, ErrInvalid
	}
	return State{intent: i, revision: 1, phase: Accepted, lastAt: i.fields.AcceptedAt}, nil
}

type CommandKind string

const (
	Claim           CommandKind = "claim_worker"
	Begin           CommandKind = "begin_observation"
	Record          CommandKind = "record_observation"
	Cancel          CommandKind = "request_cancel"
	Activate        CommandKind = "activate"
	ReverseTransfer CommandKind = "reverse_transfer"
)

type Command struct {
	Kind             CommandKind
	ExpectedRevision uint64
	LeaseToken       string
	Lease            Lease
	Step             Step
	AttemptID        string
	Receipt          Receipt
}

// Reduce is a proposed pure journal transition, not authorization or a durable CAS.
func Reduce(s State, c Command, now time.Time) (State, error) {
	// No supplied receipt, field, or lease can enable ownership in this slice.
	if c.Kind == Activate || c.Kind == ReverseTransfer {
		return s, ErrQualificationRequired
	}
	now = canonicalTime(now)
	if s.revision == 0 || !validTime(now) || now.Before(s.lastAt) {
		return s, ErrInvalid
	}
	if c.ExpectedRevision != s.revision || s.revision == ^uint64(0) {
		return s, ErrConflict
	}
	next := s
	next.receipts = s.Receipts()
	next.attempts = slices.Clone(s.attempts)
	next.leaseTokens = slices.Clone(s.leaseTokens)
	if c.Kind == Claim {
		expiry := canonicalTime(c.Lease.ExpiresAt)
		if s.lease.ExpiresAt.After(now) || !validID(c.Lease.Token) || slices.Contains(s.leaseTokens, c.Lease.Token) || !validTime(expiry) || !expiry.After(now) || expiry.After(now.Add(5*time.Minute)) {
			return s, ErrLease
		}
		next.lease = Lease{Token: c.Lease.Token, ExpiresAt: expiry}
		next.leaseTokens = append(next.leaseTokens, c.Lease.Token)
		if next.pending.AttemptID != "" {
			next.pending.Unknown = true
		}
	} else {
		if c.LeaseToken == "" || c.LeaseToken != s.lease.Token || !s.lease.ExpiresAt.After(now) {
			return s, ErrLease
		}
		switch c.Kind {
		case Cancel:
			if s.canceled {
				return s, nil
			}
			next.canceled = true
		case Begin:
			if s.canceled {
				return s, ErrCanceled
			}
			if s.pending.AttemptID != "" {
				return s, ErrUnresolved
			}
			if !s.intent.fields.Deadline.After(now) {
				return s, ErrExpired
			}
			if c.Step != nextStep(s.phase) || c.Step == "" {
				return s, ErrTransition
			}
			if !validID(c.AttemptID) {
				return s, ErrInvalid
			}
			if slices.Contains(s.attempts, c.AttemptID) {
				return s, ErrConflict
			}
			if prior, ok := lastConfirmed(s.receipts); ok && now.Sub(prior.fields.ObservedAt) > 5*time.Minute {
				return s, ErrStaleEvidence
			}
			next.pending = Pending{Step: c.Step, AttemptID: c.AttemptID, StartedAt: now}
			next.attempts = append(next.attempts, c.AttemptID)
		case Record:
			for _, saved := range s.receipts {
				if saved.fields.ID == c.Receipt.fields.ID {
					if saved == c.Receipt {
						return s, nil
					}
					return s, ErrConflict
				}
			}
			if err := checkReceipt(s, c.Receipt, now); err != nil {
				return s, err
			}
			next.receipts = append(next.receipts, c.Receipt)
			switch c.Receipt.fields.Outcome {
			case Unknown:
				next.pending.Unknown = true
			case NoEffect:
				next.pending = Pending{}
			case Confirmed:
				next.phase = phaseAfter(c.Receipt.fields.Step)
				next.pending = Pending{}
			}
		default:
			return s, ErrTransition
		}
	}
	next.revision++
	next.lastAt = now
	return next, nil
}

func checkReceipt(s State, r Receipt, now time.Time) error {
	f := r.fields
	checked, err := NewReceipt(f)
	if err != nil || checked != r || s.pending.AttemptID == "" || f.IntentDigest != s.intent.digest || f.AttemptID != s.pending.AttemptID || f.Step != s.pending.Step || f.ObservedAt.Before(s.pending.StartedAt) || f.ObservedAt.After(now) {
		return ErrReceipt
	}
	parent := ""
	parentDigest := ""
	if len(s.receipts) > 0 {
		parent = s.receipts[len(s.receipts)-1].fields.ID
		parentDigest = s.receipts[len(s.receipts)-1].digest
	}
	if f.ParentReceiptID != parent || f.ParentReceiptDigest != parentDigest {
		return ErrReceipt
	}
	if f.Outcome != Confirmed {
		return nil
	}
	if !f.Complete {
		return ErrReceipt
	}
	if prior, ok := lastConfirmed(s.receipts); ok && f.ObservedAt.Before(prior.fields.ObservedAt) {
		return ErrReceipt
	}
	switch f.Step {
	case Fence:
	case Inventory:
		if !validDigest(f.InventoryDigest) {
			return ErrReceipt
		}
	case Import, Plan:
		inventory, ok := confirmedStep(s.receipts, Inventory)
		if !ok || !validDigest(f.SnapshotDigest) || f.InventoryDigest != inventory.fields.InventoryDigest {
			return ErrReceipt
		}
		if f.Step == Plan {
			imported, ok := confirmedStep(s.receipts, Import)
			if !ok || f.SnapshotDigest != imported.fields.SnapshotDigest || f.Changes != 0 {
				return ErrReceipt
			}
		}
	default:
		return ErrReceipt
	}
	return nil
}
func nextStep(p Phase) Step {
	switch p {
	case Accepted:
		return Fence
	case FenceRecorded:
		return Inventory
	case InventoryRecorded:
		return Import
	case ImportRecorded:
		return Plan
	default:
		return ""
	}
}
func phaseAfter(step Step) Phase {
	switch step {
	case Fence:
		return FenceRecorded
	case Inventory:
		return InventoryRecorded
	case Import:
		return ImportRecorded
	case Plan:
		return EvidenceRecorded
	default:
		return ""
	}
}
func lastConfirmed(history []Receipt) (Receipt, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].fields.Outcome == Confirmed {
			return history[i], true
		}
	}
	return Receipt{}, false
}
func confirmedStep(history []Receipt, step Step) (Receipt, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].fields.Step == step && history[i].fields.Outcome == Confirmed {
			return history[i], true
		}
	}
	return Receipt{}, false
}
func validID(v string) bool {
	return v != "" && len(v) <= 512 && utf8.ValidString(v) && strings.TrimSpace(v) == v && !strings.ContainsAny(v, "\r\n\t")
}
func validDigest(v string) bool {
	if len(v) != 64 || strings.ToLower(v) != v {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}
func validTime(v time.Time) bool          { return !v.IsZero() && v.Year() >= 1 && v.Year() <= 9999 }
func canonicalTime(v time.Time) time.Time { return v.Round(0).UTC() }
func hash(domain string, v any) string {
	raw, err := json.Marshal(struct {
		Domain  string
		Payload any
	}{domain, v})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
