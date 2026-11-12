package currentauthority

import (
	"context"
	"errors"
	"testing"

	authid "github.com/xraph/authsome/id"
	"github.com/xraph/grove/drivers/pgdriver"
	"github.com/xraph/warden"
	"github.com/xraph/warden/store"

	"github.com/xraph/loomplane/internal/security"
)

type ordinaryInstallationStore struct{ store.Store }
type ordinaryInstallationStoreWrapper struct{ store.Store }

func ordinaryInstallationCandidate(t *testing.T) (*Coordinator, *builtInInstallation, *security.Authorizer) {
	t.Helper()
	c := &Coordinator{}
	// Any store call would panic. The unopened database and empty backend also
	// make this fixture fail if capture starts querying or acquiring resources.
	router := &storeRouter{owner: c, ordinary: &ordinaryInstallationStore{}}
	engine, err := warden.NewEngine(warden.WithStore(router), warden.WithCache(NewFreshCache(c, warden.NewMemoryCache())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	installation := &builtInInstallation{main: pgdriver.New(), backend: &nativeBackend{}, engine: engine, appID: authid.NewAppID().String(), schema: "qualified", owner: 42}
	return c, installation, security.NewAuthorizer(engine, router, nil)
}

func TestCaptureOrdinaryProfileWithoutQueries(t *testing.T) {
	for _, requireEmail := range []bool{false, true} {
		c, candidate, authorizer := ordinaryInstallationCandidate(t)
		app, owner := candidate.appID, candidate.owner
		captured, err := captureOrdinaryProfile(c, candidate, authorizer, requireEmail)
		if err != nil {
			t.Fatal(err)
		}
		candidate.appID, candidate.owner = authid.NewAppID().String(), 99
		if captured.authorizer != authorizer || captured.appID != app || captured.migrationRole != owner || captured.requireEmail != requireEmail {
			t.Fatal("capture retained mutable candidate policy or lost canonical authorizer")
		}
		if c.installation != nil || c.backend != nil || c.profile != nil {
			t.Fatal("capture published coordinator state")
		}
	}
}

func TestCaptureOrdinaryProfileRefusesUnsupportedCandidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(**Coordinator, **builtInInstallation, **security.Authorizer)
	}{
		{"nil coordinator", func(c **Coordinator, _ **builtInInstallation, _ **security.Authorizer) { *c = nil }},
		{"nil candidate", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) { *p = nil }},
		{"nil main", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) { (*p).main = nil }},
		{"nil backend", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) { (*p).backend = nil }},
		{"nil engine", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) { (*p).engine = nil }},
		{"zero owner", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) { (*p).owner = 0 }},
		{"empty schema", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) { (*p).schema = "" }},
		{"blank schema", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) { (*p).schema = " \t" }},
		{"invalid app", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) { (*p).appID = "unsupported" }},
		{"wrong app kind", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) {
			(*p).appID = authid.NewUserID().String()
		}},
		{"nil authorizer", func(_ **Coordinator, _ **builtInInstallation, a **security.Authorizer) { *a = nil }},
		{"different owner", func(c **Coordinator, _ **builtInInstallation, _ **security.Authorizer) { *c = &Coordinator{} }},
		{"nil ordinary store", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) {
			(*p).engine.Store().(*storeRouter).ordinary = nil
		}},
		{"typed nil ordinary store", func(_ **Coordinator, p **builtInInstallation, _ **security.Authorizer) {
			(*p).engine.Store().(*storeRouter).ordinary = (*ordinaryInstallationStore)(nil)
		}},
		{"different authorizer engine", func(_ **Coordinator, p **builtInInstallation, a **security.Authorizer) {
			*a = security.NewAuthorizer(&warden.Engine{}, (*p).engine.Store(), nil)
		}},
		{"different authorizer store", func(_ **Coordinator, p **builtInInstallation, a **security.Authorizer) {
			*a = security.NewAuthorizer((*p).engine, &ordinaryInstallationStore{}, nil)
		}},
		{"authorizer store wrapper", func(_ **Coordinator, p **builtInInstallation, a **security.Authorizer) {
			*a = security.NewAuthorizer((*p).engine, &ordinaryInstallationStoreWrapper{(*p).engine.Store()}, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, candidate, authorizer := ordinaryInstallationCandidate(t)
			tc.change(&c, &candidate, &authorizer)
			captured, err := captureOrdinaryProfile(c, candidate, authorizer, true)
			if captured != nil || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unsupported capture=%v err=%v", captured, err)
			}
		})
	}
}

func TestOrdinaryAuthorityHandleRefusesBeforeInstallation(t *testing.T) {
	var absent *OrdinaryAuthority
	var absentCoordinator *Coordinator
	c := &Coordinator{}
	for _, handle := range []*OrdinaryAuthority{absent, {}, absentCoordinator.OrdinaryAuthority(), c.OrdinaryAuthority()} {
		err := handle.RunOrdinary(t.Context(), testBinding(), func(context.Context, *Attempt) error {
			t.Fatal("uninstalled ordinary callback ran")
			return nil
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	}
	for _, p := range []*profile{nil, {}, {mode: installedProfile}, {mode: installedProfile, fleet: &fleetProfile{}}} {
		c.profile = p
		if err := c.OrdinaryAuthority().RunOrdinary(t.Context(), testBinding(), func(context.Context, *Attempt) error { t.Fatal("unsupported profile callback ran"); return nil }); !errors.Is(err, ErrUnavailable) {
			t.Fatal(err)
		}
	}
	if err := c.OrdinaryAuthority().RunOrdinary(t.Context(), Binding(fleetTestBinding()), func(context.Context, *Attempt) error {
		t.Fatal("Fleet callback ran through ordinary handle")
		return nil
	}); !errors.Is(err, ErrBinding) {
		t.Fatal(err)
	}
}

func TestOrdinarySchemaForAppExactInstallation(t *testing.T) {
	assertUnavailable := func(t *testing.T, handle *OrdinaryAuthority, main *pgdriver.PgDB, app string) {
		t.Helper()
		if schema, err := handle.OrdinarySchemaForApp(main, app); schema != "" || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unavailable ordinary schema=%q err=%v", schema, err)
		}
	}
	var nilCoordinator *Coordinator
	for _, handle := range []*OrdinaryAuthority{nil, {}, nilCoordinator.OrdinaryAuthority(), (&Coordinator{}).OrdinaryAuthority()} {
		assertUnavailable(t, handle, pgdriver.New(), "app")
	}
	for _, name := range []string{
		"exact", "nil main", "wrong main", "empty app", "wrong app", "empty installed app",
		"no installation", "nil backend", "wrong backend", "empty schema", "wrong schema",
		"no profile", "mechanism profile", "Fleet only", "nil engine", "wrong engine",
		"wrong ordinary app", "no owner", "wrong migration role", "nil authorizer",
		"authorizer engine", "authorizer store", "mutated engine config", "appended plugin",
		"installing", "stopping", "closing", "closed",
	} {
		t.Run(name, func(t *testing.T) {
			c, installed, authorizer := ordinaryInstallationCandidate(t)
			ordinary, err := captureOrdinaryProfile(c, installed, authorizer, true)
			if err != nil {
				t.Fatal(err)
			}
			router, ok := installed.engine.Store().(*storeRouter)
			if !ok {
				t.Fatal("fixture lost the canonical router")
			}
			p, err := captureEngineProfile(installed.engine, router)
			if err != nil {
				t.Fatal(err)
			}
			p.ordinary = ordinary
			c.installation, c.backend, c.schema, c.profile = installed, installed.backend, installed.schema, p
			main, app := installed.main, installed.appID
			switch name {
			case "nil main":
				main = nil
			case "wrong main":
				main = pgdriver.New()
			case "empty app":
				app = ""
			case "wrong app":
				app = authid.NewAppID().String()
			case "empty installed app":
				installed.appID = ""
			case "no installation":
				c.installation = nil
			case "nil backend":
				installed.backend, c.backend = nil, nil
			case "wrong backend":
				c.backend = &nativeBackend{}
			case "empty schema":
				installed.schema, c.schema = "", ""
			case "wrong schema":
				c.schema = "other"
			case "no profile":
				c.profile = nil
			case "mechanism profile":
				p.mode = mechanismProfile
			case "Fleet only":
				p.ordinary, p.fleet = nil, &fleetProfile{appID: app}
			case "nil engine":
				installed.engine, p.engine = nil, nil
			case "wrong engine":
				p.engine = &warden.Engine{}
			case "wrong ordinary app":
				ordinary.appID = authid.NewAppID().String()
			case "no owner":
				installed.owner, ordinary.migrationRole = 0, 0
			case "wrong migration role":
				ordinary.migrationRole++
			case "nil authorizer":
				ordinary.authorizer = nil
			case "authorizer engine":
				ordinary.authorizer = security.NewAuthorizer(&warden.Engine{}, installed.engine.Store(), nil)
			case "authorizer store":
				ordinary.authorizer = security.NewAuthorizer(installed.engine, &ordinaryInstallationStoreWrapper{installed.engine.Store()}, nil)
			case "mutated engine config":
				config := installed.engine.Config()
				*config.EnableRBAC = false
			case "appended plugin":
				installed.engine.Plugins().Register(&namedPlugin{name: "extra"})
			case "installing":
				c.installing = true
			case "stopping":
				c.stopping = true
			case "closing":
				c.closing = true
			case "closed":
				c.closed = true
			}
			handle := c.OrdinaryAuthority()
			if name == "exact" {
				// The main database is unopened and the profile is ordinary-only.
				if schema, err := handle.OrdinarySchemaForApp(main, app); err != nil || schema != "qualified" {
					t.Fatalf("exact ordinary schema=%q err=%v", schema, err)
				}
				return
			}
			assertUnavailable(t, handle, main, app)
		})
	}
}
