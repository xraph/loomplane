// Package migrations holds Loomplane's schema as a grove migration group,
// and lists every library group Loomplane runs in its one migration pass.
//
// grove migrations aren't transactional, so each statement is written to be
// safe to re-run (IF NOT EXISTS) and runs in its own Exec call.
package migrations

import (
	"context"
	"fmt"

	authpg "github.com/xraph/authsome/store/postgres"
	chronpg "github.com/xraph/chronicle/store/postgres"
	cppg "github.com/xraph/ctrlplane/store/postgres"
	dispatchpg "github.com/xraph/dispatch/store/postgres"
	conduitpg "github.com/xraph/forge/extensions/conduit/providers/postgres"
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

// Group builds Loomplane's migration group. A constructor rather than a
// package variable filled by init(), so tests and the app each get a fresh
// group.
func Group() *migrate.Group {
	g := migrate.NewGroup(GroupName)
	g.MustRegister(
		statements("20261105000400", "zone_challenge_uses", zoneChallengeUsesUp, nil),
		statements("20261105000300", "kubernetes_routes", kubernetesRoutesUp, nil),
		statements("20261105000200", "notification_kinds", notificationKindsUp, nil),
		statements("20261105000100", "domain_registrations", domainRegistrationsUp, domainRegistrationsDown),
		statements("20261101000300", "cloud_compensation_progress", cloudCompensationProgressUp, nil),
		statements("20261101000200", "native_cleanup", nativeCleanupUp, nativeCleanupDown),
		statements("20261101000100", "native_restores", nativeRestoresUp, nativeRestoresDown),
		statements("20261031000200", "cloud_compensation", cloudCompensationUp, nil),
		statements("20261031000100", "native_point_creation_identity", nativePointIdentityUp, nativePointIdentityDown),
		statements("20261030000300", "connection_use_admission", connectionUseAdmissionUp, connectionUseAdmissionDown),
		statements("20261030000200", "catalog_proxy_sessions", catalogProxySessionsUp, catalogProxySessionsDown),
		statements("20261030000100", "catalog_endpoints", catalogEndpointsUp, catalogEndpointsDown),
		statements("20261029000500", "cloud_default_switch", cloudDefaultSwitchUp, cloudDefaultSwitchDown),
		statements("20261029000400", "cloud_tags", cloudTagsUp, cloudTagsDown),
		statements("20261028000400", "workload_attempt_safety", workloadAttemptSafetyUp, nil),
		statements("20261028000300", "workload_attempts", workloadAttemptsUp, nil),
		statements("20261028000200", "shared_sync_capacity_ledger", []string{serverCapacitySQL}, nil),
		statements("20261028000100", "shared_sync_admission", sharedSyncAdmissionUp, nil),
		statements("20261027000100", "routed_releases", routedReleasesUp, nil),
		statements("20261026000100", "cloud_domains", cloudDomainsUp, cloudDomainsDown),
		statements("20261025001000", "pool_effect_fences", poolEffectFencesUp, poolEffectFencesDown),
		statements("20261025000300", "pool_recovery", poolRecoveryUp, poolRecoveryDown),
		statements("20261025000100", "server_pools", serverPoolsUp, serverPoolsDown),
		statements("20261024000500", "native_inventory", nativeInventoryUp, nativeInventoryDown),
		statements("20261024000400", "native_sources", nativeSourcesUp, nativeSourcesDown),
		statements("20261024000300", "binding_physical_identity", bindingPhysicalIdentityUp, bindingPhysicalIdentityDown),
		statements("20261024000200", "cloud_infrastructure_review_guards", cloudInfrastructureReviewUp, cloudInfrastructureReviewDown),
		statements("20261024000100", "cloud_infrastructure", cloudInfrastructureUp, cloudInfrastructureDown),
		statements("20261011000001", "create_projects", projectsUp, projectsDown),
		statements("20261012000001", "create_analyses", analysesUp, analysesDown),
		statements("20261012000002", "create_github_installations", githubUp, githubDown),
		statements("20261012000003", "create_variables", variablesUp, variablesDown),
		statements("20261012000004", "add_project_style", projectStyleUp, projectStyleDown),
		statements("20261012000005", "create_planning", planningUp, planningDown),
		statements("20261012000006", "create_cloud_connections", cloudConnectionsUp, cloudConnectionsDown),
		statements("20261012000007", "create_environment_connections", environmentConnectionsUp, environmentConnectionsDown),
		statements("20261012000008", "create_deployments", deploymentsUp, deploymentsDown),
		statements("20261012000009", "add_binding_url", bindingURLUp, bindingURLDown),
		statements("20261013000001", "add_deployment_retries", deploymentRetriesUp, deploymentRetriesDown),
		statements("20261013000002", "add_binding_drift", bindingDriftUp, bindingDriftDown),
		statements("20261014000001", "create_project_sources", sourcesUp, sourcesDown),
		statements("20261014000002", "create_plan_sources", planSourcesUp, planSourcesDown),
		statements("20261014000003", "add_deployment_sources", deploymentSourcesUp, deploymentSourcesDown),
		statements("20261014000004", "backfill_environment_sources", environmentSourcesBackfillUp, environmentSourcesBackfillDown),
		statements("20261014000005", "index_source_repositories_by_name", sourceRepositoryIndexUp, sourceRepositoryIndexDown),
		statements("20261015000001", "drop_superseded_columns", supersededUp, supersededDown),
		statements("20261016000001", "add_binding_replaced", bindingReplacedUp, bindingReplacedDown),
		statements("20261016000002", "protect_connection_uses", connectionUsesUp, connectionUsesDown),
		statements("20261016000003", "persist_realtime_events", realtimeUp, realtimeDown),
		statements("20261016000004", "conduit_realtime_transport", conduitpg.Migrations("loomplane_realtime_broker"), []string{`DROP SCHEMA IF EXISTS loomplane_realtime_broker CASCADE`}),
		statements("20261016000005", "finalize_realtime_contracts", realtimeFinalizeUp(), nil),
		statements("20261016000006", "reserve_owned_target_ports", ownedReservationsUp, []string{`DROP TABLE IF EXISTS loomplane_host_ports`}),
		statements("20261018000100", "create_federation_destinations", federationUp, federationDown),
		statements("20261018000300", "federation_destination_references", federationLifecycleUp, federationLifecycleDown),
		statements("20261018000301", "federation_runtime_catalog", federationCatalogUp, federationCatalogDown),
		statements("20261018000302", "federation_enrollment", federationEnrollmentUp, federationEnrollmentDown),
		statements("20261020000300", "federation_access", federationAccessUp, federationAccessDown),
		statements("20261020000500", "federation_delivery", federationDeliveryUp, federationDeliveryDown),
		statements("20261021000100", "federation_installations", federationInstallationsUp, federationInstallationsDown),
		statements("20261021000300", "federation_launch_attempts", federationLaunchAttemptsUp, federationLaunchAttemptsDown),
		statements("20261021000400", "federation_launch_retirement", federationLaunchRetirementUp, federationLaunchRetirementDown),
		statements("20261022000100", "federation_preparations", federationPreparationsUp, federationPreparationsDown),
		statements("20261022000200", "federation_preparation_retirement", federationPreparationRetirementUp, federationPreparationRetirementDown),
		statements("20261023000100", "federation_prepared_enrollment", federationPreparedEnrollmentUp, federationPreparedEnrollmentDown),
		statements("20261018000200", "standalone_catalog_plans", catalogPlansUp, catalogPlansDown),
		statements("20261018000201", "service_recovery", serviceRecoveryUp, serviceRecoveryDown),
		statements("20261018000202", "service_restores", serviceRestoresUp, serviceRestoresDown),
		statements("20261018000203", "backup_policies", backupPoliciesUp, backupPoliciesDown),
		statements("20261020000600", "backup_retirement", backupRetirementUp, backupRetirementDown),
		statements("20261020000700", "backup_retirement_history", backupRetirementHistoryUp, backupRetirementHistoryDown),
		statements("20261020000800", "backup_retirement_listing", backupRetirementListingUp, backupRetirementListingDown),
		statements("20261021000200", "retention_policies", retentionPoliciesUp, retentionPoliciesDown),
		statements("20261018000400", "create_cloud_profiles", cloudProfilesUp, cloudProfilesDown),
		statements("20261018000401", "cloud_sync", cloudSyncUp, cloudSyncDown),
		statements("20261020000400", "cloud_sync_recovery_guards", cloudSyncRecoveryGuardsUp, cloudSyncRecoveryGuardsDown),
		statements("20261020000900", "shared_clouds", sharedCloudsUp, sharedCloudsDown),
		statements("20261020000401", "cloud_events", cloudEventsUp, cloudEventsDown),
		statements("20261019000100", "plan_source_refs", planSourceRefsUp, planSourceRefsDown),
		WorkloadControllersMigration(),
		WorkloadRegistrationsMigration(),
		NotificationsMigration(),
	)
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

func statements(version, name string, up, down []string) *migrate.Migration {
	run := func(stmts []string) migrate.MigrateFunc {
		return func(ctx context.Context, exec migrate.Executor) error {
			for i, s := range stmts {
				if _, err := exec.Exec(ctx, s); err != nil {
					return fmt.Errorf("%s statement %d: %w", name, i+1, err)
				}
			}
			return nil
		}
	}
	return &migrate.Migration{Name: name, Version: version, Up: run(up), Down: run(down)}
}

// Projects belong to an organization, an authsome organization ID. There's
// no foreign key to authsome's tables: they're authsome's, and Loomplane
// never writes them.
var projectsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_projects (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		name           TEXT        NOT NULL,
		slug           TEXT        NOT NULL,
		description    TEXT        NOT NULL DEFAULT '',
		repository_url TEXT        NOT NULL DEFAULT '',
		status         TEXT        NOT NULL,
		created_by     TEXT        NOT NULL,
		created_at     TIMESTAMPTZ NOT NULL,
		updated_at     TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_projects_org_slug UNIQUE (org_id, slug)
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_projects_org_created
		ON loomplane_projects (org_id, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS loomplane_environments (
		id         TEXT        PRIMARY KEY,
		org_id     TEXT        NOT NULL,
		project_id TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		name       TEXT        NOT NULL,
		kind       TEXT        NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_environments_project_name UNIQUE (project_id, name)
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_environments_org_project
		ON loomplane_environments (org_id, project_id)`,
}

var projectsDown = []string{
	`DROP TABLE IF EXISTS loomplane_environments`,
	`DROP TABLE IF EXISTS loomplane_projects`,
}

// An analysis is one run of repository discovery; a succeeded one holds a
// version of the project's application graph, whole, as JSON. attempts
// counts deliveries of its job, so the worker knows when a transient
// failure has become the result.
var analysesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_analyses (
		id          TEXT        PRIMARY KEY,
		org_id      TEXT        NOT NULL,
		project_id  TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		status      TEXT        NOT NULL,
		repository  TEXT        NOT NULL,
		ref         TEXT        NOT NULL DEFAULT '',
		commit_sha  TEXT        NOT NULL DEFAULT '',
		error       TEXT        NOT NULL DEFAULT '',
		summary     JSONB       NOT NULL DEFAULT '{}',
		graph       JSONB,
		attempts    INTEGER     NOT NULL DEFAULT 0,
		created_by  TEXT        NOT NULL,
		created_at  TIMESTAMPTZ NOT NULL,
		started_at  TIMESTAMPTZ,
		finished_at TIMESTAMPTZ
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_analyses_project_created
		ON loomplane_analyses (org_id, project_id, created_at DESC)`,
}

var analysesDown = []string{
	`DROP TABLE IF EXISTS loomplane_analyses`,
}

// A GitHub App installation an organization has connected. The same
// installation may be connected to several organizations, each by someone
// GitHub confirmed can see it. Nothing secret is stored: tokens are minted
// per analysis.
var githubUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_github_installations (
		org_id          TEXT        NOT NULL,
		installation_id BIGINT      NOT NULL,
		account         TEXT        NOT NULL,
		account_type    TEXT        NOT NULL DEFAULT '',
		created_by      TEXT        NOT NULL,
		created_at      TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (org_id, installation_id)
	)`,
}

var githubDown = []string{
	`DROP TABLE IF EXISTS loomplane_github_installations`,
}

// A variable set on one layer: organization-wide (no project), a project,
// or one of its services; for every environment or for one. A project's
// variable for one environment references it, so deleting either deletes
// the variable. An organization-wide one names its environment instead,
// since it applies to that environment in every project. A secret's value
// isn't here at all: it's in Vault, encrypted.
var variablesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_variables (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id TEXT        REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		environment    TEXT        NOT NULL DEFAULT '',
		service        TEXT        NOT NULL DEFAULT '',
		key            TEXT        NOT NULL,
		value          TEXT        NOT NULL DEFAULT '',
		secret         BOOLEAN     NOT NULL DEFAULT FALSE,
		created_by     TEXT        NOT NULL,
		updated_by     TEXT        NOT NULL,
		created_at     TIMESTAMPTZ NOT NULL,
		updated_at     TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_variables_service_in_project CHECK (project_id IS NOT NULL OR service = ''),
		CONSTRAINT loomplane_variables_environment_in_project CHECK (project_id IS NOT NULL OR environment_id IS NULL),
		CONSTRAINT loomplane_variables_secret_not_stored CHECK (NOT secret OR value = '')
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_variables_layer_key
		ON loomplane_variables (org_id, COALESCE(project_id, ''), service, environment, key)`,
	`CREATE INDEX IF NOT EXISTS loomplane_variables_project
		ON loomplane_variables (org_id, project_id)`,
}

var variablesDown = []string{
	`DROP TABLE IF EXISTS loomplane_variables`,
}

// A project's icon and color draw its tile in the dashboard. The service
// picks both for new projects (internal/projects/style.go); the backfill
// gives every older project a pair. It hashes the ID rather than rolling
// dice, so a project comes out the same on every database it runs on, and
// it only fills empty values, so a re-run leaves earlier picks and later
// edits alone. Color hashes a prefixed ID: with the bare one, 48 and 10
// share a factor and an even icon would always get an even color. The
// bigint cast is for abs, which overflows on the one int4 with no positive
// twin.
//
// The arrays copy the lists in style.go, in the same order, as they stood
// when this was written.
var projectStyleUp = []string{
	`ALTER TABLE loomplane_projects ADD COLUMN IF NOT EXISTS icon TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_projects ADD COLUMN IF NOT EXISTS color TEXT NOT NULL DEFAULT ''`,
	`UPDATE loomplane_projects p
		SET icon = n.names[1 + mod(abs(hashtext(p.id)::bigint), cardinality(n.names))]
		FROM (SELECT ARRAY[
			'store', 'bag', 'card', 'receipt', 'wallet', 'truck', 'bell', 'mail',
			'chat', 'megaphone', 'users', 'calendar', 'database', 'chart', 'search', 'sparkles',
			'bot', 'radar', 'server', 'cpu', 'webhook', 'braces', 'terminal', 'workflow',
			'package', 'layers', 'smartphone', 'globe', 'pin', 'health', 'learning', 'book',
			'film', 'music', 'image', 'gamepad', 'shield', 'key', 'fingerprint', 'rocket',
			'zap', 'compass', 'anchor', 'leaf', 'flame', 'mountain', 'atom', 'hexagon'
		] AS names) n
		WHERE p.icon = ''`,
	`UPDATE loomplane_projects p
		SET color = n.names[1 + mod(abs(hashtext('color:' || p.id)::bigint), cardinality(n.names))]
		FROM (SELECT ARRAY[
			'gold', 'orange', 'red', 'rose', 'violet', 'indigo', 'blue', 'teal', 'green', 'slate'
		] AS names) n
		WHERE p.color = ''`,
}

var projectStyleDown = []string{
	`ALTER TABLE loomplane_projects DROP COLUMN IF EXISTS color`,
	`ALTER TABLE loomplane_projects DROP COLUMN IF EXISTS icon`,
}

// Planning keeps three things. A manifest revision is one saved version of
// a project's loomplane.yaml; saving adds one and never edits one, so a
// plan can name exactly what it was made from. A plan is pinned to the
// analysis and manifest revision it was made from, with its result whole,
// as JSON. The partial unique index allows one open plan per environment,
// so of two started at once, one fails instead of both waiting for a
// decision. A binding records what runs one service or resource in one
// environment; deployments write them and the next plan reads them.
var planningUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_manifests (
		id         TEXT        PRIMARY KEY,
		org_id     TEXT        NOT NULL,
		project_id TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		revision   INTEGER     NOT NULL,
		body       TEXT        NOT NULL,
		digest     TEXT        NOT NULL,
		created_by TEXT        NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_manifests_project_revision UNIQUE (project_id, revision)
	)`,
	`CREATE TABLE IF NOT EXISTS loomplane_plans (
		id                TEXT        PRIMARY KEY,
		org_id            TEXT        NOT NULL,
		project_id        TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id    TEXT        NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		environment       TEXT        NOT NULL,
		status            TEXT        NOT NULL,
		revision          TEXT        NOT NULL DEFAULT '',
		analysis_id       TEXT        NOT NULL REFERENCES loomplane_analyses (id) ON DELETE CASCADE,
		commit_sha        TEXT        NOT NULL DEFAULT '',
		manifest_id       TEXT        NOT NULL REFERENCES loomplane_manifests (id) ON DELETE CASCADE,
		manifest_revision INTEGER     NOT NULL,
		result            JSONB,
		executable        BOOLEAN     NOT NULL DEFAULT FALSE,
		error             TEXT        NOT NULL DEFAULT '',
		created_by        TEXT        NOT NULL,
		created_at        TIMESTAMPTZ NOT NULL,
		planned_at        TIMESTAMPTZ,
		expires_at        TIMESTAMPTZ,
		decided_by        TEXT        NOT NULL DEFAULT '',
		decided_at        TIMESTAMPTZ,
		decision_note     TEXT        NOT NULL DEFAULT ''
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS loomplane_plans_one_open
		ON loomplane_plans (environment_id) WHERE status IN ('planning', 'ready')`,
	`CREATE INDEX IF NOT EXISTS loomplane_plans_project_created
		ON loomplane_plans (org_id, project_id, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS loomplane_bindings (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id TEXT        NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		subject        TEXT        NOT NULL,
		name           TEXT        NOT NULL,
		provider       TEXT        NOT NULL,
		region         TEXT        NOT NULL DEFAULT '',
		engine         TEXT        NOT NULL,
		external_id    TEXT        NOT NULL DEFAULT '',
		config         JSONB       NOT NULL DEFAULT '{}',
		plan_id        TEXT        NOT NULL DEFAULT '',
		created_at     TIMESTAMPTZ NOT NULL,
		updated_at     TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_bindings_subject UNIQUE (environment_id, subject, name)
	)`,
}

var planningDown = []string{
	`DROP TABLE IF EXISTS loomplane_bindings`,
	`DROP TABLE IF EXISTS loomplane_plans`,
	`DROP TABLE IF EXISTS loomplane_manifests`,
}

// A cloud account an organization deploys to. The credential isn't here:
// it's in Vault, encrypted, under the organization's and the connection's
// IDs. details holds what's safe to show about it, such as a service
// account's email.
var cloudConnectionsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_connections (
		id         TEXT        PRIMARY KEY,
		org_id     TEXT        NOT NULL,
		cloud      TEXT        NOT NULL,
		name       TEXT        NOT NULL,
		account    TEXT        NOT NULL DEFAULT '',
		details    JSONB       NOT NULL DEFAULT '{}',
		status     TEXT        NOT NULL,
		missing    JSONB       NOT NULL DEFAULT '[]',
		message    TEXT        NOT NULL DEFAULT '',
		checked_at TIMESTAMPTZ,
		created_by TEXT        NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CONSTRAINT loomplane_cloud_connections_org_name UNIQUE (org_id, name)
	)`,
}

var cloudConnectionsDown = []string{
	`DROP TABLE IF EXISTS loomplane_cloud_connections`,
}

// Which connection an environment deploys to each cloud with, when the
// organization has more than one. No foreign key to the connection: a
// removed one stays named here, so the next plan says so instead of
// quietly picking another account.
var environmentConnectionsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_environment_connections (
		org_id         TEXT NOT NULL,
		project_id     TEXT NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id TEXT NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		cloud          TEXT NOT NULL,
		connection_id  TEXT NOT NULL,
		PRIMARY KEY (environment_id, cloud)
	)`,
}

var environmentConnectionsDown = []string{
	`DROP TABLE IF EXISTS loomplane_environment_connections`,
}

// A deployment is one run of an approved plan, at most one per plan, with
// a step per action of the plan. source is the key of the commit's
// tarball in artifact storage.
var deploymentsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_deployments (
		id             TEXT        PRIMARY KEY,
		org_id         TEXT        NOT NULL,
		project_id     TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		environment_id TEXT        NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		environment    TEXT        NOT NULL,
		plan_id        TEXT        NOT NULL REFERENCES loomplane_plans (id) ON DELETE CASCADE,
		commit_sha     TEXT        NOT NULL,
		status         TEXT        NOT NULL,
		source         TEXT        NOT NULL DEFAULT '',
		error          TEXT        NOT NULL DEFAULT '',
		steps          JSONB       NOT NULL DEFAULT '[]',
		started_by     TEXT        NOT NULL,
		started_at     TIMESTAMPTZ NOT NULL,
		finished_at    TIMESTAMPTZ,
		CONSTRAINT loomplane_deployments_plan UNIQUE (plan_id)
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_deployments_project_started
		ON loomplane_deployments (org_id, project_id, started_at DESC)`,
}

var deploymentsDown = []string{
	`DROP TABLE IF EXISTS loomplane_deployments`,
}

// Where a deployed service answers, so the services that need it can be
// given its address.
var bindingURLUp = []string{
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS url TEXT NOT NULL DEFAULT ''`,
}

var bindingURLDown = []string{
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS url`,
}

// How often a failed deployment was retried.
var deploymentRetriesUp = []string{
	`ALTER TABLE loomplane_deployments ADD COLUMN IF NOT EXISTS retries INTEGER NOT NULL DEFAULT 0`,
}

var deploymentRetriesDown = []string{
	`ALTER TABLE loomplane_deployments DROP COLUMN IF EXISTS retries`,
}

// What a binding's drift check needs and finds: the connection that
// deployed it, the revision and commit that went live, and what the last
// check saw.
var bindingDriftUp = []string{
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS connection_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS revision TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS commit_sha TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS drift TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS drift_message TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS observed_at TIMESTAMPTZ`,
}

var bindingDriftDown = []string{
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS observed_at`,
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS drift_message`,
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS drift`,
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS commit_sha`,
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS revision`,
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS connection_id`,
}

// A source is a repository, or a directory of one, that a project builds
// from; a project has any number. A removed source keeps its row, so its
// alias stays taken: plans and variables name sources by alias, and an
// alias must always mean the same repository. An analysis reads one
// source; its alias is copied onto it, which is safe because aliases never
// change.
//
// The backfill turns each project's repository_url into its first source,
// with the alias internal/discovery.AliasFor would pick, and qualifies the
// service IDs its variables name with that alias. A URL that isn't on
// GitHub gets no source: it could never be analyzed. The pattern is anchored
// to the hosts repositories.ParseGitHubURL accepts, so a look-alike such as
// mygithub.com or a gist doesn't slip through. repository_url stays,
// unread, so a rollback still finds it; a later migration drops it. Each
// UPDATE only touches a project with exactly one source, which keeps a
// re-run from guessing between two.
var sourcesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_project_sources (
		id         TEXT        PRIMARY KEY,
		org_id     TEXT        NOT NULL,
		project_id TEXT        NOT NULL REFERENCES loomplane_projects (id) ON DELETE CASCADE,
		alias      TEXT        NOT NULL,
		repository TEXT        NOT NULL,
		root       TEXT        NOT NULL DEFAULT '',
		ref        TEXT        NOT NULL DEFAULT '',
		created_by TEXT        NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		removed_at TIMESTAMPTZ,
		CONSTRAINT loomplane_project_sources_alias UNIQUE (project_id, alias)
	)`,
	`CREATE INDEX IF NOT EXISTS loomplane_project_sources_repository
		ON loomplane_project_sources (repository)`,
	`ALTER TABLE loomplane_analyses ADD COLUMN IF NOT EXISTS source_id TEXT
		REFERENCES loomplane_project_sources (id) ON DELETE CASCADE`,
	`ALTER TABLE loomplane_analyses ADD COLUMN IF NOT EXISTS alias TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS loomplane_analyses_source_created
		ON loomplane_analyses (org_id, source_id, created_at DESC)`,
	`WITH m AS (
		SELECT id, org_id, created_by, created_at,
			regexp_match(trim(repository_url),
				'^(?:(?:https?://)?(?:www\.)?github\.com(?::[0-9]+)?/|git@github\.com:)([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+)',
				'i') AS parts
		FROM loomplane_projects WHERE repository_url <> ''
	), r AS (
		SELECT id, org_id, created_by, created_at,
			parts[1] || '/' || regexp_replace(parts[2], '\.git$', '') AS repository,
			trim(both '-' FROM regexp_replace(lower(regexp_replace(parts[2], '\.git$', '')), '[^a-z0-9]+', '-', 'g')) AS base
		FROM m WHERE parts IS NOT NULL
	), a AS (
		SELECT *, rtrim(left(CASE WHEN base = '' THEN 'repo' WHEN base ~ '^[0-9]' THEN 'r-' || base ELSE base END, 32), '-') AS alias
		FROM r
	)
	INSERT INTO loomplane_project_sources (id, org_id, project_id, alias, repository, root, ref, created_by, created_at, updated_at)
	SELECT 'src_' || substr(md5('source:' || id), 1, 26), org_id, id,
		CASE WHEN alias IN ('go', 'rust', 'node', 'container') THEN alias || '-src' ELSE alias END,
		repository, '', '', created_by, created_at, created_at
	FROM a
	ON CONFLICT DO NOTHING`,
	`UPDATE loomplane_analyses a SET source_id = s.id, alias = s.alias
		FROM loomplane_project_sources s
		WHERE a.source_id IS NULL AND s.project_id = a.project_id
		AND (SELECT count(*) FROM loomplane_project_sources x WHERE x.project_id = a.project_id) = 1`,
	`UPDATE loomplane_variables v SET service = 'svc:' || s.alias || ':' || substr(v.service, 5)
		FROM loomplane_project_sources s
		WHERE v.project_id = s.project_id AND v.service LIKE 'svc:%'
		AND split_part(v.service, ':', 2) IN ('go', 'rust', 'node', 'container')
		AND (SELECT count(*) FROM loomplane_project_sources x WHERE x.project_id = v.project_id) = 1`,
}

var sourcesDown = []string{
	`UPDATE loomplane_variables v SET service = 'svc:' || substr(v.service, length('svc:' || s.alias || ':') + 1)
		FROM loomplane_project_sources s
		WHERE v.project_id = s.project_id AND v.service LIKE 'svc:' || s.alias || ':%'`,
	`ALTER TABLE loomplane_analyses DROP COLUMN IF EXISTS alias`,
	`ALTER TABLE loomplane_analyses DROP COLUMN IF EXISTS source_id`,
	`DROP TABLE IF EXISTS loomplane_project_sources`,
}

// A plan pins one analysis of each source it was made from, and whether
// it moves that source or holds it at what's deployed. Each row copies the
// source's alias, repository and directory, so a plan recomposes exactly
// even after someone changes a source. An environment's sources are what
// a deployment last put there; only a successful deployment writes them.
// The backfill gives every older plan the one source it was made from,
// moving. loomplane_plans.analysis_id and commit_sha stay, unread, until a
// later migration drops them.
var planSourcesUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_plan_sources (
		plan_id     TEXT    NOT NULL REFERENCES loomplane_plans (id) ON DELETE CASCADE,
		source_id   TEXT    NOT NULL REFERENCES loomplane_project_sources (id) ON DELETE CASCADE,
		alias       TEXT    NOT NULL,
		repository  TEXT    NOT NULL,
		root        TEXT    NOT NULL DEFAULT '',
		analysis_id TEXT    NOT NULL REFERENCES loomplane_analyses (id) ON DELETE CASCADE,
		commit_sha  TEXT    NOT NULL,
		in_scope    BOOLEAN NOT NULL,
		PRIMARY KEY (plan_id, source_id)
	)`,
	`CREATE TABLE IF NOT EXISTS loomplane_environment_sources (
		environment_id TEXT        NOT NULL REFERENCES loomplane_environments (id) ON DELETE CASCADE,
		source_id      TEXT        NOT NULL REFERENCES loomplane_project_sources (id) ON DELETE CASCADE,
		org_id         TEXT        NOT NULL,
		analysis_id    TEXT        NOT NULL REFERENCES loomplane_analyses (id) ON DELETE CASCADE,
		commit_sha     TEXT        NOT NULL,
		plan_id        TEXT        NOT NULL REFERENCES loomplane_plans (id) ON DELETE CASCADE,
		deployed_at    TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (environment_id, source_id)
	)`,
	`ALTER TABLE loomplane_plans ALTER COLUMN analysis_id DROP NOT NULL`,
	`INSERT INTO loomplane_plan_sources (plan_id, source_id, alias, repository, root, analysis_id, commit_sha, in_scope)
		SELECT p.id, a.source_id, a.alias, a.repository, '', p.analysis_id, p.commit_sha, TRUE
		FROM loomplane_plans p JOIN loomplane_analyses a ON a.id = p.analysis_id
		WHERE a.source_id IS NOT NULL
		ON CONFLICT DO NOTHING`,
}

var planSourcesDown = []string{
	`DROP TABLE IF EXISTS loomplane_environment_sources`,
	`DROP TABLE IF EXISTS loomplane_plan_sources`,
}

// A deployment fetches one tarball per repository it moves. sources holds
// each one's alias, repository, commit and artifact key. source, the single
// key deployments used before, stays unread; the cleanup migration drops it
// with the other old columns.
var deploymentSourcesUp = []string{
	`ALTER TABLE loomplane_deployments ADD COLUMN IF NOT EXISTS sources JSONB NOT NULL DEFAULT '[]'`,
	`UPDATE loomplane_deployments SET sources = jsonb_build_array(jsonb_build_object(
		'alias', '', 'repository', '', 'commit_sha', commit_sha, 'key', source))
		WHERE sources = '[]'::jsonb AND source <> ''`,
}

var deploymentSourcesDown = []string{
	`ALTER TABLE loomplane_deployments DROP COLUMN IF EXISTS sources`,
}

// An environment deployed before sources existed has no rows saying what's
// deployed there, so its first scoped plan would call every held source
// never deployed. Each (environment, source) gets the commit the newest
// successful deployment moved it to, which is exactly what a deployment
// records now. A failed deployment doesn't count, the same as it doesn't
// write a row today. A row that's already there was written by a real
// deployment and stays.
var environmentSourcesBackfillUp = []string{
	`INSERT INTO loomplane_environment_sources (environment_id, source_id, org_id, analysis_id, commit_sha, plan_id, deployed_at)
		SELECT DISTINCT ON (d.environment_id, ps.source_id)
			d.environment_id, ps.source_id, d.org_id, ps.analysis_id, ps.commit_sha, d.plan_id, d.finished_at
		FROM loomplane_deployments d
		JOIN loomplane_plan_sources ps ON ps.plan_id = d.plan_id AND ps.in_scope
		WHERE d.status = 'succeeded' AND d.finished_at IS NOT NULL
		ORDER BY d.environment_id, ps.source_id, d.finished_at DESC
		ON CONFLICT (environment_id, source_id) DO NOTHING`,
}

// Nothing to undo: the backfilled rows can't be told apart from ones a
// deployment wrote, and create_plan_sources' down drops the table anyway.
var environmentSourcesBackfillDown = []string{}

// A push webhook finds the sources of a repository across organizations.
// GitHub names it in its own case and a source keeps the case it was
// added with, so the lookup compares lowercased, and the index does too.
var sourceRepositoryIndexUp = []string{
	`CREATE INDEX IF NOT EXISTS loomplane_project_sources_repository_lower
		ON loomplane_project_sources (lower(repository)) WHERE removed_at IS NULL`,
	`DROP INDEX IF EXISTS loomplane_project_sources_repository`,
}

var sourceRepositoryIndexDown = []string{
	`CREATE INDEX IF NOT EXISTS loomplane_project_sources_repository
		ON loomplane_project_sources (repository)`,
	`DROP INDEX IF EXISTS loomplane_project_sources_repository_lower`,
}

// Sources and plan locksets replaced a project's repository_url, a plan's
// analysis_id and commit_sha, and a deployment's source, and the
// migrations that brought them in copied every value across. Nothing reads
// the old columns, so they go. Going down puts them back and fills them
// from what replaced them: a project's oldest live source, a plan's only
// source when it has just one, and a deployment's first source's key.
var supersededUp = []string{
	`ALTER TABLE loomplane_projects DROP COLUMN IF EXISTS repository_url`,
	`ALTER TABLE loomplane_plans DROP COLUMN IF EXISTS analysis_id`,
	`ALTER TABLE loomplane_plans DROP COLUMN IF EXISTS commit_sha`,
	`ALTER TABLE loomplane_deployments DROP COLUMN IF EXISTS source`,
}

var supersededDown = []string{
	`ALTER TABLE loomplane_projects ADD COLUMN IF NOT EXISTS repository_url TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_plans ADD COLUMN IF NOT EXISTS analysis_id TEXT
		REFERENCES loomplane_analyses (id) ON DELETE CASCADE`,
	`ALTER TABLE loomplane_plans ADD COLUMN IF NOT EXISTS commit_sha TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE loomplane_deployments ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT ''`,
	`UPDATE loomplane_projects p SET repository_url = 'https://github.com/' || s.repository
		FROM (SELECT DISTINCT ON (project_id) project_id, repository
			FROM loomplane_project_sources WHERE removed_at IS NULL
			ORDER BY project_id, created_at, id) s
		WHERE s.project_id = p.id AND p.repository_url = ''`,
	`UPDATE loomplane_plans p SET analysis_id = ps.analysis_id, commit_sha = ps.commit_sha
		FROM loomplane_plan_sources ps
		WHERE ps.plan_id = p.id AND p.analysis_id IS NULL
			AND (SELECT count(*) FROM loomplane_plan_sources x WHERE x.plan_id = p.id) = 1`,
	`UPDATE loomplane_deployments SET source = sources->0->>'key'
		WHERE source = '' AND jsonb_typeof(sources) = 'array' AND sources->0->>'key' IS NOT NULL`,
}

// What an action ran as before it moved and still needs removing, kept
// with what runs it now: a list of bindings, empty for most.
var bindingReplacedUp = []string{
	`ALTER TABLE loomplane_bindings ADD COLUMN IF NOT EXISTS replaced JSONB NOT NULL DEFAULT '[]'`,
}

var bindingReplacedDown = []string{
	`ALTER TABLE loomplane_bindings DROP COLUMN IF EXISTS replaced`,
}
