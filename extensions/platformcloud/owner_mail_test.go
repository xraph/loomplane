package platformcloud

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/platformcloud"
)

type sentMail struct {
	to string
	n  notifications.HostCertificateExpiry
}

func TestOwnerMailerEmailsEveryOwner(t *testing.T) {
	expires := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	var sent []sentMail
	m := ownerMailer{
		owners: func(context.Context) ([]string, error) { return []string{"a@example.com", "b@example.com"}, nil },
		send: func(_ context.Context, to string, n notifications.HostCertificateExpiry) error {
			sent = append(sent, sentMail{to, n})
			if to == "a@example.com" {
				return errors.New("smtp down") // one failure doesn't stop the rest
			}
			return nil
		},
		log: forge.NewNoopLogger(),
	}
	m.HostCertificateExpiring(context.Background(), platformcloud.CertificateNotice{HostID: "pchost_1", HostName: "fc-1", ExpiresAt: expires, DaysLeft: 21, Rotating: true})
	if got := []string{sent[0].to, sent[1].to}; len(sent) != 2 || !slices.Equal(got, []string{"a@example.com", "b@example.com"}) {
		t.Fatalf("sent %+v", sent)
	}
	want := notifications.HostCertificateExpiry{HostID: "pchost_1", HostName: "fc-1", ExpiresAt: expires, DaysLeft: 21, Rotating: true}
	if sent[1].n != want {
		t.Fatalf("notice %+v", sent[1].n)
	}
}

func TestOwnerMailerNeverFailsTheSweep(t *testing.T) {
	ctx := context.Background()
	notice := platformcloud.CertificateNotice{HostID: "pchost_1"}
	calls := 0
	send := func(context.Context, string, notifications.HostCertificateExpiry) error {
		calls++
		return notifications.ErrMailDisabled
	}
	// No owners can be read, or there are none: nothing is sent.
	ownerMailer{owners: func(context.Context) ([]string, error) { return nil, errors.New("warden down") }, send: send, log: forge.NewNoopLogger()}.HostCertificateExpiring(ctx, notice)
	ownerMailer{owners: func(context.Context) ([]string, error) { return nil, nil }, send: send, log: forge.NewNoopLogger()}.HostCertificateExpiring(ctx, notice)
	if calls != 0 {
		t.Fatalf("sent %d without owners", calls)
	}
	// Mail is off: one try, then it stops.
	ownerMailer{owners: func(context.Context) ([]string, error) { return []string{"a@example.com", "b@example.com"}, nil }, send: send, log: forge.NewNoopLogger()}.HostCertificateExpiring(ctx, notice)
	if calls != 1 {
		t.Fatalf("tried %d sends with mail off", calls)
	}
}
