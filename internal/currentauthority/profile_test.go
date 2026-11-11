package currentauthority

import (
	"context"
	"errors"
	"testing"

	"github.com/xraph/warden"
)

type namedPlugin struct{ name string }

func (p *namedPlugin) Name() string { return p.name }

func fixedEngineFixture(t *testing.T, c *Coordinator) (*warden.Engine, *storeRouter) {
	t.Helper()
	router := &storeRouter{owner: c, ordinary: &recordingStore{}}
	engine, err := warden.NewEngine(warden.WithStore(router), warden.WithCache(NewFreshCache(c, warden.NewMemoryCache())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return engine, router
}

func TestFixedEngineProfileIsPrivateAndRejectsAppendedPlugins(t *testing.T) {
	for _, name := range []string{"custom", "warden-cache-invalidator"} {
		t.Run(name, func(t *testing.T) {
			c := &Coordinator{}
			engine, router := fixedEngineFixture(t, c)
			p, err := captureEngineProfile(engine, router)
			if err != nil {
				t.Fatal(err)
			}
			if err := p.verify(); err != nil {
				t.Fatal(err)
			}
			if c.profile != nil || c.backend != nil {
				t.Fatal("profile capture enabled authority")
			}
			engine.Plugins().Register(&namedPlugin{name: name})
			if !errors.Is(p.verify(), ErrUnavailable) {
				t.Fatal("appended plugin accepted")
			}
			if _, err := captureEngineProfile(engine, router); !errors.Is(err, ErrUnavailable) {
				t.Fatal("unknown startup accepted")
			}
		})
	}
}

func TestFixedEngineProfileChecksConfigValues(t *testing.T) {
	c := &Coordinator{}
	engine, router := fixedEngineFixture(t, c)
	p, err := captureEngineProfile(engine, router)
	if err != nil {
		t.Fatal(err)
	}
	// Config contains pointer fields. Verify values, not just pointer identity.
	config := engine.Config()
	*config.EnableRBAC = false
	if !errors.Is(p.verify(), ErrUnavailable) {
		t.Fatal("changed config accepted")
	}
	if _, err := captureEngineProfile(engine, router); !errors.Is(err, ErrUnavailable) {
		t.Fatal("nondefault startup accepted")
	}
}

func TestReaderRevalidatesCapturedProfile(t *testing.T) {
	reader := &recordingStore{}
	c, a := routerAttempt(t, reader)
	engine, router := fixedEngineFixture(t, c)
	p, err := captureEngineProfile(engine, router)
	if err != nil {
		t.Fatal(err)
	}
	c.profile = p
	a.profile = p
	engine.Plugins().Register(&namedPlugin{name: "warden-cache-invalidator"})
	if err := router.Ping(a.ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ping=%v", err)
	}
	if reader.calls != 0 || a.poison == nil {
		t.Fatal("profile mismatch reached reader")
	}
}

func validConnectionFixture() connectionProfile {
	p := connectionProfile{ReplicationRole: "origin", SystemID: "cluster", DatabaseID: 1, Database: "db", Role: "role", SessionRole: "role", RoleID: 10, SessionRoleID: 10, Schema: "public", SearchPath: "public", Primary: true}
	for i, name := range authorityTables {
		p.Tables = append(p.Tables, tableProfile{Name: name, OID: uint32(i + 1), Resolved: uint32(i + 1), Kind: "r", Readable: true, Temporal: map[string]string{"expires_at": "timestamp with time zone", "not_before": "timestamp with time zone", "not_after": "timestamp with time zone", "ban_expires": "timestamp with time zone", "banned": "boolean", "is_active": "boolean"}})
	}
	return p
}

func TestConnectionProfileRejectsUnknownVisibility(t *testing.T) {
	valid := validConnectionFixture()
	if err := valid.validate("public"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*connectionProfile){
		"replica mode":     func(p *connectionProfile) { p.ReplicationRole = "replica" },
		"local mode":       func(p *connectionProfile) { p.ReplicationRole = "local" },
		"missing mode":     func(p *connectionProfile) { p.ReplicationRole = "" },
		"missing role OID": func(p *connectionProfile) { p.RoleID = 0 },
		"session role OID": func(p *connectionProfile) { p.SessionRoleID++ },
		"standby":          func(p *connectionProfile) { p.Primary = false },
		"cluster":          func(p *connectionProfile) { p.SystemID = "" },
		"role":             func(p *connectionProfile) { p.Role = "other" },
		"schema":           func(p *connectionProfile) { p.Schema = "other" },
		"missing table":    func(p *connectionProfile) { p.Tables = p.Tables[:12] },
		"shadowed table":   func(p *connectionProfile) { p.Tables[0].Resolved++ },
		"RLS":              func(p *connectionProfile) { p.Tables[0].RLS = true },
		"forced RLS":       func(p *connectionProfile) { p.Tables[0].ForcedRLS = true },
		"privilege":        func(p *connectionProfile) { p.Tables[0].Readable = false },
		"view":             func(p *connectionProfile) { p.Tables[0].Kind = "v" },
		"trigger":          func(p *connectionProfile) { p.Tables[0].Triggers++ },
		"rule":             func(p *connectionProfile) { p.Tables[0].Rules++ },
		"inheritance":      func(p *connectionProfile) { p.Tables[0].Inheritance++ },
		"generated":        func(p *connectionProfile) { p.Tables[0].Generated++ },
		"temporal type":    func(p *connectionProfile) { p.Tables[2].Temporal["ban_expires"] = "timestamp without time zone" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validConnectionFixture()
			mutate(&p)
			if !errors.Is(p.validate("public"), ErrUnavailable) {
				t.Fatal("unsupported profile accepted")
			}
		})
	}
}

func TestConnectionProfileComparisonIncludesDatabaseRoleAndSearchPath(t *testing.T) {
	for name, mutate := range map[string]func(*connectionProfile){
		"cluster":      func(p *connectionProfile) { p.SystemID = "other" },
		"database":     func(p *connectionProfile) { p.Database = "other" },
		"database OID": func(p *connectionProfile) { p.DatabaseID++ },
		"role OID":     func(p *connectionProfile) { p.RoleID++; p.SessionRoleID++ },
		"role":         func(p *connectionProfile) { p.Role = "other"; p.SessionRole = "other" },
		"search path":  func(p *connectionProfile) { p.SearchPath = "public,other" },
		"table OID":    func(p *connectionProfile) { p.Tables[0].OID++; p.Tables[0].Resolved++ },
	} {
		t.Run(name, func(t *testing.T) {
			a, b := validConnectionFixture(), validConnectionFixture()
			mutate(&b)
			if sameDatabaseProfile(a, b) {
				t.Fatal("connection mismatch accepted")
			}
		})
	}
}
