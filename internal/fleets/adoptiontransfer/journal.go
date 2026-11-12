package adoptiontransfer

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"time"
)

const maxJournalBytes = 4 << 20

// Journal preserves reducer commands for restart recovery. Stored observations
// remain evidence claims until their owning domain verifies them.
type Journal struct {
	state   State
	entries []journalEntry
	digest  string
}

type journalCommand struct {
	Kind             CommandKind
	ExpectedRevision uint64
	LeaseToken       string
	Lease            Lease
	Step             Step
	AttemptID        string
	Receipt          *ReceiptFields `json:",omitempty"`
}

type journalEntry struct {
	Command journalCommand
	At      time.Time
}

type journalDocument struct {
	Version int            `json:"version"`
	Intent  IntentFields   `json:"intent"`
	Entries []journalEntry `json:"entries"`
	Digest  string         `json:"digest"`
}

func NewJournal(intent Intent) (Journal, error) {
	state, err := NewState(intent)
	if err != nil {
		return Journal{}, err
	}
	return Journal{state: state, entries: []journalEntry{}, digest: hash("fleet-adoption-journal-root/v1", intent.Fields())}, nil
}

func (j Journal) State() State { return j.state }

// Apply copies history before recording a successful transition. Replayed
// receipts and cancellation requests do not produce another journal entry.
func (j Journal) Apply(c Command, at time.Time) (Journal, error) {
	next, err := Reduce(j.state, c, at)
	if err != nil {
		return j, err
	}
	if next.Revision() == j.state.Revision() {
		return j, nil
	}
	wire := journalCommand{Kind: c.Kind, ExpectedRevision: c.ExpectedRevision}
	switch c.Kind {
	case Claim:
		wire.Lease = c.Lease
		wire.Lease.ExpiresAt = canonicalTime(wire.Lease.ExpiresAt)
	case Begin:
		wire.LeaseToken = c.LeaseToken
		wire.Step = c.Step
		wire.AttemptID = c.AttemptID
	case Record:
		wire.LeaseToken = c.LeaseToken
		fields := c.Receipt.Fields()
		wire.Receipt = &fields
	case Cancel:
		wire.LeaseToken = c.LeaseToken
	}
	entries := append(slices.Clone(j.entries), journalEntry{Command: wire, At: canonicalTime(at)})
	return Journal{state: next, entries: entries, digest: entryDigest(j.digest, entries[len(entries)-1])}, nil
}

func (j Journal) Encode() ([]byte, error) {
	if j.state.Revision() == 0 {
		return nil, ErrInvalid
	}
	document := journalDocument{Version: 1, Intent: j.state.Intent().Fields(), Entries: j.entries}
	document.Digest = j.digest
	raw, err := json.Marshal(document)
	if err != nil || len(raw) > maxJournalBytes {
		return nil, ErrInvalid
	}
	return raw, nil
}

func DecodeJournal(raw []byte) (Journal, error) {
	if len(raw) > maxJournalBytes {
		return Journal{}, ErrInvalid
	}
	var document journalDocument
	if err := decodeStrict(raw, &document); err != nil {
		return Journal{}, ErrInvalid
	}
	if document.Version != 1 || !validDigest(document.Digest) {
		return Journal{}, ErrInvalid
	}
	intent, err := NewIntent(document.Intent)
	if err != nil {
		return Journal{}, err
	}
	j, err := NewJournal(intent)
	if err != nil {
		return Journal{}, err
	}
	for _, entry := range document.Entries {
		command, decodeErr := entry.Command.decode()
		if decodeErr != nil {
			return Journal{}, decodeErr
		}
		next, applyErr := j.Apply(command, entry.At)
		if applyErr != nil || next.State().Revision() != j.State().Revision()+1 || entryDigest(j.digest, entry) != next.digest {
			return Journal{}, ErrInvalid
		}
		j = next
	}
	if j.digest != document.Digest {
		return Journal{}, ErrInvalid
	}
	return j, nil
}

func (c journalCommand) decode() (Command, error) {
	command := Command{Kind: c.Kind, ExpectedRevision: c.ExpectedRevision, LeaseToken: c.LeaseToken, Lease: c.Lease, Step: c.Step, AttemptID: c.AttemptID}
	if (c.Kind == Record) != (c.Receipt != nil) {
		return Command{}, ErrInvalid
	}
	if c.Receipt != nil {
		receipt, err := NewReceipt(*c.Receipt)
		if err != nil {
			return Command{}, err
		}
		command.Receipt = receipt
	}
	return command, nil
}

func entryDigest(parent string, entry journalEntry) string {
	return hash("fleet-adoption-journal-entry/v1", struct {
		Parent string
		Entry  journalEntry
	}{parent, entry})
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

// SourceKey excludes rotatable connection and attachment records. Two fleets
// referring to the same physical namespace must contend for the same claim.
func SourceKey(source Source) string {
	return hash("fleet-adoption-source/v1", struct{ Provider, Account, ClusterIncarnation, NamespaceUID string }{source.Provider, source.AccountIdentity, source.ClusterIncarnation, source.NamespaceUID})
}
