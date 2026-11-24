package planning

import (
	"slices"
	"strings"
	"testing"

	"github.com/xraph/loomplane/internal/catalogconnect"
	"github.com/xraph/loomplane/internal/recipes/recipestest"
)

// accessInput wires api to the registered Postgres with wiring.
func accessInput(t *testing.T, wiring string) Input {
	t.Helper()
	return accessInputOf(t, "postgres", wiring)
}

func accessInputOf(t *testing.T, kind, wiring string) Input {
	t.Helper()
	in := workspaceInput(t)
	in.ProjectSlug = "shop"
	in.Manifest = parse(t, "version: 1\nresources:\n  orders: {kind: "+kind+", existing_resource: "+workspaceService+"}\n"+
		"services:\n  api:\n    source: cmd/api\n    provider: server\n    target: "+targetA+"\n    endpoint: {host_port: 8080}\n    needs: [orders]\n"+
		"    wiring:\n      orders:\n"+wiring)
	return in
}

func ordersLinks(t *testing.T, got *Result) []EnvLink {
	t.Helper()
	for _, a := range got.Actions {
		if a.Name == "api" && a.Service != nil {
			return slices.DeleteFunc(slices.Clone(a.Service.Env), func(l EnvLink) bool { return l.From != "orders" })
		}
	}
	t.Fatalf("no api: %+v", got.Actions)
	return nil
}

func TestAServiceGetsADatabaseOfItsOwn(t *testing.T) {
	in := accessInput(t, "        access: own_database\n")
	in.ExistingResources[workspaceService].Endpoint.LastCheck = &catalogconnect.LastCheck{OK: true, Capabilities: &catalogconnect.Capabilities{CreateDatabase: true}}
	got := Compute(in)
	if !got.Executable {
		t.Fatalf("problems: %v", codes(got.Problems))
	}
	links := ordersLinks(t, got)
	if len(links) != 1 || links[0].Key != "DATABASE_URL" || links[0].Access != "own_database" || links[0].Database != "api" || links[0].User != "" {
		t.Fatalf("links: %+v", links)
	}
	if slices.Contains(codes(got.Warnings), "wiring-database-must-exist:api") {
		t.Fatal("warned although the credentials can create it")
	}

	// Credentials that can't create one leave it to exist already.
	in.ExistingResources[workspaceService].Endpoint.LastCheck.Capabilities.CreateDatabase = false
	if got := Compute(in); !got.Executable || !slices.Contains(codes(got.Warnings), "wiring-database-must-exist:api") {
		t.Fatalf("problems %v, warnings %v", codes(got.Problems), codes(got.Warnings))
	}
}

func TestAServiceGetsAUserOfItsOwn(t *testing.T) {
	in := accessInput(t, "        access: own_user\n        database: shop_api\n        env: {PGUSER: user, PGPASSWORD: password, PGDATABASE: database}\n")
	e := in.ExistingResources[workspaceService].Endpoint
	e.AdminSet = true
	e.LastCheck = &catalogconnect.LastCheck{OK: true, Admin: &catalogconnect.Capabilities{CreateDatabase: true, CreateUser: true}}
	got := Compute(in)
	if !got.Executable {
		t.Fatalf("problems: %v", codes(got.Problems))
	}
	links := ordersLinks(t, got)
	if len(links) != 3 {
		t.Fatalf("links: %+v", links)
	}
	for _, l := range links {
		if l.Access != "own_user" || l.Database != "shop_api" || l.User != "lp_shop_api" || l.Part == "" {
			t.Fatalf("link: %+v", l)
		}
	}

	// Without an admin credential that can create users, it's refused.
	e.LastCheck.Admin.CreateUser = false
	if got := Compute(in); got.Executable || !slices.Contains(codes(got.Problems), "wiring-access:api") {
		t.Fatalf("no create user: %v", codes(got.Problems))
	}
	e.LastCheck.Admin.CreateUser, e.AdminSet = true, false
	if got := Compute(in); got.Executable {
		t.Fatal("no admin credential was planned")
	}
}

func TestAccessModesAreRefusedWhereTheyCantWork(t *testing.T) {
	// A Redis database is a number.
	in := accessInputOf(t, "redis", "        access: own_database\n")
	in.ExistingResources[workspaceService].Endpoint.Engine = catalogconnect.EngineRedis
	if got := Compute(in); got.Executable || !slices.Contains(codes(got.Problems), "wiring-access:api") {
		t.Fatalf("redis without a number: %v", codes(got.Problems))
	}
	// MongoDB gets no user of its own yet.
	in = accessInputOf(t, "mongodb", "        access: own_user\n")
	e := in.ExistingResources[workspaceService].Endpoint
	e.Engine, e.AdminSet = catalogconnect.EngineMongoDB, true
	e.LastCheck = &catalogconnect.LastCheck{Admin: &catalogconnect.Capabilities{CreateDatabase: true, CreateUser: true}}
	if got := Compute(in); got.Executable || !slices.Contains(codes(got.Problems), "wiring-access:api") {
		t.Fatalf("mongodb own_user: %v", codes(got.Problems))
	}
	// A database Loomplane runs isn't a workspace service.
	got := composedPlan(t, recipestest.Registry(t), "version: 1\nresources:\n  db: {kind: postgres, provider: server, target: "+targetA+"}\n"+
		"services:\n  api:\n    source: cmd/api\n    provider: server\n    target: "+targetA+"\n    endpoint: {host_port: 8080}\n    needs: [db]\n    wiring:\n      db: {access: own_database}\n")
	if !slices.Contains(codes(got.Problems), "wiring-access:api") {
		t.Fatalf("not a workspace service: %v", codes(got.Problems))
	}
}

func TestOwnUserNamesFitEveryEngine(t *testing.T) {
	if got := OwnUserName("shop", "project_x", "api-v2"); got != "lp_shop_api_v2" {
		t.Fatal(got)
	}
	if got := OwnUserName("", "project_01jd8x5k2m", "api"); got != "lp_01jd8x5k2m_api" {
		t.Fatal(got)
	}
	long := OwnUserName("a-very-long-project-name", "", "a-very-long-service-name")
	if len(long) != ownUserMax || !strings.HasPrefix(long, "lp_a_very_long_project") || long == OwnUserName("a-very-long-project-name", "", "a-very-long-service-nam2") {
		t.Fatal(long)
	}
}
