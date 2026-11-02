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

// seedTemplates adds herald's default templates and the waitlist approval
// email. Existing templates are left alone, so a restart or a second replica
// changes nothing.
func (s *Service) seedTemplates(ctx context.Context) error {
	appID := s.cfg.Identity.AppID
	if err := s.herald.SeedDefaultTemplates(ctx, appID); err != nil {
		return err
	}
	existing, err := s.mailStore.GetTemplateBySlug(ctx, appID, waitlistApprovedSlug, "email")
	if err != nil && !errors.Is(err, heraldstore.ErrTemplateNotFound) {
		return fmt.Errorf("look up the waitlist approval template: %w", err)
	}
	if existing != nil {
		return nil
	}
	now := time.Now()
	tmpl := &template.Template{
		ID: id.NewTemplateID(), AppID: appID, Slug: waitlistApprovedSlug,
		Name: "Waitlist Approved", Channel: "email", Category: template.CategoryAuth,
		IsSystem: true, Enabled: true, CreatedAt: now, UpdatedAt: now,
		Variables: []template.Variable{
			{Name: "app_name", Type: "string", Required: true},
			{Name: "sign_up_url", Type: "url", Required: true},
		},
	}
	if err := s.mailStore.CreateTemplate(ctx, tmpl); err != nil {
		return err
	}
	return s.mailStore.CreateVersion(ctx, &template.Version{
		ID: id.NewTemplateVersionID(), TemplateID: tmpl.ID, Locale: "en", Active: true,
		Subject:   "You're in: create your Loomplane account",
		HTML:      waitlistApprovedHTML,
		Text:      "Your spot on the Loomplane waitlist was approved. Create your account here: {{.sign_up_url}}",
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
