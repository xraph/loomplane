// Package migrations holds Loomplane's schema as a grove migration group,
// and lists every library group Loomplane runs in its one migration pass.
//
// Each migration is a file of its own, named <version>_<name>.go, that adds
// itself to App from init. Tables, columns, indexes and constraints go
// through grove's schema builders. What they don't cover, such as trigger
// functions, DO blocks and backfills, runs through db.Exec one statement at
// a time. On Postgres every migration runs in a transaction with the row
// that records it, so one that fails leaves nothing behind.
package migrations

import (
	"slices"

	"github.com/xraph/authsome/plugins/oauth2provider"
	"github.com/xraph/authsome/plugins/waitlist"
	authpg "github.com/xraph/authsome/store/postgres"
	chronpg "github.com/xraph/chronicle/store/postgres"
	cortexpg "github.com/xraph/cortex/store/postgres"
	cppg "github.com/xraph/ctrlplane/store/postgres"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove/migrate"
	heraldpg "github.com/xraph/herald/store/postgres"
	kspg "github.com/xraph/keysmith/store/postgres"
	nexuspg "github.com/xraph/nexus/store/postgres"
	relaypg "github.com/xraph/relay/store/postgres"
	shieldpg "github.com/xraph/shield/store/postgres"
	trovepg "github.com/xraph/trove/extension/store/postgres"
	vaultpg "github.com/xraph/vault/store/postgres"
	wardenpg "github.com/xraph/warden/store/postgres"
	weavepg "github.com/xraph/weave/store/postgres"
	"github.com/xraph/weave/vectorstore/pgvector"
)

// GroupName is the migration group for Loomplane's own tables.
const GroupName = "loomplane"

// App is where every Loomplane migration registers. Run
//
//	forge db create-go <name>
//
// and you get database/migrations/<version>_<name>.go, which adds itself
// here from init. You don't edit this file to add a migration.
//
// A migration's version and name are what databases record it as. Renaming
// the file of one that has shipped makes grove see a new migration and run
// it again, so leave those alone.
var App = migrate.NewGroup(GroupName)

// Group builds Loomplane's migration group from App. It's a constructor,
// and it copies App's migrations, because tests lift migrations out of the
// result into groups of their own and grove's Register writes to every
// migration it's handed.
func Group() *migrate.Group {
	g := migrate.NewGroup(GroupName)
	for _, m := range App.Migrations() {
		copied := *m
		g.MustRegister(&copied)
	}
	return g
}

// nativeCredentials holds the migrations that tie Loomplane's tables to
// Vault's, so it runs once both have created theirs.
var nativeCredentials = migrate.NewGroup("loomplane_native_credentials", migrate.DependsOn(GroupName, "vault"))

// NativeCredentialsGroup builds the native credentials group, copying its
// migrations for the same reason Group does.
func NativeCredentialsGroup() *migrate.Group {
	g := migrate.NewGroup("loomplane_native_credentials", migrate.DependsOn(GroupName, "vault"))
	for _, m := range nativeCredentials.Migrations() {
		copied := *m
		g.MustRegister(&copied)
	}
	return g
}

// All is every group Loomplane migrates: its own, then each library that
// keeps tables in Loomplane's database. Each library's own extension has
// its self-migration turned off, so this one pass (Forge's central
// migrations, run by the grove extension) owns the schema.
//
// The agent libraries (cortex, nexus, shield and weave) come last. Their
// tables are made whether or not AI is on, so turning it on later needs no
// migration step. Weave's pgvector group (weave_pgvector) is never here: it
// needs the vector extension, so it runs on demand once a workspace turns
// knowledge on. OptionalVersions names its migrations for the schema guard.
func All() []*migrate.Group {
	// Groups only this edition runs come last.
	return slices.Concat([]*migrate.Group{
		Group(),
		authpg.Migrations,
		waitlist.PostgresMigrations,
		oauth2provider.PostgresMigrations,
		wardenpg.Migrations,
		chronpg.Migrations,
		dispatchpg.Migrations,
		cppg.Migrations,
		managedInstancesGroup(),
		vaultpg.Migrations,
		NativeCredentialsGroup(),
		kspg.Migrations,
		trovepg.Migrations,
		heraldpg.Migrations,
		relaypg.Migrations,
		cortexpg.Migrations,
		nexuspg.Migrations,
		shieldpg.Migrations,
		weavepg.Migrations,
	}, editionGroups())
}

// optionalDimensions is any size pgvector accepts. A migration's version
// doesn't depend on it, and OptionalVersions only reads versions.
const optionalDimensions = 1536

// OptionalVersions are the migrations a database may have that All()
// never runs, as group:version: weave's pgvector group, which runs on
// demand. The schema guard counts them as known rather than as a newer
// Loomplane's.
func OptionalVersions() []string {
	g, err := pgvector.Migrations(optionalDimensions)
	if err != nil {
		// A pinned library refusing a valid size is a programming error.
		panic("migrations: weave pgvector migrations: " + err.Error())
	}
	out := make([]string, 0, len(g.Migrations()))
	for _, m := range g.Migrations() {
		out = append(out, g.Name()+":"+m.Version)
	}
	return out
}
