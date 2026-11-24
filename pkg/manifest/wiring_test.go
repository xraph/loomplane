package manifest

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const wiredDoc = `version: 1
resources:
  orders:
    kind: postgres
services:
  api:
    source: cmd/api
    needs: [orders]
`

func TestWiringIsReadAndChecked(t *testing.T) {
	doc := wiredDoc + "    wiring:\n      orders:\n        env:\n          PGHOST: host\n          PGPASSWORD: password\n          DB: url\n"
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"PGHOST": "host", "PGPASSWORD": "password", "DB": "url"}
	if got := m.Services["api"].Wiring["orders"].Env; !reflect.DeepEqual(got, want) {
		t.Fatalf("wiring: %v", got)
	}
	for name, tc := range map[string]struct{ wiring, want string }{
		"a need it doesn't have": {"      cache:\n        env:\n          REDIS: url\n", "wiring.cache isn't in needs"},
		"no variables":           {"      orders:\n        env: {}\n", "names at least one variable"},
		"a bad name":             {"      orders:\n        env:\n          1DB: url\n", "isn't a variable name"},
		"an unknown output":      {"      orders:\n        env:\n          DB: schema\n", `"schema" isn't something a variable can hold`},
		"an unknown access":      {"      orders:\n        access: borrowed\n", `"borrowed" isn't an access mode`},
		"a database when shared": {"      orders:\n        database: api\n", "is only for access own_database or own_user"},
		"a bad database":         {"      orders:\n        access: own_user\n        database: api-db\n", "isn't a database name"},
	} {
		_, err := Parse([]byte(wiredDoc + "    wiring:\n" + tc.wiring))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestWithWiringSetsReplacesAndClears(t *testing.T) {
	doc, err := WithWiring([]byte(wiredDoc), "api", "orders", Wiring{Env: map[string]string{"PGHOST": "host", "PGPORT": "port"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "wiring:\n      orders:\n        env:\n          PGHOST: host\n          PGPORT: port\n") {
		t.Fatalf("set:\n%s", doc)
	}
	doc, err = WithWiring(doc, "api", "orders", Wiring{Env: map[string]string{"DATABASE_DSN": "url"}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Services["api"].Wiring["orders"].Env; !reflect.DeepEqual(got, map[string]string{"DATABASE_DSN": "url"}) {
		t.Fatalf("replaced: %v", got)
	}
	if doc, err = WithWiring(doc, "api", "orders", Wiring{}); err != nil || strings.Contains(string(doc), "wiring") {
		t.Fatalf("cleared: %v\n%s", err, doc)
	}
	if _, err = WithWiring([]byte(wiredDoc), "api", "cache", Wiring{Env: map[string]string{"X": "url"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wired to something it doesn't need: %v", err)
	}
	if _, err = WithWiring([]byte(wiredDoc), "api", "orders", Wiring{Env: map[string]string{"X": "schema"}}); err == nil {
		t.Fatal("an unknown output was written")
	}
}

func TestWithoutNeedDropsItsWiring(t *testing.T) {
	doc, err := WithWiring([]byte(wiredDoc), "api", "orders", Wiring{Env: map[string]string{"PGHOST": "host"}})
	if err != nil {
		t.Fatal(err)
	}
	if doc, err = WithoutNeed(doc, "api", "orders"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), "wiring") || strings.Contains(string(doc), "needs") {
		t.Fatalf("left behind:\n%s", doc)
	}
	if _, err = Parse(doc); err != nil {
		t.Fatal(err)
	}
}

func TestWiringCarriesAnAccessMode(t *testing.T) {
	doc, err := WithWiring([]byte(wiredDoc), "api", "orders", Wiring{Access: AccessOwnUser, Database: "api", Env: map[string]string{"DATABASE_URL": "url"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "orders:\n        access: own_user\n        database: api\n        env:\n") {
		t.Fatalf("set:\n%s", doc)
	}
	m, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	if w := m.Services["api"].Wiring["orders"]; w.Access != AccessOwnUser || w.Database != "api" {
		t.Fatalf("read: %+v", w)
	}
	// An access mode alone is a wiring; the shared one alone is none.
	if doc, err = WithWiring([]byte(wiredDoc), "api", "orders", Wiring{Access: AccessOwnDatabase}); err != nil || !strings.Contains(string(doc), "access: own_database") {
		t.Fatalf("access alone: %v\n%s", err, doc)
	}
	if doc, err = WithWiring(doc, "api", "orders", Wiring{Access: AccessShared}); err != nil || strings.Contains(string(doc), "wiring") {
		t.Fatalf("shared: %v\n%s", err, doc)
	}
	if _, err = WithWiring([]byte(wiredDoc), "api", "orders", Wiring{Database: "api"}); err == nil {
		t.Fatal("a database without its own access was written")
	}
}
