package notifications

import (
	"context"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/xraph/herald"
	"github.com/xraph/herald/template"
)

const hostCertificateSlug = "platform.host-certificate-expiring"

const hostCertificateHTML = `<!DOCTYPE html>
<html><body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
<h2 style="color: #333;">{{.headline}}</h2>
<p>The client certificate of Platform Cloud host <strong>{{.host_name}}</strong> ({{.host_id}}) {{.when}}.</p>
<p>{{.action}}</p>
<p><a href="{{.console_url}}" style="display: inline-block; padding: 12px 24px; background-color: #4F46E5; color: #fff; text-decoration: none; border-radius: 6px;">Open the hosts</a></p>
<p style="color: #666; font-size: 14px;">You get this because you're a platform owner of this Loomplane instance.</p>
</body></html>`

const hostCertificateText = `{{.headline}}

The client certificate of Platform Cloud host {{.host_name}} ({{.host_id}}) {{.when}}.

{{.action}}

Open the hosts: {{.console_url}}

You get this because you're a platform owner of this Loomplane instance.`

func hostCertificateTemplate() seededTemplate {
	return seededTemplate{
		slug: hostCertificateSlug, name: "Platform Host Certificate Expiring", category: template.CategorySystem,
		variables: []template.Variable{
			{Name: "headline", Type: "string", Required: true},
			{Name: "host_name", Type: "string", Required: true},
			{Name: "host_id", Type: "string", Required: true},
			{Name: "when", Type: "string", Required: true},
			{Name: "action", Type: "string", Required: true},
			{Name: "console_url", Type: "url", Required: true},
		},
		subject: "{{.headline}}",
		html:    hostCertificateHTML,
		text:    hostCertificateText,
	}
}

// HostCertificateExpiry is what a platform owner is told about a Platform
// Cloud host whose client certificate is running out. It names the host and
// nothing of any workspace.
type HostCertificateExpiry struct {
	HostID    string
	HostName  string
	ExpiresAt time.Time
	DaysLeft  int
	Rotating  bool
}

// SendHostCertificateExpiring emails one platform owner about a host's
// client certificate. It goes to the address alone, under no organization,
// so nothing of it reaches a workspace's inbox, preferences or webhooks.
func (s *Service) SendHostCertificateExpiring(ctx context.Context, email string, n HostCertificateExpiry) error {
	if !s.MailEnabled() {
		return ErrMailDisabled
	}
	if address, err := mail.ParseAddress(email); err != nil || address.Address != email || strings.ContainsAny(email, "\r\n") {
		return ErrInvalid
	}
	link, err := url.Parse(s.cfg.PublicURL)
	if err != nil {
		return ErrInvalid
	}
	res, err := s.herald.Send(ctx, &herald.SendRequest{
		AppID: s.cfg.Identity.AppID, Channel: "email", Template: hostCertificateSlug, To: []string{email},
		Data: hostCertificateData(n, link.JoinPath("platform", "cloud").String(), time.Now()),
	})
	return deliveryError(res, err)
}

// hostCertificateData words the email for the stage the certificate is in.
// The host name is the agent's, so it is kept out of the subject.
func hostCertificateData(n HostCertificateExpiry, consoleURL string, now time.Time) map[string]any {
	expires := n.ExpiresAt.UTC().Format("2 January 2006 15:04 UTC")
	headline, when := "", ""
	switch {
	case !n.ExpiresAt.After(now):
		headline = "A Platform Cloud host certificate has expired"
		when = "expired on " + expires
	case n.DaysLeft == 1:
		headline = "A Platform Cloud host certificate expires tomorrow"
		when = "expires on " + expires
	case n.DaysLeft == 0:
		headline = "A Platform Cloud host certificate expires today"
		when = "expires on " + expires
	default:
		headline = fmt.Sprintf("A Platform Cloud host certificate expires in %d days", n.DaysLeft)
		when = "expires on " + expires
	}
	action := "Rotate it from the hosts table in the console. The host keeps using its current certificate until it switches."
	if !n.ExpiresAt.After(now) {
		action = "Rotate it from the hosts table in the console as soon as you can."
	}
	if n.Rotating {
		action = "A new certificate is already waiting. The host switches to it at its next check, so make sure the host is up and reachable."
	}
	return map[string]any{
		"headline": headline, "host_name": n.HostName, "host_id": n.HostID,
		"when": when, "action": action, "console_url": consoleURL,
	}
}
