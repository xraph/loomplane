package identity

import (
	"testing"

	"github.com/xraph/authsome/hook"
)

func TestNotificationMappingsSendOnlyRecoveryMail(t *testing.T) {
	on := map[string]string{
		hook.ActionPasswordReset:              "auth.password-reset",
		hook.ActionPasswordChange:             "auth.password-changed",
		hook.ActionEmailVerificationRequested: "auth.email-verification",
	}
	mappings := notificationMappings()
	for action, m := range mappings {
		// The console's Event mappings tab joins every mapping's channels;
		// a null list breaks the whole page.
		if len(m.Channels) == 0 || m.Template == "" {
			t.Errorf("%s has no template or channels: %+v", action, m)
		}
		if want, ok := on[action]; ok {
			if !m.Enabled || m.Template != want || len(m.Channels) != 1 || m.Channels[0] != "email" {
				t.Errorf("%s should send %s by email only: %+v", action, want, m)
			}
		} else if m.Enabled {
			t.Errorf("%s is enabled", action)
		}
	}
	for _, action := range []string{hook.ActionWaitlistJoin, hook.ActionWaitlistApprove, hook.ActionWaitlistReject} {
		if m, ok := mappings[action]; !ok || m.Enabled {
			t.Errorf("%s must be present and off, so the waitlist plugin's own mapping doesn't apply", action)
		}
	}
}
