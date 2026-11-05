package platform

import (
	"slices"
	"testing"

	"github.com/hallproof/witness/adapter"
	witnessforge "github.com/hallproof/witness/forge"

	"github.com/xraph/loomplane/internal/config"
	"github.com/xraph/loomplane/internal/platformadmin"
)

func hallproofOn() config.Config {
	cfg := config.Defaults()
	cfg.Hallproof = config.HallproofConfig{Endpoint: "https://hallproof.example.com", IngestKey: "hpk_test"}
	return cfg
}

func TestWitnessOffWithoutEndpoint(t *testing.T) {
	if got := names(config.Defaults()); slices.Contains(got, witnessforge.Name) {
		t.Fatalf("no hallproof.endpoint and witness is registered: %v", got)
	}
}

func TestWitnessMountedWithEndpoint(t *testing.T) {
	got := names(hallproofOn())
	i := slices.Index(got, witnessforge.Name)
	if i < 0 {
		t.Fatalf("hallproof.endpoint is set and witness isn't registered: %v", got)
	}
	// The console depends on every extension listed before it.
	if c := slices.Index(got, console(nil).Name()); c < i {
		t.Fatalf("witness at %d comes after the console at %d: %v", i, c, got)
	}
}

func TestWitnessConfig(t *testing.T) {
	cfg := hallproofOn()
	w := witnessConfig(cfg)
	if w.Endpoint != cfg.Hallproof.Endpoint || w.IngestKey != "hpk_test" || w.IngestKeyFile != "" {
		t.Fatalf("endpoint and key %q %q %q", w.Endpoint, w.IngestKey, w.IngestKeyFile)
	}
	if w.Scope.AppID != cfg.Identity.AppID || w.Scope.TenantID != platformadmin.AuditTenant || w.Scope.AllTenants {
		t.Fatalf("scope %+v", w.Scope)
	}
	if !slices.Equal(w.Scope.StaffRoles, []string{"platform-owner", "platform-admin"}) {
		t.Fatalf("staff roles %v", w.Scope.StaffRoles)
	}
	apps, _ := w.Adapters["vault"]["apps"].([]string)
	want := []string{"loomplane-platform-oauth-apps", "loomplane-platform-cloud", "loomplane-oidc-issuer", "loomplane-chronicle-checkpoints"}
	if !slices.Equal(apps, want) {
		t.Fatalf("vault apps %v, want %v", apps, want)
	}
	if shield := w.Adapters["shield"]; shield["app_id"] != "loomplane" || shield["tenants"] != nil {
		t.Fatalf("shield %v, want app loomplane and no tenants", shield)
	}
	// Every adapter is on: each reads its engine through its library's
	// extension, so none starts an engine a second time.
	if len(w.Exclude) != 0 {
		t.Errorf("exclude %v, want every adapter on", w.Exclude)
	}
	// No staff lists until Loomplane names a staff organization.
	for _, name := range []string{"keysmith", "relay", "cortex", "dispatch"} {
		if _, ok := w.Adapters[name]; ok {
			t.Errorf("%s has config: %v", name, w.Adapters[name])
		}
	}
}

func TestWitnessAdaptersImported(t *testing.T) {
	all := adapter.Adapters()
	got := make([]string, 0, len(all))
	for _, a := range all {
		got = append(got, a.Name)
	}
	want := []string{"authsome", "chronicle", "cortex", "dispatch", "grove", "keysmith", "nexus", "relay", "shield", "trove", "vault", "warden"}
	if !slices.Equal(got, want) {
		t.Fatalf("registered adapters %v, want %v", got, want)
	}
}
