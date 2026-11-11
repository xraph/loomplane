package cloudinfra

import (
	"errors"
	"time"
)

var errNamespaceAuthorizationInstant = errors.New("invalid namespace authorization instant")

// splitNamespaceAuthorizationInstant preserves the exact decision time across
// PostgreSQL's microsecond timestamp representation. It never rounds upward.
func splitNamespaceAuthorizationInstant(at time.Time) (time.Time, int16, error) {
	if at.IsZero() {
		return time.Time{}, 0, errNamespaceAuthorizationInstant
	}
	at = at.UTC()
	micros := at.Truncate(time.Microsecond)
	nanos := int64(at.Sub(micros))
	if nanos < 0 || nanos > 999 {
		return time.Time{}, 0, errNamespaceAuthorizationInstant
	}
	return micros, int16(nanos), nil
}

func joinNamespaceAuthorizationInstant(micros time.Time, nanos int16) (time.Time, error) {
	if micros.IsZero() || micros.Nanosecond()%1000 != 0 || nanos < 0 || nanos > 999 {
		return time.Time{}, errNamespaceAuthorizationInstant
	}
	return micros.UTC().Add(time.Duration(nanos)), nil
}
