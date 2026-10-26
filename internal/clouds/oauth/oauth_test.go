package oauth

import (
	"testing"
	"time"
)

func TestGrantDue(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	g := Grant{Expiry: now.Add(time.Hour)}
	if !g.Due(now, 24*time.Hour) {
		t.Error("a grant expiring in 1h should be due within 24h")
	}
	if g.Due(now, 30*time.Minute) {
		t.Error("a grant expiring in 1h should not be due within 30m")
	}
}
