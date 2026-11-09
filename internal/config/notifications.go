package config

import (
	"errors"
	"net/mail"
	"strings"
)

// NotificationsConfig selects the mail transport: Resend when ResendAPIKey is
// set, a directory of .eml files when MailDir is, otherwise SMTP. With none
// of an API key, a mail directory or an SMTP host, mail is off.
type NotificationsConfig struct {
	ResendAPIKey string
	// MailDir is where mail is written instead of sent, one .eml file per
	// message, for local development. Not allowed in production.
	MailDir      string
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	From         string
	SMTPTLS      bool
}

func (n NotificationsConfig) MailEnabled() bool {
	return n.ResendAPIKey != "" || n.SMTPHost != "" || n.MailDir != ""
}

// Validate refuses partial or unsafe production mail settings.
func (n NotificationsConfig) Validate(environment string) error {
	if !n.MailEnabled() {
		if n.SMTPUsername != "" || n.SMTPPassword != "" || n.From != "" {
			return errors.New("notifications: SMTP host, Resend API key or mail directory is required when email settings are provided")
		}
		return nil
	}
	if n.MailDir != "" {
		if n.ResendAPIKey != "" || n.SMTPHost != "" {
			return errors.New("notifications: set only one of the Resend API key, an SMTP host or a mail directory")
		}
		if environment == EnvProduction {
			return errors.New("notifications: a mail directory is for development; production needs SMTP or Resend")
		}
		if n.From == "" {
			return nil
		}
		return n.validateFrom()
	}
	if n.ResendAPIKey != "" {
		if n.SMTPHost != "" {
			return errors.New("notifications: set either the Resend API key or an SMTP host, not both")
		}
		return n.validateFrom()
	}
	if n.SMTPPort < 1 || n.SMTPPort > 65535 || strings.ContainsAny(n.SMTPHost, "\r\n/@") {
		return errors.New("notifications: SMTP host and port are invalid")
	}
	if err := n.validateFrom(); err != nil {
		return err
	}
	if (n.SMTPUsername == "") != (n.SMTPPassword == "") {
		return errors.New("notifications: SMTP username and password must be provided together")
	}
	if environment == EnvProduction && !n.SMTPTLS {
		return errors.New("notifications: production SMTP requires implicit TLS")
	}
	return nil
}

func (n NotificationsConfig) validateFrom() error {
	parsed, err := mail.ParseAddress(n.From)
	if err != nil || parsed.Address != n.From || strings.ContainsAny(n.From, "\r\n") {
		return errors.New("notifications: from must be a single email address")
	}
	return nil
}
