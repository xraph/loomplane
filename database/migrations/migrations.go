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
	"github.com/xraph/authsome/plugins/waitlist"
	authpg "github.com/xraph/authsome/store/postgres"
	chronpg "github.com/xraph/chronicle/store/postgres"
	cppg "github.com/xraph/ctrlplane/store/postgres"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	"github.com/xraph/grove/migrate"
	heraldpg "github.com/xraph/herald/store/postgres"
	kspg "github.com/xraph/keysmith/store/postgres"
	relaypg "github.com/xraph/relay/store/postgres"
	trovepg "github.com/xraph/trove/extension/store/postgres"
	vaultpg "github.com/xraph/vault/store/postgres"
	wardenpg "github.com/xraph/warden/store/postgres"
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
func All() []*migrate.Group {
	return []*migrate.Group{
		Group(),
		authpg.Migrations,
		waitlist.PostgresMigrations,
		wardenpg.Migrations,
		chronpg.Migrations,
		dispatchpg.Migrations,
		cppg.Migrations,
		vaultpg.Migrations,
		NativeCredentialsGroup(),
		kspg.Migrations,
		trovepg.Migrations,
		heraldpg.Migrations,
		relaypg.Migrations,
	}
}
