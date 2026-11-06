package security

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// The dashboard asks a project's permission list only about the permissions
// that list covers, and asks the workspace about everything else. Its copy
// of the catalog has to match the server's, or a project grant goes unseen.
func TestDashboardProjectPermissionsMatchCatalog(t *testing.T) {
	src, err := os.ReadFile("../../apps/dashboard/src/lib/project-permissions.ts")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)PROJECT_PERMISSIONS[^=]*=\s*\[(.*?)\]`).FindSubmatch(src)
	if block == nil {
		t.Fatal("PROJECT_PERMISSIONS not found in project-permissions.ts")
	}
	var got []string
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(block[1], -1) {
		got = append(got, string(m[1]))
	}
	var want []string
	for _, p := range ProjectPermissions() {
		want = append(want, p.Name())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("dashboard PROJECT_PERMISSIONS = %v\nwant ProjectPermissions() = %v", got, want)
	}
}
