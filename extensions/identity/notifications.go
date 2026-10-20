package identity

import (
	"strings"

	"github.com/xraph/authsome/hook"
	"github.com/xraph/authsome/plugins/notification"

	"github.com/xraph/loomplane/internal/config"
)

func recoveryNotifications(cfg config.Config) *notification.Plugin {
	// Security mail bypasses deployment-alert preferences and stays email-only.
	// Other default mappings include templates/channels Loomplane does not expose.
	mappings := make(map[string]*notification.Mapping)
	for action := range notification.DefaultMappings() {
		mappings[action] = &notification.Mapping{Enabled: false}
	}
	mappings[hook.ActionPasswordReset] = &notification.Mapping{Template: "auth.password-reset", Channels: []string{"email"}, Enabled: true}
	mappings[hook.ActionPasswordChange] = &notification.Mapping{Template: "auth.password-changed", Channels: []string{"email"}, Enabled: true}
	mappings[hook.ActionEmailVerificationRequested] = &notification.Mapping{Template: "auth.email-verification", Channels: []string{"email"}, Enabled: true}
	return notification.New(notification.Config{
		AppName: "Loomplane", BaseURL: strings.TrimRight(cfg.PublicURL, "/"),
		PasswordResetPath: "/reset-password", EmailVerifyPath: "/verify-email",
		SignInPath: "/sign-in", Mappings: mappings,
	})
}
