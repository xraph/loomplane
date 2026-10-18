# Clouds and deployment targets

Status: proposed design, pending product approval. No implementation or live account migration is claimed here.

## What you can do

Create a Cloud for your application and give it targets such as `frontend`, `backend` and `workers`. Each target selects a provider and one connected account. Your services reference the target, so you can replace an account without editing every service that uses it.

For example, `forge-production` can route `frontend` to a Vercel team, `backend` to Cloud Run and `workers` to Kubernetes. Switch `frontend` to another Vercel team and Loomplane redeploys the currently running application there. Future releases use that team too. The user explicitly requested both behaviors.

Changing the provider follows the same target workflow when the new provider supports the service. A target name describes its role, not its vendor. A target configured for Vercel accepts Vercel connections; choosing a different provider is a separate, validated change to that target.

## Confirmed intent and proposed choices

The requested behavior is a Cloud abstraction containing named targets, replaceable providers/accounts, automatic redeployment of the current release on account replacement, and persistent routing of future releases to the replacement.

The remaining choices in this document are proposed implementation decisions. They have not been separately approved:

- A Cloud belongs to one organization and project. An environment selects one Cloud. You can reuse a Cloud across environments in that project, with every affected environment shown before a switch. Separate production and staging Clouds are the default.
- Connecting an account from a target's replacement flow assigns it to that target after verification. Adding an account under Integrations only makes it available. The newest connected account never silently wins.
- A switch is one explicit action, called `Switch and sync`. It authorizes the displayed migration and updates future placement. There is no second approval for the same unchanged migration, but the existing authorization and plan revision checks still apply.
- Sync recreates Loomplane-managed workloads from their applied release state. It does not import arbitrary deployments from the connected account.
- Database movement, storage migration and unmanaged domain transfer need their own workflows. An account switch must report these dependencies and retain the old deployment when they prevent a completed cutover.

## Existing implementation

`internal/connections` stores organization-scoped accounts and keeps credentials in Vault. The existing `Connection.Cloud` field identifies a vendor or infrastructure family, such as `vercel`, `gcp` or `kubernetes`. Keep this internal compatibility field while naming the new product object Cloud; use `CloudProfile` internally if needed to avoid confusion with adapter packages under `internal/clouds`.

`internal/planning/service.go` stores an environment's selected connection by vendor. `ChooseConnection` saves the selection and audit event. It does not start reconciliation. `pkg/manifest/manifest.go` currently lets services select a provider directly, and `target` identifies a connection for owned infrastructure.

`internal/planning/planner.go` already detects connection changes. It proposes service replacement and blocks moving a stateful resource to an empty destination. Bindings record their actual provider and connection, and `Binding.Replaced` retains resources still awaiting cleanup. Reuse these contracts.

`internal/connections.Service.Replace` currently writes new credentials behind an existing connection ID. It must distinguish a token renewal for the same verified account from an account change before Cloud targets depend on immutable connection identity.

## Object model

| Object                   | Responsibility                                                                                                                    |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------- |
| Cloud                    | Project-scoped collection of named targets, with an immutable ID and revision.                                                    |
| Target                   | Stable ID and slug within a Cloud; desired provider, connection, placement settings and revision.                                 |
| Environment assignment   | Selects a Cloud for one environment within the same project.                                                                      |
| Service target reference | Resolves a target slug through the environment's assigned Cloud.                                                                  |
| Resolved placement       | Pins Cloud, target and revisions, provider, connection ID, verified account identity and reviewed settings for an action.         |
| Sync operation           | Durable migration request with actor, affected environments/services, release snapshots, pinned placements, attempts and outcome. |
| Binding                  | Records the actual deployed resource and applied placement, including old resources awaiting cleanup.                             |

Use immutable IDs for references and display names for presentation. Target slugs are stable manifest keys; renaming a display label is harmless. Reject deleting a referenced target, Cloud or connection while bindings, active plans, syncs or pending cleanup still need it. Explicit removal uses the existing deployment removal workflow first.

Targets can serve several services. They carry defaults for placement, not credentials or application source. Service-specific build, port, health and scale settings remain in the manifest. Reject settings that conflict with the selected provider's capabilities.

Desired and applied placement are separate. During a failed switch, the target can desire account B while the binding still reports a healthy deployment on account A. Show both. Never rewrite deployment history to make the switch look successful.

## Manifest and compatibility

Add an explicit `cloud_target` reference to service settings and environment overrides. The selected Cloud belongs to the environment's deployment configuration, so the same manifest can use `frontend` across staging and production.

```yaml
version: 1
services:
  web:
    source: apps/web
    cloud_target: frontend
  api:
    source: cmd/api
    cloud_target: backend
    port: 8080
```

Use `cloud_target` for the initial schema extension because existing `target: conn_...` documents already mean a concrete infrastructure connection. Do not reinterpret their values. A service using `cloud_target` cannot also set direct `provider` or `target` placement, including inherited defaults after environment overrides resolve. Report the conflicting path and offer migration guidance.

Keep existing manifests and environment connection choices working until you explicitly adopt a Cloud. An adoption preview groups services by their resolved placement, proposes target names, and shows the resulting manifest change. Do not infer service roles from framework names without showing the result. Adoption that preserves placement must produce no replacement deployment.

Resources and gateways retain direct placement in the first delivery. Service targets must still validate their dependency paths against those placements. Extending Cloud targets to stateful resources is a later contract with explicit migration support; the initial feature must say so in its editor and API documentation.

## Switching and automatic sync

1. You select an existing compatible connection, or connect one inside the target editor. Verify provider, account identity, permissions and required capabilities before it becomes selectable.
2. Build a preview for every affected environment. Show old/new placement, services to redeploy, dependent services to update, traffic changes, cleanup and any blocking requirements. Snapshot the applied release state and target revision.
3. `Switch and sync` checks target management and deployment approval permissions for the affected scope. Compare the preview's revision with current state. A stale preview has no effect and must be refreshed.
4. Atomically save the desired target revision, scoped migration authorization and durable reconciliation work. Use a transactional outbox or an equivalent durable queue integration so a process crash cannot save the switch without scheduling it.
5. The worker resolves the saved release snapshot into an ordinary deployment plan and executes it through the existing orchestration and executor paths. Automatic approval is limited to the authorized migration's revision and impact. No worker receives general permission to approve unrelated plans.
6. Create the replacement, verify readiness, update supported endpoint references and affected dependents, then publish the applied placement. Cleanup runs through each old binding's original provider and connection.
7. If cleanup fails after cutover, keep a separate cleanup-pending result and retry record. If creation or readiness fails before traffic changes, retain the old serving deployment. If traffic updates partly succeed, retain both deployments and report partial cutover with the exact consumers/routes already changed. Show the failed stage with retry or revert actions.

Future release plans resolve the target's current desired placement. Once approved, their resolved placement is pinned. They must never silently substitute another account at execution time. A target or verified account change makes incompatible pending plans stale.

If you assign a connection to an empty target, save it for the next release; there is no existing application to redeploy. A pending switch waiting for a recoverable connection failure resumes after that exact assigned connection becomes ready. Connecting a different account elsewhere does not resume or redirect it.

Changing an environment's Cloud assignment follows the same preview, authorization and sync contract whenever resolved placement changes. Resolve every service against the proposed Cloud, block missing target names and incompatible placements, and migrate only the affected services. An initial assignment with no deployed services saves configuration for the next release. An assignment or adoption that preserves all resolved placements records the new references without redeploying them. A plain assignment PATCH cannot bypass this behavior.

## Which release gets redeployed

Use the applied service configuration and exact source commit or immutable artifact of each affected binding, together with its repository identity and dependency configuration. Do not read the current branch head or a draft manifest. Multi-repository applications retain their applied commit for each service.

Capture a complete migration snapshot, including versioned references to required secrets and variables. Credentials stay in Vault. If existing storage cannot recover a required applied setting, source archive, artifact or secret version, report the missing prerequisite and block automatic migration rather than inventing a value or copying a plaintext secret into a plan.

Rebuilding a pinned source commit can still produce a different artifact when dependencies are not reproducible. Reuse immutable artifacts when the destination supports them; otherwise label the operation as a rebuild of the deployed source revision. The Vercel adapter currently uploads source and starts a build.

Rollback of application code uses the currently selected target by default. Moving back to the old account is an explicit target switch with its own preview and sync. Historical placements remain available to explain where earlier releases ran.

## Concurrency and recovery

Serialize deployment mutations per environment using the existing locking/orchestration boundary. Give each sync a deduplication key derived from its target revision, environment and release snapshot. Duplicate events, retries and restarts must resume the recorded work rather than start extra deployments.

A switch requested while a release is already executing waits for that release to reach a settled state. Its preview is then recomputed against the actual applied state before authorization. Once a switch is accepted, later releases queue behind its sync and resolve the new placement. A failed sync keeps later automatic mutations paused until retry or revert resolves the failure.

Reject another switch on the same target while its sync is active, with a link to the operation. The initial delivery favors an explicit retry or revert over silently superseding an in-flight account migration. Two targets in one environment also share the environment mutation lock.

For a Cloud used by several environments, each environment has its own operation and result. The aggregate can report partial success; it must not report success until all affected environments converge. Do not automatically undo successful environments because another failed. Revert creates compensating work for each environment that actually changed.

Checkpoint every dependent deployment and traffic update with its before/after value. On partial cutover, stop further changes and cleanup. Retry resumes the unfinished steps after revalidation; it does not redeploy completed work unnecessarily. Revert has a preview and authorization of its own, then restores only changes owned by that operation whose current values still match its recorded after-values. Concurrent external edits block compensation for the affected step. Keep both deployments until recovery establishes the intended traffic state, and report any compensation failure explicitly. There is no automatic claim that all traffic returned to the old deployment merely because it still exists.

Worker recovery must revalidate account identity, permissions, revision and authorization before external mutations. A revoked actor permission or changed deployment policy blocks remaining work. Retain checkpoints, emitted events and provider operation IDs across restart.

## Accounts, networking and cleanup

Token renewal is allowed in place only when verification proves the same provider account and relevant deployment scope. A changed Vercel team, GCP project, AWS account, Kubernetes namespace identity or server destination requires a new connection identity and the switch workflow. Preserve the previous credential reference for existing bindings and cleanup; never overwrite it with credentials for another account.

Provider changes validate workload support, build requirements, environment delivery, dependency reachability and endpoint compatibility. Unsupported combinations produce a concrete blocked reason. Cloud targets select deployment destinations; the existing federation design remains responsible for cross-cloud connectivity and access rules.

Distinguish deployment readiness from traffic cutover. A new provider-generated URL can be recorded and propagated to Loomplane-managed dependents. External consumers and custom domains may require separate actions. Show known routing requirements in the preview, retain the old resource while they are unresolved, and expose an explicit completion step with evidence for routes Loomplane cannot verify. Do not promise automatic domain transfer or zero downtime.

Keep current stateful-resource movement protections. A service switch may continue using its existing database only when the destination can reach it under the configured access rules. Database credentials and connection strings are not evidence that a private network path exists.

## API, dashboard and CLI

Add scoped Cloud/Target CRUD, environment assignment, switch preview, switch execution, sync status, retry and revert APIs. Writes use expected revisions. The switch response returns an operation ID; deployment status and streamed logs remain in the existing deployment views. Publish durable lifecycle events through the existing event path.

Expose the same operations through the generated Forge client and `loomctl`. A target update that changes placement must use switch semantics; a generic PATCH cannot bypass its preview, authorization or reconciliation. Label-only updates need no deployment. Use idempotency keys for switch requests, and validate organization/project scope on every referenced object.

The dashboard shows Cloud selection in the environment and a compact target table with provider, desired account, applied account, sync state and actions. Account replacement stays beside the target. The deployment page links back to the target revision and sync operation. Integrations continues to own account credentials.

Use existing shared components and the shared ZeroState for missing Clouds, empty targets and filtered results. Loading, denied access, failures and an empty result remain distinct. Do not add a walkthrough button unless its registered tour can be started and its anchors work. Verify desktop and narrow layouts with working controls and readable status text.

## Verification and delivery

The implementation plan should cover persistence and scoped APIs, target resolution and compatibility, durable sync with pinned applied state, connection identity protection, then generated clients, CLI and dashboard. These are parts of one feature. A saved target with no working sync does not complete it.

Required behavior checks:

- Switch frontend from account A to B and prove both the currently deployed source revision and the next release use B, while unrelated targets retain their accounts.
- Exercise distinct Vercel teams with disposable resources when test accounts are available. Verify actual provider resources, build results, readiness, credentials used for cleanup and source revision. Fixture tests alone do not establish this.
- Prove newly connected but unassigned accounts never redirect a target. Verify token renewal for the same identity causes no replacement and account substitution cannot overwrite an old binding's identity.
- Reject cross-organization/project references, insufficient switch/deploy permissions, wrong-provider connections, incompatible workloads and stale revisions before mutation.
- Exercise duplicate events, restart between save and enqueue, restart during apply, concurrent release/switch, target deletion, failed readiness, lost credentials, cleanup retry and multi-environment partial success.
- Verify endpoint/dependency updates, custom-domain pending work and blocked stateful moves. Failed cutover retains the previous serving resource.
- Verify exact applied source/configuration selection with newer branch commits and multi-repository partial releases. Missing historical inputs block sync visibly.
- Regenerate and inspect API/client output. Run Go tests, relevant database integration tests, dashboard tests, both TypeScript checks used by the project and client drift checks. Run the documented lint/format/build equivalents and browser checks at desktop and narrow widths.

Report implementation, automated tests and real-provider acceptance separately. Missing test accounts, inaccessible old credentials, unavailable source artifacts or unmanaged routing work remain visible limitations.
