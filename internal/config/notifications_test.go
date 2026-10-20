package config

import (
	"strings"
	"testing"
)

func TestNotificationConfigLoadsSMTPAndRejectsUnsafeSettings(t *testing.T) {
	cfg, err := FromSource(mapGetter{"database.url": testDSN, "notifications.smtp_host": "mail.example.com", "notifications.smtp_port": "465", "notifications.from": "accounts@example.com", "notifications.smtp_username": "sender", "notifications.smtp_password": "test-password", "notifications.smtp_tls": true})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Notifications.MailEnabled() || cfg.Notifications.SMTPPort != 465 || !cfg.Notifications.SMTPTLS || cfg.Notifications.SMTPPassword != "test-password" {
		t.Fatal("SMTP settings were not loaded")
	}
	for _, test := range []struct {
		name              string
		edit              func(*NotificationsConfig)
		environment, want string
	}{
		{"cleartext production", func(c *NotificationsConfig) { c.SMTPTLS = false }, EnvProduction, "TLS"},
		{"header injection", func(c *NotificationsConfig) { c.From = "accounts@example.com\r\nBcc: victim@example.com" }, EnvDevelopment, "single email"},
		{"partial credentials", func(c *NotificationsConfig) { c.SMTPPassword = "" }, EnvDevelopment, "together"},
		{"port range", func(c *NotificationsConfig) { c.SMTPPort = 65536 }, EnvDevelopment, "port"},
		{"partial disabled mail", func(c *NotificationsConfig) { c.SMTPHost = "" }, EnvDevelopment, "host"},
	} {
		t.Run(test.name, func(t *testing.T) {
			edited := cfg.Notifications
			test.edit(&edited)
			if err := edited.Validate(test.environment); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected rejection containing %q: %v", test.want, err)
			}
		})
	}
}
