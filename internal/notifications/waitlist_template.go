package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/xraph/authsome/bridge"
	"github.com/xraph/herald/id"
	heraldstore "github.com/xraph/herald/store"
	"github.com/xraph/herald/template"
)

const waitlistApprovedSlug = "auth.waitlist-approved"

// Same look as herald's own auth emails.
const waitlistApprovedHTML = `<!DOCTYPE html>
<html><body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
<h2 style="color: #333;">You're in</h2>
<p>Your spot on the {{.app_name}} waitlist was approved.</p>
<p><a href="{{.sign_up_url}}" style="display: inline-block; padding: 12px 24px; background-color: #4F46E5; color: #fff; text-decoration: none; border-radius: 6px;">Create your account</a></p>
<p style="color: #666; font-size: 14px;">If you didn't ask to join the waitlist, you can safely ignore this email.</p>
</body></html>`

// seededTemplate is one of Loomplane's own emails, added beside herald's
// defaults.
type seededTemplate struct {
	slug, name, category string
	variables            []template.Variable
	subject, html, text  string
}

func ownTemplates() []seededTemplate {
	return []seededTemplate{
		{
			slug: waitlistApprovedSlug, name: "Waitlist Approved", category: template.CategoryAuth,
			variables: []template.Variable{
				{Name: "app_name", Type: "string", Required: true},
				{Name: "sign_up_url", Type: "url", Required: true},
			},
			subject: "You're in: create your Loomplane account",
			html:    waitlistApprovedHTML,
			text:    "Your spot on the Loomplane waitlist was approved. Create your account here: {{.sign_up_url}}",
		},
		hostCertificateTemplate(),
	}
}

// seedTemplates adds herald's default templates and Loomplane's own emails.
// Existing templates are left alone, so a restart or a second replica
// changes nothing.
func (s *Service) seedTemplates(ctx context.Context) error {
	appID := s.cfg.Identity.AppID
	if err := s.herald.SeedDefaultTemplates(ctx, appID); err != nil {
		return err
	}
	for _, t := range ownTemplates() {
		if err := s.seedTemplate(ctx, appID, t); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) seedTemplate(ctx context.Context, appID string, t seededTemplate) error {
	existing, err := s.mailStore.GetTemplateBySlug(ctx, appID, t.slug, "email")
	if err != nil && !errors.Is(err, heraldstore.ErrTemplateNotFound) {
		return fmt.Errorf("look up the %s template: %w", t.slug, err)
	}
	if existing != nil {
		return nil
	}
	now := time.Now()
	tmpl := &template.Template{
		ID: id.NewTemplateID(), AppID: appID, Slug: t.slug,
		Name: t.name, Channel: "email", Category: t.category,
		IsSystem: true, Enabled: true, CreatedAt: now, UpdatedAt: now,
		Variables: t.variables,
	}
	if err := s.mailStore.CreateTemplate(ctx, tmpl); err != nil {
		return err
	}
	return s.mailStore.CreateVersion(ctx, &template.Version{
		ID: id.NewTemplateVersionID(), TemplateID: tmpl.ID, Locale: "en", Active: true,
		Subject: t.subject, HTML: t.html, Text: t.text,
		CreatedAt: now, UpdatedAt: now,
	})
}

// SendWaitlistApproved tells someone their waitlist entry was approved and
// where to create their account.
func (s *Service) SendWaitlistApproved(ctx context.Context, email string) error {
	link, err := url.Parse(s.cfg.PublicURL)
	if err != nil {
		return ErrInvalid
	}
	link = link.JoinPath("sign-up")
	link.RawQuery = "email=" + url.QueryEscape(email)
	return s.AuthBridge().Send(ctx, &bridge.HeraldSendRequest{Channel: "email", Template: waitlistApprovedSlug, To: []string{email}, Data: map[string]any{"app_name": "Loomplane", "sign_up_url": link.String()}})
}
