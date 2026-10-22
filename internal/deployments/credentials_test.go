package deployments

import "testing"

// The database gateway reads addresses with ResourceURLKey, so it has to be
// the key the runner writes them under.
func TestResourceURLKeyIsTheRunnersKey(t *testing.T) {
	if got, want := ResourceURLKey("org_1", "env_1", "db"), resourceKey("org_1", "env_1", "db", ResourceURL); got != want {
		t.Fatalf("ResourceURLKey = %q, want %q", got, want)
	}
	if got := ResourceURLKey("org_1", "env_1", "db"); got != "org_1/env_1/db/url" {
		t.Fatalf("ResourceURLKey = %q, the stored keys use org/env/name/url", got)
	}
}
