package dbexec

import (
	"errors"
	"strings"
	"unicode/utf8"
)

func ValidatePassword(password string) error {
	if len(password) < 26 || len(password) > 4096 || !utf8.ValidString(password) || strings.ContainsAny(password, "\x00\r\n") {
		return errors.New("database application password is invalid")
	}
	return nil
}
