package platformcloud

import (
	"context"
	"errors"

	"github.com/xraph/forge"

	"github.com/xraph/loomplane/internal/notifications"
	"github.com/xraph/loomplane/internal/platformcloud"
)

// ownerMailer emails the platform owners when a host's client certificate
// enters a warning stage. It reaches the owners' own addresses only: no
// organization, inbox item or webhook, so host details never reach a
// workspace. A failure is logged and never fails the sweep, which has
// already recorded the stage.
type ownerMailer struct {
	owners func(ctx context.Context) ([]string, error)
	send   func(ctx context.Context, email string, n notifications.HostCertificateExpiry) error
	log    forge.Logger
}

var _ platformcloud.OwnerNotifier = ownerMailer{}

func (m ownerMailer) HostCertificateExpiring(ctx context.Context, n platformcloud.CertificateNotice) {
	emails, err := m.owners(ctx)
	if err != nil {
		m.log.Error("find the platform owners to warn about a host certificate", forge.F("host", n.HostID), forge.F("error", err.Error()))
		return
	}
	if len(emails) == 0 {
		m.log.Warn("no platform owner to warn about a host certificate", forge.F("host", n.HostID))
		return
	}
	notice := notifications.HostCertificateExpiry{HostID: n.HostID, HostName: n.HostName, ExpiresAt: n.ExpiresAt, DaysLeft: n.DaysLeft, Rotating: n.Rotating}
	failed := 0
	for _, email := range emails {
		err := m.send(ctx, email, notice)
		if errors.Is(err, notifications.ErrMailDisabled) {
			m.log.Warn("mail is off, so no platform owner was emailed about a host certificate", forge.F("host", n.HostID))
			return
		}
		if err != nil {
			failed++
		}
	}
	// Addresses stay out of the log.
	if failed > 0 {
		m.log.Error("email platform owners about a host certificate", forge.F("host", n.HostID),
			forge.F("failed", failed), forge.F("owners", len(emails)))
	}
}
