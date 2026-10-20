package config

import (
	"errors"
	"net/mail"
	"strings"
)

// NotificationsConfig selects the SMTP transport. An empty host disables mail.
type NotificationsConfig struct {
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	From         string
	SMTPTLS      bool
}

func (n NotificationsConfig) MailEnabled() bool { return n.SMTPHost != "" }

// Validate refuses partial or unsafe production SMTP settings.
func (n NotificationsConfig) Validate(environment string) error {
	if !n.MailEnabled() {
		if n.SMTPUsername != "" || n.SMTPPassword != "" || n.From != "" {
			return errors.New("notifications: SMTP host is required when email settings are provided")
		}
		return nil
	}
	if n.SMTPPort < 1 || n.SMTPPort > 65535 || strings.ContainsAny(n.SMTPHost, "\r\n/@") {
		return errors.New("notifications: SMTP host and port are invalid")
	}
	parsed, err := mail.ParseAddress(n.From)
	if err != nil || parsed.Address != n.From || strings.ContainsAny(n.From, "\r\n") {
		return errors.New("notifications: from must be a single email address")
	}
	if (n.SMTPUsername == "") != (n.SMTPPassword == "") {
		return errors.New("notifications: SMTP username and password must be provided together")
	}
	if environment == EnvProduction && !n.SMTPTLS {
		return errors.New("notifications: production SMTP requires implicit TLS")
	}
	return nil
}
