# Cloud and Targets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give each project named Clouds and Targets, with persistent release routing and automatic migration of the deployed release when its account or provider changes.

**Architecture:** Add `internal/cloudprofiles` for scoped Cloud configuration and durable change records. Resolve manifest `cloud_target` references into pinned actions in the existing planner, then execute migrations through the existing deployment orchestration and adapters. Keep desired placement separate from actual bindings, retain old resources until affected services finish cutover, and use the existing Vault, authorization, audit and event systems.

**Tech Stack:** Go, PostgreSQL/Grove, Forge routes and generated clients, Dispatch durable workflows, Warden, Vault, React and the existing dashboard components.

**Spec:** `docs/superpowers/specs/2026-10-18-cloud-targets-design.md`

## Global Constraints

- Work directly on `main` in the current primary checkout. Preserve concurrent edits and commit only owned paths. Push verified changes; do not merge or create worktrees.
- A Cloud belongs to one organization and project. An environment selects one Cloud. Resources and gateways retain direct placement in this delivery.
- Switching migrates the applied release and routes future releases to the chosen account. Adding an unassigned account never redirects a target.
- Preserve direct manifest `provider` and `target` compatibility. Add `cloud_target`; reject conflicting effective placement after overrides.
- Use immutable connection identity for account changes. Token renewal for the same verified account remains available.
- A placement-changing write requires a revision-bound preview and deployment authorization. Persist desired configuration and durable work atomically.
- Never use current branch heads or draft configuration for migration. Missing applied inputs block migration with an actionable reason.
- Never remove old resources before all affected consumers are ready. Preserve partial cutover and cleanup-pending state across retries and restarts.
- Match the approved React mock's compact layout, shared ZeroState and existing generated-client patterns. Verify desktop and narrow widths.
- Invoke rex-voice and embedded humanizer for shipped prose; use no em dashes or attribution trailers.
- There are no `make l`/`make f` targets. Use `make lint`, configured `golangci-lint fmt` on owned Go files, and dashboard Prettier on owned TS/TSX files. Run relevant tests/builds after formatting. Record unrelated concurrent failures without rewriting others' work.

## Review Focus

- Two targets using the same vendor but different accounts must resolve independently; test this in Task 2.
- Assignment changes can redirect services without editing targets; test the same migration contract in Task 3.
- Partial releases across repositories must retain each binding's applied source and settings; test this in Task 3.
- Duplicate/replayed switch requests and a crash after saving desired state must not lose or duplicate migration; test this in Task 3.
- An old cleanup credential must never be replaced by a new account's token; test this in Tasks 1 and 3.

## Task 1: Scoped configuration and account identity

**Files:** Create `database/migrations/cloud_profiles.go`, `internal/cloudprofiles/{model,store,pgstore,service,service_test}.go`, `internal/connections/identity.go`, `internal/connections/identity_test.go`, `tests/integration/cloud_profiles_test.go`. Modify `database/migrations/migrations.go`, `internal/connections/service.go`, and connection-use checks only as necessary.

**Interfaces:**

- `Profile`: ID, OrgID, ProjectID, Name, Revision, CreatedAt, UpdatedAt.
- `Target`: ID, OrgID, ProjectID, CloudID, Name (immutable slug), DisplayName, Provider, ConnectionID, Region, Revision, CreatedAt, UpdatedAt.
- `Assignment`: OrgID, ProjectID, EnvironmentID, CloudID, Revision and resolved `Targets []Target`.
- `Resolver.Resolve(ctx context.Context, orgID, projectID, environmentID string) (*Assignment, error)`: returns nil when no Cloud is assigned; verifies all scope boundaries.
- `NewPgStore(db *pgdriver.PgDB) *PgStore`, `NewService(Deps) *Service`; dependencies include Projects, Connections, Providers, Authz and Audit using existing interface conventions.
- `Service.List/Create/Get/Update/Delete` for Profiles; `ListTargets/CreateTarget/UpdateTarget/DeleteTarget` for targets. UpdateTarget changes display metadata only. Placement updates are reserved for Task 3.
- `connections.AccountIdentity(cloud, account string, details map[string]string) string`: stable verified identity including relevant provider scope, excluding permission check timestamps and credential material.

- [ ] Add failing service/store tests for cross-project and cross-organization IDs, duplicate target slugs, invalid provider/connection pairing, optimistic revision conflicts and protected deletion.
- [ ] Implement scoped tables with composite foreign keys and indexes, monotonic revisions, deterministic listing and existing error conventions. Use migration version `20261018000400`. Assigned Clouds/targets and referenced connections cannot be deleted through CRUD.
- [ ] Implement creation and metadata editing with CloudAccountRead/CloudAccountManage permissions, capability validation and audit events. Initial target creation can select a ready matching connection. Assignment writes remain internal to the store until Task 3 exposes reviewed changes.
- [ ] Add failing tests proving `Replace` rejects a different verified account before changing Vault, while the same account's token can rotate. Include Vercel team and Kubernetes namespace identity changes.
- [ ] Implement account identity protection and preserve existing old-account credentials. Recheck must not silently accept a different identity for an existing connection either.
- [ ] Run `go test ./internal/cloudprofiles ./internal/connections`, migration checks and focused database integration tests. Format owned files, run lint, inspect and commit.

## Task 2: Manifest targets and pinned release routing

**Files:** Modify `pkg/manifest/{manifest,edit,schema}.go` and schema JSON as required, `internal/planning/{service,planner,targets,plan}.go`, `internal/deployments/runner.go`. Create `internal/planning/cloud_targets.go`, `internal/planning/cloud_targets_test.go`, and focused manifest/deployment tests.

**Interfaces:**

- `manifest.Settings.CloudTarget string` serialized as `cloud_target`, including environment overrides and schema/edit support.
- `planning.Deps.CloudProfiles cloudprofiles.Resolver` and matching Service field.
- `planning.CloudPlacement`: CloudID, CloudRevision, AssignmentRevision, TargetID, TargetRevision, TargetName, Provider, ConnectionID, AccountIdentity and Region.
- `planning.ServiceConfig.CloudPlacement *CloudPlacement` persists resolved placement inside action/binding configuration.
- `planning.ResolveCloudTargets(manifest *manifest.Manifest, environment string, assignment *cloudprofiles.Assignment, connections []ConnectionOption) (*manifest.Manifest, map[string]CloudPlacement, error)` returns a copy, never mutating stored manifests.

- [ ] Add failing tests for two Vercel targets choosing different accounts; absent assignment/target; wrong scope; conflicting inherited/default placement; supported overrides; legacy direct-target compatibility; and unchanged-placement adoption without replacement.
- [ ] Resolve each service's target before provider planning, pin connection IDs per action for managed and owned providers, and include placement revision and account identity in plan digest.
- [ ] Keep build/health/scale and resource placement rules intact. Never choose a vendor default when a named target is missing or unready.
- [ ] Validate pinned identity/settings before execution. Revalidate target/assignment revisions at execution boundaries so a queued stale plan cannot deploy elsewhere. Persist actual Cloud placement in bindings through ServiceConfig.
- [ ] Run `go test ./pkg/manifest ./internal/planning ./internal/deployments`, schema checks and regression tests for same-provider account replacement. Format, lint, inspect and commit.

## Task 3: Durable switch, assignment, retry and revert

**Files:** Create `database/migrations/cloud_sync.go`, `internal/cloudprofiles/{change,sync_store}.go`, `internal/planning/cloud_sync*.go`, `internal/orchestration/cloud_sync.go`, `internal/deployments/cloud_sync.go`, and matching tests. Modify migration registration, existing orchestration registration, planning/deployment hooks and environment variable snapshot support where needed.

**Interfaces:**

- `cloudprofiles.ChangeInput`: expected revision, target ID or environment assignment change, new provider/connection/region or CloudID, and caller idempotency key.
- `cloudprofiles.Preview`: immutable ID/revision, before/after placement, affected environments/services, plan summaries and blocking reasons. Preview records persist their exact applied-input snapshot server-side.
- `cloudprofiles.Sync`: ID, OrgID, ProjectID, CloudID, TargetID, Status, CreatedBy, Before/After, per-environment plan/deployment IDs, timestamps and errors. Persist enough state for retry, compensation and cleanup.
- `planning.Service.PreviewCloudChange(ctx, principal, projectID, input) (*cloudprofiles.Preview,error)`, `ApplyCloudChange(ctx, principal, projectID, previewID, revision, key string) (*cloudprofiles.Sync,error)`, `CloudSync/CloudSyncs`, `RetryCloudSync`, `PreviewCloudRevert` and `ApplyCloudRevert` using the same preview contract.
- `orchestration` adds Cloud reconciliation handlers and queue recovery with deterministic request IDs. Durable records are the source of truth, with startup/periodic recovery of pending work.

- [ ] Add failing tests for exact per-binding applied source/configuration with newer repository analyses, empty targets, assignment changes and same-placement adoption.
- [ ] Build ordinary deployment plans from applied bindings and their original plan source references. Include managed dependent endpoint updates. Capture secret/variable version references or encrypted snapshots through Vault; never expose values in the preview. Historical inputs that cannot be recovered block the change.
- [ ] Persist revision-bound previews. Apply verifies actor permissions and all reviewed inputs, atomically updates desired configuration and inserts sync work. Repeated requests return the same operation; stale previews and second active target switches fail without mutation.
- [ ] Serialize with ordinary deployments per environment, including approved-but-not-started plans and retries. Active release work prevents accepting a stale switch preview. Later releases wait behind accepted sync; failed sync keeps them paused. Use database-backed guards, not a process-local mutex.
- [ ] Execute through existing planner/deployer machinery with narrowly scoped automatic authorization. Revalidate authorization, account identity and placement before mutations. Resume exact provider operations on restart, retaining their checkpoints.
- [ ] Defer old-resource cleanup until replacements and affected dependents finish. Track cleanup-pending separately. For partial cutover, retain both placements and checkpoint each changed consumer. Retry resumes work; revert previews compensating changes and rejects external changes to recorded after-values.
- [ ] Add explicit completion for external routing that Loomplane cannot verify, retaining old resources until completion. Keep database/storage moves blocked and show the reason.
- [ ] Exercise multi-environment partial success, duplicate delivery, crash between commit/enqueue, restart during execution, token renewal, denied actor, cleanup retry, rapid switch/release races and unavailable old credentials in unit and PostgreSQL integration tests.
- [ ] Run focused tests, restart/recovery integration tests, formatting and lint. Commit a coherent verified implementation.

## Task 4: Public API, CLI and deployment wiring

**Files:** Create `extensions/planning/clouds.go`, `pkg/client/clouds.go`, `internal/ctl/clouds.go`, corresponding tests and `docs/clouds.md`. Modify `extensions/planning/extension.go`, route registration, `internal/ctl/commands.go`, and deletion guards where necessary.

**Interfaces:**

- Forge routes under `/api/v1/projects/:id/clouds`, target subresources, environment Cloud assignment previews, and `/api/v1/projects/:id/cloud-syncs` with preview/apply/get/list/retry/revert operations.
- Named operations `clouds.list/create/get/update/delete`, `cloudTargets.list/create/update/delete`, `cloudChanges.preview/apply`, `cloudSyncs.list/get/retry/previewRevert/applyRevert` and `environmentClouds.get/preview`.
- `loomctl clouds`, `loomctl cloud-targets`, and `loomctl cloud-syncs` expose the same revision/idempotency contracts with JSON output and bounded waits.

- [ ] Wire Profile store/service and resolver into the existing planning extension and workflow lifecycle. Register recoverable sync scheduling only after dependencies are ready.
- [ ] Add guarded typed routes with request/response schemas, scoped errors, pagination where appropriate and operation IDs compatible with Forge client generation. Never expose credential snapshots.
- [ ] Implement CLI commands for create/list/show/update/delete, assignment preview/apply, target account/provider switch, sync wait/retry/revert and external routing completion. Display exact previews before executing a switch.
- [ ] Add end-to-end API tests for denial, cross-scope references, revision conflicts, idempotency, persistent listing after restart, switching the current release and deploying the next release to the chosen account.
- [ ] Document manifest adoption, token renewal versus account replacement, sync states, external routing completion and real-provider qualification limits.
- [ ] Run relevant CLI/API integration tests, format/lint/build and commit.

## Task 5: Dashboard Cloud flow

**Files:** Create `apps/dashboard/src/pages/clouds.tsx`, `apps/dashboard/src/components/cloud-targets/*`, `apps/dashboard/src/routes/clouds.tsx`, dashboard behavior tests. Modify project/environment controls, shared sidebar navigation and generated Forge specs/clients.

**Interfaces:** Consume Task 4's generated hooks and types. Use the shared session, query, permission and event mechanisms. Keep the mock route available as a design reference.

- [ ] Regenerate specs and clients from the running local API; inspect operation/envelope metadata and generated types. Do not handwrite parallel HTTP clients.
- [ ] Implement project/environment Cloud selection, Cloud create/rename/delete, target create/edit/delete and service-target assignment/adoption through real manifest APIs. Surface required service edits with an actual save action.
- [ ] Port the approved compact mock using real providers and connections. Provide account/provider switch previews, connecting an account through existing Integrations controls, current versus desired account, affected services and confirmation.
- [ ] Implement sync status, per-environment outcomes, deployment/log links, retry, revert preview/apply, external routing completion and deletion denial states. Refresh from persisted API results and existing events or bounded polling.
- [ ] Add behavior tests for stale preview, denied actions, failed sync, partial success, retry, empty/filter/error distinctions and project/environment scope changes. Use shared ZeroState and no unregistered tours.
- [ ] Run Prettier on owned files, `eslint .`, `tsc --noEmit`, `tsc -b`, dashboard tests, client drift checks and Vite build. Verify working routes and dialogs at desktop/narrow widths in the browser. Commit.

## Task 6: Full-flow acceptance and final review

**Files:** Create `tests/integration/cloud_flow_test.go`, `docs/qualification/cloud-targets.md`, and provider acceptance coverage under the existing acceptance harness. Fix owned implementation files as findings require.

- [ ] Prove fixture-provider flow through real persisted API/CLI: Cloud and targets, manifest adoption, initial deploy, account A to B migration of the same source, later release to B, isolation of other targets, provider replacement, restart recovery, failure/retry/revert and safe deletion.
- [ ] Verify dashboard flows against the running API with real persisted responses and generated clients; capture desktop/narrow evidence. Do not use the mock as acceptance evidence.
- [ ] Inspect available test infrastructure without exposing secrets. Run live Vercel account-switch acceptance only with available authorized test accounts; record missing credentials or unmanaged-domain steps explicitly. Preserve unrelated resources and remove only task-created disposable resources.
- [ ] Run final formatting, `make lint`, `make test`, relevant integration tests, `make build`, `make check-client` and generated schema checks. Inspect all owned diffs and concurrent state.
- [ ] Obtain independent final spec/security/concurrency review, fix findings and rerun affected checks. Document implemented, automated-tested, browser-verified and live-provider-verified status separately.
- [ ] Commit and push completed verified work. Report remaining external verification blockers without claiming they passed.
