//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/xraph/loomplane/internal/config"
)

// TestConsoleWardenGrantsSurviveRestart checks the platform roles' Warden
// read grants: exactly the reads, on each platform role, and the same rows
// after a restart has run authsome's bootstrap again.
func TestConsoleWardenGrantsSurviveRestart(t *testing.T) {
	cfg := config.Defaults()
	r, owner := freshSetup(t, &cfg)
	if code, body := owner.setup(ada); code != http.StatusOK {
		r.stop()
		t.Fatalf("setup: %d %s", code, body)
	}
	app := cfg.Identity.AppID
	grants := `SELECT count(*) FROM warden_role_permissions rp JOIN warden_roles r ON r.id = rp.role_id
		WHERE r.tenant_id = '` + app + `' AND r.slug IN ('platform-owner', 'platform-admin') AND rp.perm_name LIKE 'warden:%'`
	writes := `SELECT count(*) FROM warden_permissions WHERE tenant_id = '` + app + `' AND resource LIKE 'warden:%' AND action NOT IN ('read', 'read_audit', 'check')`
	read := envelope(t, "query", "warden", "roles.list", map[string]any{}, "")
	check := func(when string) {
		t.Helper()
		if n := countRows(t, cfg.Database.URL, grants); n != 20 {
			t.Errorf("%s: %d Warden grants on the platform roles, want 10 reads on each of 2", when, n)
		}
		if n := countRows(t, cfg.Database.URL, writes); n != 0 {
			t.Errorf("%s: %d Warden write permissions in Loomplane's app, want none", when, n)
		}
		if got := consoleCall(owner, http.MethodPost, consoleAPI, read); got.status != http.StatusOK {
			t.Errorf("%s: warden/roles.list: %+v", when, got)
		}
	}
	check("first start")
	r.stop()

	r = startStoredWaitlist(t, cfg)
	defer r.stop()
	owner.base = r.srv.URL
	check("after a restart")
}
