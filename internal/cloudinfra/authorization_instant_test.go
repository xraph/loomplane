package cloudinfra

import (
	"testing"
	"time"
)

func TestNamespaceAuthorizationInstantRoundTrip(t *testing.T) {
	for _, at := range []time.Time{
		time.Date(2026, 11, 4, 12, 0, 0, 123456789, time.UTC),
		time.Date(2026, 11, 4, 6, 0, 0, 999999999, time.FixedZone("offset", -6*60*60)),
		time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(2001, 1, 1, 0, 0, 0, 1000, time.UTC),
		time.Now(),
	} {
		micros, nanos, err := splitNamespaceAuthorizationInstant(at)
		if err != nil {
			t.Fatal(err)
		}
		if micros.Location() != time.UTC || micros.Nanosecond()%1000 != 0 || micros.After(at) || nanos < 0 || nanos > 999 {
			t.Fatalf("noncanonical split: %s + %d", micros, nanos)
		}
		got, err := joinNamespaceAuthorizationInstant(micros, nanos)
		if err != nil || !got.Equal(at) {
			t.Fatalf("lost exact instant %s: %s, %v", at, got, err)
		}
		// Expiry at the exact authorization instant is already expired.
		if got.After(at) || !at.Add(time.Nanosecond).After(got) {
			t.Fatal("expiry boundary changed")
		}
	}
}

func TestNamespaceAuthorizationInstantRejectsMalformedEvidence(t *testing.T) {
	if _, _, err := splitNamespaceAuthorizationInstant(time.Time{}); err == nil {
		t.Fatal("accepted missing authorization time")
	}
	at := time.Date(2026, 11, 4, 12, 0, 0, 123456000, time.UTC)
	for _, tc := range []struct {
		at time.Time
		ns int16
	}{
		{time.Time{}, 0},
		{at, -1},
		{at, 1000},
		{at.Add(time.Nanosecond), 0},
	} {
		if _, err := joinNamespaceAuthorizationInstant(tc.at, tc.ns); err == nil {
			t.Fatalf("accepted malformed evidence %s + %d", tc.at, tc.ns)
		}
	}
}
