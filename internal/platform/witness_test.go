package platform

import (
	"slices"
	"testing"

	"github.com/hallproof/witness/adapter"
	witnessforge "github.com/hallproof/witness/forge"

	"github.com/xraph/loomplane/extensions/aiagent"
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
	// With no staff orgs there are no staff lists, so nothing an
	// organization owns is collected.
	for _, name := range []string{"keysmith", "relay", "cortex", "dispatch", "nexus"} {
		if _, ok := w.Adapters[name]; ok {
			t.Errorf("%s has config: %v", name, w.Adapters[name])
		}
	}
}

func TestWitnessConfigStaffOrgs(t *testing.T) {
	const a, b = "aorg_01jbxk1v7wf8sr6ka9w5gq0z2c", "aorg_01jbxk2m3ne4ytr8p0d6hv1c5x"
	for _, tt := range []struct {
		name  string
		staff []string
		orgs  []string // cortex's orgs after its org
	}{
		{"one", []string{a}, nil},
		{"two", []string{a, b}, []string{b}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := hallproofOn()
			cfg.Hallproof.StaffOrgs = tt.staff
			w := witnessConfig(cfg)
			list := func(adapter, key string) []string {
				t.Helper()
				v, _ := w.Adapters[adapter][key].([]string)
				return v
			}
			for _, at := range []struct{ adapter, key string }{
				{"keysmith", "staff_tenants"},
				{"relay", "staff_tenants"},
				{"dispatch", "staff_orgs"},
				{"shield", "tenants"},
			} {
				if got := list(at.adapter, at.key); !slices.Equal(got, tt.staff) {
					t.Errorf("%s %s = %v, want %v", at.adapter, at.key, got, tt.staff)
				}
			}
			if shield := w.Adapters["shield"]; shield["app_id"] != "loomplane" {
				t.Errorf("shield app_id %v, want loomplane", shield["app_id"])
			}
			cortex := w.Adapters["cortex"]
			if cortex["org"] != tt.staff[0] || cortex["scope_key"] != "org" || !slices.Equal(list("cortex", "orgs"), tt.orgs) {
				t.Errorf("cortex %v, want org %s, scope_key org and orgs %v", cortex, tt.staff[0], tt.orgs)
			}
			// Nexus names each staff org's agent tenant by its slug.
			want := make([]string, 0, len(tt.staff))
			for _, org := range tt.staff {
				want = append(want, aiagent.TenantSlug(org))
			}
			if got := list("nexus", "tenants"); !slices.Equal(got, want) {
				t.Errorf("nexus tenants %v, want %v", got, want)
			}
			// Nothing widens past the staff orgs.
			for name, section := range w.Adapters {
				if _, ok := section["all_tenants"]; ok {
					t.Errorf("%s sets all_tenants", name)
				}
			}
			// The adapters get their own copy, so one can't change another's.
			keys, hooks := list("keysmith", "staff_tenants"), list("relay", "staff_tenants")
			if len(keys) == 0 || len(hooks) == 0 {
				return
			}
			keys[0] = "changed"
			if cfg.Hallproof.StaffOrgs[0] != tt.staff[0] || hooks[0] != tt.staff[0] {
				t.Error("the adapters share the staff list")
			}
		})
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
