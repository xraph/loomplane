//go:build integration

package cloudinfra

import (
	"slices"
	"testing"
	"time"
)

func TestZoneProofsDueQuery(t *testing.T) {
	pg := overviewDatabase(t)
	ctx := t.Context()
	if _, err := pg.Exec(ctx, `CREATE TABLE loomplane_dns_zones(id TEXT PRIMARY KEY, org_id TEXT NOT NULL, name TEXT NOT NULL, connection_id TEXT NOT NULL, revision BIGINT NOT NULL, body JSONB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	st := NewPgStore(pg)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	checkedBefore := now.Add(-zoneProofCheckEvery)
	zone := func(id, org, mode string, edit func(*Zone)) {
		t.Helper()
		z := &Zone{ID: id, OrgID: org, Name: id + ".example.com", Mode: mode, Revision: 1}
		z.ProofName, z.ProofValue, z.ProofExpiresAt = "_loomplane."+z.Name, "loomplane="+id, now.Add(time.Hour)
		if edit != nil {
			edit(z)
		}
		if err := st.CreateZone(ctx, z); err != nil {
			t.Fatal(err)
		}
	}
	zone("never", "org", "managed", nil)
	zone("stale", "org", "managed", func(z *Zone) { z.ProofCheckedAt = now.Add(-10 * time.Minute) })
	zone("edge", "org", "managed", func(z *Zone) { z.ProofCheckedAt = checkedBefore })
	zone("recent", "org", "managed", func(z *Zone) { z.ProofCheckedAt = now.Add(-time.Minute) })
	zone("verified", "org", "managed", func(z *Zone) { z.VerifiedAt = now.Add(-time.Hour) })
	zone("expired", "org", "managed", func(z *Zone) { z.ProofExpiresAt = now })
	zone("external", "org", "external", nil)
	zone("theirs", "other", "managed", func(z *Zone) { z.ProofCheckedAt = now.Add(-5 * time.Minute) })

	due, err := st.ZoneProofsDue(ctx, now, checkedBefore, 10)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(due))
	for _, z := range due {
		ids = append(ids, z.ID)
		if !zoneProofDue(z, now, checkedBefore) {
			t.Fatalf("query and service disagree on %s", z.ID)
		}
	}
	// Longest unchecked first, across organizations.
	if want := []string{"never", "stale", "theirs", "edge"}; !slices.Equal(ids, want) {
		t.Fatalf("due %v, want %v", ids, want)
	}
	if due, err = st.ZoneProofsDue(ctx, now, checkedBefore, 2); err != nil || len(due) != 2 || due[0].ID != "never" || due[1].ID != "stale" {
		t.Fatalf("limit: %v %v", due, err)
	}
}
