package identity

import (
	"strings"

	"github.com/xraph/authsome/hook"
	"github.com/xraph/authsome/plugins/notification"
	"github.com/xraph/authsome/plugins/waitlist"

	"github.com/xraph/loomplane/internal/config"
)

func recoveryNotifications(cfg config.Config) *notification.Plugin {
	return notification.New(notification.Config{
		AppName: "Loomplane", BaseURL: strings.TrimRight(cfg.PublicURL, "/"),
		PasswordResetPath: "/reset-password", EmailVerifyPath: "/verify-email",
		SignInPath: "/sign-in", Mappings: notificationMappings(),
	})
}

// notificationMappings turns off every mapping but recovery mail. Security
// mail bypasses deployment-alert preferences and stays email-only. Other
// default mappings include templates/channels Loomplane does not expose.
// A mapping that's off keeps its template and channels: nothing sends it,
// and the console's Event mappings tab shows what it would send.
func notificationMappings() map[string]*notification.Mapping {
	mappings := make(map[string]*notification.Mapping)
	for action, m := range notification.DefaultMappings() {
		mappings[action] = &notification.Mapping{Template: m.Template, Channels: m.Channels, Enabled: false}
	}
	// The waitlist plugin contributes its own mappings unless these exist.
	// Approval mail goes through approvalMailer instead.
	for action, m := range new(waitlist.Plugin).NotificationMappings() {
		mappings[action] = &notification.Mapping{Template: m.Template, Channels: m.Channels, Enabled: false}
	}
	mappings[hook.ActionPasswordReset] = &notification.Mapping{Template: "auth.password-reset", Channels: []string{"email"}, Enabled: true}
	mappings[hook.ActionPasswordChange] = &notification.Mapping{Template: "auth.password-changed", Channels: []string{"email"}, Enabled: true}
	mappings[hook.ActionEmailVerificationRequested] = &notification.Mapping{Template: "auth.email-verification", Channels: []string{"email"}, Enabled: true}
	return mappings
}
