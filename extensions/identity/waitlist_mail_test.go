package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/authsome/id"
	"github.com/xraph/authsome/plugins/waitlist"
	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/notifications"
)

// fakeWaitlistStore records status changes and serves one entry.
type fakeWaitlistStore struct {
	waitlist.Store
	entry     *waitlist.WaitlistEntry
	updateErr error
	updated   []waitlist.WaitlistStatus
}

func (f *fakeWaitlistStore) UpdateEntryStatus(_ context.Context, _ id.WaitlistID, s waitlist.WaitlistStatus, _ string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = append(f.updated, s)
	return nil
}

func (f *fakeWaitlistStore) GetEntry(context.Context, id.WaitlistID) (*waitlist.WaitlistEntry, error) {
	return f.entry, nil
}

func TestApprovalMailer(t *testing.T) {
	boom := errors.New("database is down")
	for name, tc := range map[string]struct {
		status    waitlist.WaitlistStatus
		updateErr error
		sendErr   error
		wantSent  []string
		wantErr   error
	}{
		"approve sends once":         {status: waitlist.StatusApproved, wantSent: []string{"ada@example.com"}},
		"reject sends nothing":       {status: waitlist.StatusRejected},
		"update error is returned":   {status: waitlist.StatusApproved, updateErr: boom, wantErr: boom},
		"mail disabled is ignored":   {status: waitlist.StatusApproved, sendErr: notifications.ErrMailDisabled, wantSent: []string{"ada@example.com"}},
		"other send errors are kept": {status: waitlist.StatusApproved, sendErr: errors.New("smtp down"), wantSent: []string{"ada@example.com"}},
	} {
		t.Run(name, func(t *testing.T) {
			inner := &fakeWaitlistStore{entry: &waitlist.WaitlistEntry{Email: "ada@example.com"}, updateErr: tc.updateErr}
			var sent []string
			m := approvalMailer{Store: inner, log: forge.NewNoopLogger(), send: func(_ context.Context, email string) error {
				sent = append(sent, email)
				return tc.sendErr
			}}
			err := m.UpdateEntryStatus(context.Background(), id.NewWaitlistID(), tc.status, "")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if len(sent) != len(tc.wantSent) || (len(sent) == 1 && sent[0] != tc.wantSent[0]) {
				t.Fatalf("sent %v, want %v", sent, tc.wantSent)
			}
			if tc.updateErr == nil && len(inner.updated) != 1 {
				t.Fatalf("inner store updated %d times", len(inner.updated))
			}
		})
	}
}
