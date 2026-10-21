# Clouds and deployment targets

Create a Cloud for your project, add named targets, then assign the Cloud to an environment. Each target selects a provider and a verified connection. Your manifest can reference `frontend` without carrying a particular account ID:

```yaml
version: 1
services:
  web:
    source: node:apps/web
    cloud_target: frontend
```

Use the IDs returned by these commands:

```sh
loomctl clouds create my-project --name Production
loomctl cloud-targets create my-project cloud_... \
  --name frontend --provider vercel --connection conn_...
loomctl clouds assignment my-project env_...
loomctl clouds assign my-project env_... --cloud cloud_... --revision 0
loomctl clouds assign my-project env_... --cloud cloud_... --revision 0 \
  --yes --request-id assign-production --wait --timeout 10m
```

The first assignment uses revision `0`. Read `clouds assignment` before later changes: an unassigned environment can have a nonzero revision because unassignment retains its generation. You must use that revision when assigning it again. An environment from another project returns a scoped error, never an empty assignment.

## Adopt an existing service

Keep your existing manifest until you choose to adopt a Cloud. Create a target with the service's current provider, connection and region, assign that Cloud to its environment, and save a manifest revision that sets `cloud_target` while removing effective direct `provider` and `target` settings. Check inherited defaults and environment overrides too. A service cannot resolve both forms of placement.

Review the ordinary deployment plan before approving it. When the resolved placement and service configuration match what already serves, the plan records Cloud metadata without a provider deployment. If the plan proposes replacement, check the account, region and effective settings before approval.

Cloud targets currently place services. Resources and gateways retain their direct placement. Dependency reachability is checked against those placements; a Cloud switch does not move database or storage contents.

## Replace an account or provider

Connect the destination account through Integrations first. Renewing a token for the same verified account keeps its connection ID and does not move services. Replacing that account requires a new connection. Stable provider identities, such as a Vercel user or team ID, distinguish renewal from replacement. If an older connection lacks its required immutable identity, reconnect it before using it for a switch.

```sh
loomctl cloud-targets show my-project cloud_... target_...
loomctl cloud-targets switch my-project cloud_... target_... \
  --provider vercel --connection conn_new --revision 1
```

Read the preview. It includes the old and proposed placement, affected environments and services, action counts, external routing requirements and blockers. A Cloud shared by production and staging can affect both environments. Use separate Clouds when their destinations should change independently.

To accept a review you already saved, use its ID and digest:

```sh
loomctl cloud-syncs apply my-project \
  --preview cpv_... --revision REVIEW_DIGEST \
  --request-id switch-frontend-2026-10-20 --yes --wait --timeout 30m
```

The command fetches and displays that exact preview before applying it. Alternatively, repeat `cloud-targets switch` with `--yes`, which creates and displays a fresh review before accepting it. Placement and assignment previews expire after 24 hours. Revision changes or altered serving inputs require another review.

The idempotency key belongs to acceptance. Reuse the same preview, digest and `--request-id` after a lost response to recover the original sync ID. A new preview represents a separate request, even if its destination looks identical.

Switching migrates the release that currently serves, using each binding's saved source artifact and encrypted variable snapshot. It does not deploy an unapproved draft. Missing historical inputs block the switch. Future ordinary releases resolve the newly selected account from the saved target, while deployments already approved against an older revision must be reviewed again.

A service region set in the manifest, its environment override or manifest defaults stays pinned when you change a target's default region. Services that inherit the target or provider default follow the new default. Older bindings without recorded region provenance keep their applied region during sync. Deploy an ordinary release to record that provenance before relying on a target default change to move them.

## Follow and recover a sync

```sh
loomctl cloud-syncs my-project
loomctl cloud-syncs show my-project csync_...
loomctl cloud-syncs wait my-project csync_... --timeout 30m
loomctl cloud-syncs retry my-project csync_... --wait --timeout 30m
```

Finish or revert unfinished Cloud syncs before renaming a Cloud or target in the same project. This temporary project-wide restriction keeps the reviewed revisions valid through failed deployments, routing and cleanup recovery.

Use `--json` for scripts. Cloud records use the same JSON fields as the HTTP API. Review-and-apply commands emit the preview followed by the sync as separate JSON documents. History is paginated; pass the returned `next` value as `--before` to read older operations.

| State             | Meaning and next action                                                          |
| ----------------- | -------------------------------------------------------------------------------- |
| `pending`         | Accepted and persisted; Dispatch will schedule or recover it.                    |
| `running`         | Services are being reconciled from recorded inputs.                              |
| `routing_pending` | Managed deployment finished, but you must verify external routing.               |
| `cleanup_pending` | Old resources are being removed. An error means cleanup needs recovery or retry. |
| `failed`          | Inspect per-environment errors, restore access or dependencies, then retry.      |
| `succeeded`       | Every affected environment and its cleanup finished.                             |
| `reverting`       | A compensating operation owns the remaining work. Follow its sync ID.            |
| `reverted`        | Compensation finished and original ownership was released.                       |

Waits stop at success, failure, external routing, or a cleanup error. They continue through active cleanup. A timeout returns an error and the latest record so you can resume following its ID. Restarting Loomplane recovers committed work; a periodic recovery pass handles accepted operations whose enqueue failed. It does not grant new permissions.

Desired placement changes when acceptance commits. Applied account, plan and deployment IDs describe what actually serves in each environment. Partial completion is retained: a successful environment stays deployed when another fails. Old resources remain until managed consumers and required external routing have completed. Failed operations retain their guards, preventing an ordinary release from racing unfinished migration or cleanup.

## Complete external routing or revert

If you control DNS or another external entry point, update it and verify traffic reaches the new service. Record that check for each affected environment:

```sh
loomctl cloud-syncs routing my-project csync_... \
  --environment env_... --evidence 'Production DNS and HTTPS verified' \
  --wait --timeout 10m
```

This records your explicit evidence and schedules remaining cleanup. It does not perform DNS changes. Without that evidence, Loomplane keeps the old resources.

```sh
loomctl cloud-syncs revert my-project csync_...
loomctl cloud-syncs revert my-project csync_... \
  --yes --request-id restore-frontend --wait --timeout 30m
```

Revert reviews compensation from the actual serving state. It refuses work that changed outside the original operation, restores the recorded source and account, and retains unfinished resources until cleanup succeeds. You may need external routing evidence for the compensating sync too. Original credentials must remain available until all cleanup finishes.

## HTTP and generated clients

All routes require organization and project authorization under `/api/v1/projects/:id`:

| Route                                                 | Operation                                                        |
| ----------------------------------------------------- | ---------------------------------------------------------------- |
| `GET, POST /clouds`                                   | `clouds.list`, `clouds.create`                                   |
| `GET, PUT, DELETE /clouds/:cloudId`                   | `clouds.get`, `clouds.update`, `clouds.delete`                   |
| `GET, POST /clouds/:cloudId/targets`                  | `cloudTargets.list`, `cloudTargets.create`                       |
| `GET, PUT, DELETE /clouds/:cloudId/targets/:targetId` | `cloudTargets.get`, `cloudTargets.update`, `cloudTargets.delete` |
| `POST /cloud-changes/preview`                         | `cloudChanges.preview`                                           |
| `GET /cloud-changes/:previewId`                       | `cloudChanges.getPreview`                                        |
| `POST /cloud-changes/apply`                           | `cloudChanges.apply`                                             |
| `GET /environments/:environmentId/cloud`              | `environmentClouds.get`                                          |
| `POST /environments/:environmentId/cloud/preview`     | `environmentClouds.preview`                                      |
| `GET /cloud-syncs`                                    | `cloudSyncs.list`                                                |
| `GET /cloud-syncs/:syncId`                            | `cloudSyncs.get`                                                 |
| `POST /cloud-syncs/:syncId/retry`                     | `cloudSyncs.retry`                                               |
| `POST /cloud-syncs/:syncId/revert/preview`            | `cloudSyncs.previewRevert`                                       |
| `POST /cloud-syncs/revert/apply`                      | `cloudSyncs.applyRevert`                                         |
| `POST /cloud-syncs/:syncId/routing`                   | `cloudSyncs.completeRouting`                                     |

Acceptance takes `preview_id`, the preview's `revision` digest and `idempotency_key`, and returns HTTP 202 with the persisted sync ID. Assignment reads return `{ "assignment": null, "revision": 0 }` for an environment never assigned, with a higher generation after unassignment. Metadata updates use integer revisions. History accepts `before` and `limit` (30 by default, at most 100).

Cloud configuration reads require account-read permission. Placement review and acceptance require account-management and deployment-approval permissions. Sync reads require deployment-read permission. The recorded actor is checked again before provider mutations and cleanup. Cross-project references fail within the requested scope. Stale, busy and referenced-resource conflicts return HTTP 409; invalid or blocked changes return 422. Credential values and internal snapshots are excluded from public syncs and lifecycle events.

## Qualification limits

Provider capability checks determine whether a proposed workload, region and dependency path can move. Fixture-backed API checks verify persisted switching, historical input recovery and next-release account selection. They do not establish access, quotas, DNS propagation or service behavior in your real provider account. Qualify those against your own configuration before production migration, and keep database or storage migration outside this service-placement workflow.
