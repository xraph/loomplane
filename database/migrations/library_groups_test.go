package migrations

import (
	"slices"
	"testing"

	"github.com/xraph/weave/vectorstore/pgvector"
)

func TestAllRunsLibraryGroupsAfterLoomplanes(t *testing.T) {
	want := []string{
		"loomplane", "authsome", "authsome-waitlist", "authsome-oauth2", "authsome-mfa", "authsome-passkey", "warden", "chronicle", "dispatch",
		"ctrlplane", "loomplane_managed_instances", "vault", "loomplane_native_credentials", "keysmith", "trove", "herald", "relay",
		"cortex", "nexus", "shield", "weave",
	}
	got := make([]string, 0, len(want))
	seen := map[string]bool{}
	for _, g := range All() {
		if seen[g.Name()] {
			t.Errorf("group %s runs twice", g.Name())
		}
		seen[g.Name()] = true
		got = append(got, g.Name())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("All() runs\n  %v\nwant\n  %v", got, want)
	}
	for _, g := range All()[len(want)-4:] {
		if len(g.Migrations()) == 0 {
			t.Errorf("library group %s has no migrations", g.Name())
		}
	}
}

func TestAllNeverRunsPgvector(t *testing.T) {
	for _, g := range All() {
		if g.Name() == pgvector.GroupName {
			t.Fatalf("All() runs %s; it needs the vector extension and only runs on demand", pgvector.GroupName)
		}
	}
}

func TestOptionalVersionsArePgvectors(t *testing.T) {
	g, err := pgvector.Migrations(1536)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, len(g.Migrations()))
	for _, m := range g.Migrations() {
		want = append(want, pgvector.GroupName+":"+m.Version)
	}
	if len(want) == 0 {
		t.Fatal("pgvector.Migrations(1536) has no migrations")
	}
	if got := OptionalVersions(); !slices.Equal(got, want) {
		t.Fatalf("OptionalVersions() = %v, want %v", got, want)
	}
}
