# Clouds and deployment targets

Create an organization Cloud before you create a project. Add named targets, grant a project access, then assign the Cloud to an environment. A Cloud belongs to one organization. Its legacy `project_id` records where it was created, not exclusive ownership. A target selects either a managed provider account or a server pool. Your manifest can reference `frontend` without carrying a particular account ID:

```yaml
version: 1
services:
  web:
    source: node:apps/web
    cloud_target: frontend
```

Use the IDs returned by these commands:

```sh
loomctl clouds create --organization --name Production
loomctl cloud-targets create cloud_... --organization \
  --name frontend --provider vercel --connection conn_...
# Once your project exists, grant all targets or select them with --targets.
loomctl cloud-grants grant cloud_... project_... --targets target_...
loomctl clouds assignment my-project env_...
loomctl clouds assign my-project env_... --cloud cloud_... --revision 0
loomctl clouds assign my-project env_... --cloud cloud_... --revision 0 \
  --yes --request-id assign-production --wait --timeout 10m
```

You can list, create, rename and delete organization Clouds with `--organization`, without a project positional argument. Use the same flag on `cloud-targets` and `cloud-syncs` for organization management. Existing project forms remain available, including `clouds create my-project --name Production` and `cloud-targets show my-project cloud_... target_...`. Assignment commands always require a project.

```sh
loomctl clouds --organization
loomctl clouds show cloud_... --organization
loomctl cloud-grants cloud_...
loomctl cloud-grants revoke cloud_... project_...
loomctl cloud-syncs --organization
```

An empty `--targets` grants the whole Cloud, including its managed default placement. A target list grants only those targets. Revocation prevents fresh placement; it keeps the recorded authority needed to clean up work you already accepted. Sharing capacity does not grant another project's releases, variables, logs or routes.

The first assignment uses revision `0`. Read `clouds assignment` before later changes: an unassigned environment can have a nonzero revision because unassignment retains its generation. You must use that revision when assigning it again. An environment from another project returns a scoped error, never an empty assignment.

## Pick a target by tags

You don't have to name a target. Give your targets tags, then ask for the tags you need:

```sh
loomctl cloud-targets create my-project cloud_... \
  --name eu-gpu --provider cloud-run --connection conn_... --tags eu,gpu
loomctl cloud-targets update my-project cloud_... target_... \
  --display-name "EU GPU" --tags eu,gpu,large --revision 1
```

```yaml
version: 1
defaults:
  cloud_tags: [eu]
services:
  web:
    source: node:apps/web
    cloud_tags: [frontend, eu]
  trainer:
    source: python:trainer
    cloud_tags: [gpu]
    environments:
      staging: { cloud_target: shared-gpu }
```

Over HTTP, send `tags` when you create a target or update one. Leave `tags` out of an update and they stay as they are. Send `[]` and they're removed (`--clear-tags` in `loomctl`). A tag is a lowercase slug of up to 32 characters, and a target carries at most 16.

A service resolves to the one target that carries every tag it lists. Loomplane only looks at the targets your project's grant allows, so a tag on a target you haven't been granted matches nothing. When no target matches, the plan fails with a `cloud-tag-unmatched` finding. When two or more match, it fails with `cloud-tag-ambiguous` and names each of them, so you can add a tag that only one of them carries or name the target with `cloud_target`.

`cloud_target` and `cloud_tags` can't sit together at the same level. An environment override that sets one replaces whichever the service set, so staging above uses `shared-gpu` by name. `defaults.cloud_tags` applies to every service that sets no `cloud_target`, `cloud_tags`, `provider` or `target` of its own, and it can't be combined with `defaults.provider`. Like `cloud_target`, tags conflict with an effective direct `provider` or `target`, including one inherited from defaults.

Changing a target's tags advances its revision. The next plan resolves against the new tags, and a plan approved against the old ones has to be planned again. Adding a new target with the same tags doesn't touch anything already approved, but the next plan will report the overlap.

## One provider without targets

A Cloud can have a default placement: one provider, one connection and an optional region. When a Cloud has no targets, that default is the only place its services go, so a targetless Cloud always uses exactly one provider and account. Its connection has to be ready and belong to that provider's cloud.

```sh
curl -X PUT .../api/v1/clouds/cloud_.../default-placement \
  -d '{"default_placement":{"provider":"vercel","connection_id":"conn_...","region":"iad1"},"revision":1}'
```

You can also pass `default_placement` when you create the Cloud. Send `"default_placement": null` to clear it.

A service uses the default when it has no placement of its own: no `cloud_target`, no `cloud_tags`, and no effective `provider` or `target`. That keeps working after you add targets. Each target brings its own provider, and several providers are fine once targets exist, but adding a target only places the services that name or tag it. Your untagged services stay on the default. A service with its own `provider` keeps its direct placement.

The default reaches a project only when its grant covers the whole Cloud. If an organization admin granted your project a chosen set of targets, your services can use those targets but not the default.

You can set a first default at any time with `default-placement`, because the services it places had nowhere to go before. Renaming the Cloud keeps its default. Once an environment uses the Cloud, `default-placement` refuses to replace or clear it (HTTP 409). Change it through a review instead:

```sh
loomctl clouds switch-default my-project cloud_... \
  --provider vercel --connection conn_new --revision 3
loomctl clouds switch-default my-project cloud_... \
  --provider vercel --connection conn_new --revision 3 \
  --yes --request-id move-default --wait --timeout 30m
```

Over HTTP, that's `POST /clouds/changes/preview` with `{"cloud_id": "cloud_...", "default": true, "provider": "vercel", "connection_id": "conn_new", "revision": 3}`, where `revision` is the Cloud's. Leave out the provider and connection (or pass `--clear`) to remove the default. You accept the preview with `/clouds/changes/apply`, exactly like a target switch, and you need organization Cloud management to do either.

The review covers every environment that uses the Cloud, across projects, and moves only the services the old default placed. Services on a target, or with their own `provider`, keep their placement, though one that depends on a moved service is redeployed so it picks up the new address. The preview shows `before_default` and `after_default`, and the sync records them. Acceptance writes the new default and advances the Cloud's revision in the same transaction that records the work, so plans approved against the old default have to be planned again. Clearing a default that serving services still use is blocked in the review, because they'd have nowhere to go.

One default switch runs per Cloud at a time, and no environment can be assigned to or from the Cloud until it settles. Follow it, complete external routing and retry it with the `cloud-syncs` commands below. `cloud-syncs revert` reviews the old default back, as long as the Cloud still holds the one this sync set.

A Cloud reassignment works the same way for default-placed services: they move to the new Cloud's default, and the review blocks if the new Cloud has none your project can use.

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

To have Loomplane write the DNS records and certificates for hostnames under your own domain, see [domains.md](domains.md).

```sh
loomctl cloud-syncs revert my-project csync_...
loomctl cloud-syncs revert my-project csync_... \
  --yes --request-id restore-frontend --wait --timeout 30m
```

Revert reviews compensation from the actual serving state. Read each environment's `compensation_mode` and `compensation_message`: `restore_retained` restores the exact retained original server runtime, `recreate` deploys saved inputs again, and `mixed` includes both. A managed platform uses saved-input recreation even when an old managed binding remains recorded. Compensation refuses work that changed outside the original operation and retains unfinished resources until cleanup succeeds. You may need external routing evidence for the compensating sync too. Original credentials must remain available until all cleanup finishes.

## HTTP and generated clients

Organization Cloud routes live under `/api/v1/clouds`; they require organization permissions and work before you create a project. Organization change and history collections are `/api/v1/clouds/changes` and `/api/v1/clouds/syncs`. Project grants use `GET /api/v1/clouds/:cloudId/projects` and `PUT, DELETE /api/v1/clouds/:cloudId/projects/:projectId`.

The compatible project routes below live under `/api/v1/projects/:id` and retain project authorization:

| Route                                                 | Operation                                                        |
| ----------------------------------------------------- | ---------------------------------------------------------------- |
| `GET, POST /clouds`                                   | `clouds.list`, `clouds.create`                                   |
| `GET, PUT, DELETE /clouds/:cloudId`                   | `clouds.get`, `clouds.update`, `clouds.delete`                   |
| `PUT /clouds/:cloudId/default-placement`              | `clouds.setDefaultPlacement`                                     |
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

Organization Cloud reads and management require organization Cloud management permission. Compatible project reads require account-read permission. Project placement review and acceptance require account-management and deployment-approval permissions. Project sync reads require deployment-read permission. The recorded actor is checked again before provider mutations and cleanup. Cross-project references fail within the requested scope. Stale, busy and referenced-resource conflicts return HTTP 409; invalid or blocked changes return 422. Credential values and internal snapshots are excluded from public syncs and lifecycle events.

## Qualification limits

Provider capability checks determine whether a proposed workload, region and dependency path can move. Fixture-backed API checks verify persisted switching, historical input recovery and next-release account selection. They do not establish access, quotas, DNS propagation or service behavior in your real provider account. Qualify those against your own configuration before production migration, and keep database or storage migration outside this service-placement workflow.

## Manage servers and pools

Connect your infrastructure account or pinned SSH runtime through Integrations first. Your installed provider adapter declares each operation separately: discovery, provisioning, resize, deletion, DNS and domain attachment are independent capabilities. An unsupported capability is a failure you need to resolve with a supported account or operation. A provider name does not establish runtime support.

```sh
loomctl cloud-servers cloud_...
loomctl cloud-servers options cloud_... conn_...
loomctl cloud-servers preview cloud_... --file server.json
loomctl cloud-servers apply cloud_... infraop_... --review REVIEW_TOKEN --yes \
  --wait --timeout 20m
loomctl cloud-servers show cloud_... infraop_...
loomctl cloud-servers operations cloud_...
loomctl cloud-servers retry cloud_... infraop_... --wait --timeout 20m
loomctl cloud-servers cancel cloud_... infraop_...
```

For a managed server creation, `server.json` has this shape. Get supported region, size and bootstrap image IDs from `options` for your connection:

```json
{
  "kind": "create",
  "name": "host-a",
  "connection_id": "conn_...",
  "region": "nyc3",
  "size": "s-1vcpu-1gb",
  "image": "ubuntu-24-04-x64",
  "cloud_revision": 1
}
```

For adoption, use `{"kind":"adopt","name":"existing-host","connection_id":"ssh_conn_...","cloud_revision":1,"confirm_adoption":true}`. You retain provider ownership of an adopted host. Loomplane can disconnect it, but cannot resize or delete it through this lifecycle. A managed host records the provider resource and creation operation; its deletion uses that recorded ownership. Disconnecting or deleting a host is blocked while workloads, reservations or pool membership still reference it.

To review a resize, disconnect or deletion, supply `kind`, `server_id`, `cloud_revision` and `server_revision`. A resize also needs `size`. The preview returns an operation ID, exact review token, expiry, estimated cost and provider actions. Reviews last 15 minutes. The apply command fetches and prints the saved operation before accepting your exact token; a stale review, denied permission or unsupported provider response remains an error.

Provider creation acceptance does not mean runtime readiness. Follow the operation's provider actions and bootstrap expiry, then use `cloud-servers check cloud_... server_...` to refresh the pinned SSH and Docker health. `--wait` stops at success, failure, cancellation or a waiting checkpoint and returns the latest persisted record on timeout. A waiting checkpoint still needs your attention.

```sh
loomctl cloud-servers usage cloud_... server_...
loomctl cloud-servers drain cloud_... server_...
loomctl cloud-servers drain cloud_... server_... --review DRAIN_DIGEST --yes
loomctl cloud-pools cloud_...
loomctl cloud-pools save cloud_... --file pool.json
loomctl cloud-pools save cloud_... pool_... --file pool-update.json
```

Draining disables new admission after you review current usage. It does not move workloads. Usage includes capacity reservations, serving workloads, execution phases and retained storage markers; those markers do not prove that a volume exists. A pool input gives `name`, `revision` (0 for creation) and `members`, each with `server_id`, `cpu_millis`, `memory_bytes` and `draining`. Budgets must fit verified runtime capacity. Pools group compatible Docker hosts and do not create a private network.

Point a target at a pool without choosing an infrastructure account as the runtime:

```sh
loomctl cloud-targets create cloud_... --organization --name workers \
  --provider server --runtime server --pool pool_... --tags workers
loomctl cloud-targets switch cloud_... target_... --organization \
  --provider server --runtime server --pool pool_new --revision 2
```

A pool service has one bounded replica and one active placement. Ordinary deployments keep the chosen host when possible. To select another host for a stateless service, review a move:

```json
{
  "project_id": "project_...",
  "environment_id": "env_...",
  "service": "web",
  "pool_id": "pool_...",
  "destination_server_id": "server_next"
}
```

```sh
loomctl cloud-pools move cloud_... --file move.json
# Copy the returned review digest into move.json as "review", then accept.
loomctl cloud-pools move cloud_... --file move.json --yes
```

Acceptance saves a 15-minute intent. It starts no deployment. The response includes a conservative local `local_review_and_approve_by` deadline and the exact next `loomctl plan` command for the same project and environment. Review that plan, then run `loomctl approve` with its returned ID before the intent expires. The server expiry is measured from acceptance; the CLI deadline starts just before the request and is not an exact server timestamp. If the source, pool, capacity or grant changes, plan again. Local data requires a separate explicit migration.

To recover an unfinished accepted reservation, use `loomctl cloud-pools recover cloud_... --file recovery.json` with `{"plan_id":"plan_...","service":"web"}`. Read its execution revision, active/started state and digest. Copy the digest into the input's `review` field, then repeat with `--yes`. Recovery fences new attempts before checking exact owned compute cleanup and releasing capacity. It retains local storage and reports `storage_retained`; that field does not establish a backup or a successful data migration. A serving workload requires a reviewed delete or move. Failed cleanup stays blocked until you restore the recorded connection or reconcile its checkpoint, then review again. Do not clear a reservation by editing database rows. Pending mixed managed/server attempts without proof that provider work stopped remain blocked: reconcile or finish the original operation, then review again. An unfinished legacy unfenced server attempt also requires reconciliation; blind retry does not establish that old execution has stopped.

## Keep domain authority separate

DNS zones belong to the organization independently of a Cloud or compute connection. Grant zones to projects explicitly. Managed mode uses a supported DNS account. Both modes return a TXT ownership challenge that you publish and verify before using the namespace. Provider hosting access alone does not prove control. This configures DNS for a domain you already own, not domain registration or registrar nameserver changes.

```sh
loomctl dns-zones
loomctl dns-zones create --file zone.json
loomctl dns-zones verify zone_...
loomctl dns-zones project my-project
loomctl dns-zones update zone_... --file zone-update.json
loomctl dns-zones remove zone_... --revision 3 --yes
```

Use `{"name":"example.com","mode":"external","project_ids":["project_..."]}` for an external zone, or `mode: "managed"` with `connection_id` for a DNS adapter. A zone update carries `revision`, `project_ids` and `renew_proof`. Renew an expired unverified proof with `renew_proof: true`, then publish the returned replacement TXT value. Managed zones saved by older versions with only a provider verification timestamp also need Renew proof and public verification. Referenced zones cannot be removed.

The zone name is the namespace you grant to projects. For managed DNS, Loomplane selects the most specific containing hosted zone and pins its provider ID, account and connection generation separately. You can grant `app.example.com` while writing records in the hosted zone `example.com`: publish the challenge at `_loomplane.app.example.com`. That proof covers `app.example.com` and its children, never `example.com` or sibling names. Record names remain relative to the provider hosted zone. Each new route review and acceptance rechecks the public challenge and provider identity. Removing or changing the TXT blocks new work; previously accepted exact cleanup keeps its retained authority.

```json
{
  "zone_id": "zone_...",
  "cloud_id": "cloud_...",
  "environment_id": "env_...",
  "service": "web",
  "hostname": "app.example.com",
  "port": 8080,
  "external_ttl_seconds": 7200
}
```

```sh
loomctl domain-routes preview my-project --file route.json
loomctl domain-routes apply my-project routeop_... --review REVIEW_DIGEST --yes \
  --wait --timeout 15m
loomctl domain-routes my-project
loomctl domain-routes show my-project routeop_...
loomctl domain-routes verify my-project route_... --wait --timeout 10m
loomctl domain-routes retry my-project routeop_... --wait --timeout 10m
loomctl domain-routes history my-project route_...
loomctl domain-routes ttl my-project route_... --revision 3 --external-ttl-seconds 7200
loomctl domain-routes remove my-project route_... --revision 4 --yes
```

Keep DNS, TLS and backend verification separate. A route operation prints its desired record, applied receipt, message and `not_before` drain gate. For external DNS, publish the exact record from the operation and verify again. A server route needs the public hostname to reach its ingress and HTTP-01 challenge before public certificate issuance can succeed. Fixture certificates and local ACME checks do not qualify your domain for public ACME.

`external_ttl_seconds` is the maximum TTL previously served by your external DNS records, including any still cached value. You must establish that bound yourself, between 60 and 86400 seconds. Resolver success and the requested new TTL cannot establish it. The TTL update requires the ready route's exact revision, advances that revision and invalidates old reviews; it refuses a pending handoff. Legacy accepted work missing the bound needs reviewed recovery, not an edit to an immutable snapshot. Managed handoffs also retain the maximum prior and current recorded provider receipt TTL, including receipts above the external-input limit.

During a routed release, old and new compute can coexist while DNS changes and cached traffic drain. Cloud sync output preserves each environment's `routing_status` and route operation IDs, manual DNS instructions and `not_before` timestamp. Desired target placement may already be saved while a previous release still serves. Follow the applied plan and deployment IDs before deciding the move finished. Route histories remain project scoped, including on a shared Cloud.

If a legacy runtime lacks a trusted current container/release receipt, redeploy it through the reviewed deployment flow before attaching a route. DNS-only retries cannot repair missing runtime identity. Stop every older app and worker binary before running the migrations and start only the upgraded binaries afterward. Mixed versions with unfenced writers are unsupported.

The Go client exposes typed Cloud, server, pool, zone and route operations through the same authenticated transport as `loomctl`. Infrastructure operations live under `/api/v1/clouds/:cloudId`; zones under `/api/v1/dns-zones`; project route operations under `/api/v1/projects/:projectId/domain-routes` and `/route-operations`. Review inputs, revisions and cleanup authority remain enforced by those APIs.

## Review Kubernetes cluster setup

Use Infrastructure, Clusters inside an organization Cloud. The existing `servers` tab URL still opens Infrastructure. Project members use their granted targets; cluster discovery, account details and lifecycle operations require organization Cloud management permission.

The CLI follows the existing `cloud-servers` naming convention:

```sh
loomctl cloud-clusters list cloud_...
loomctl cloud-clusters show cloud_... resource_...
loomctl cloud-clusters options cloud_... conn_... --region nyc1
loomctl cloud-clusters discover conn_... --cursor next-page
loomctl cloud-clusters preview cloud_... --file cluster.json
loomctl cloud-clusters apply cloud_... operation-id --review exact-digest --yes --wait --timeout 10m
loomctl cloud-clusters operation cloud_... operation-id --wait --timeout 10m
loomctl cloud-clusters retry cloud_... operation-id --wait --timeout 10m
loomctl cloud-clusters cancel cloud_... operation-id
```

A wait timeout prints the latest saved checkpoint. Keep its operation ID and resume it; creating another preview is not a recovery step for an accepted operation. Retry rechecks current authority, and a stale review requires fresh options and another review. Cancellation does not promise provider deletion.

Preview accepts `create`, `adopt-external` or `adopt-managed`. Pin the Cloud revision, connection generation, verified account identity, options digest and capacity policy revision. Runtime attachment input also pins the project grant revision, namespace, target slug and service account. Managed adoption requires the saved managed resource ID and revision with its creation receipt. External adoption preserves external lifecycle ownership. The public API has no raw cluster enrollment endpoint.

The optional qualification runner is `tests/acceptance/managed-clusters/run.py`. It requires a disposable configuration, explicit account/project/region bounds, fixed node and cost ceilings, a digest-pinned worker image, and commands for worker restart, deployment/readiness, access revocation/restoration and cleanup inventory. Commands return scoped JSON evidence; the runner rejects mismatched identities and nonempty cleanup inventory. Run its fixture tests with `python3 -m unittest discover -s tests/acceptance/managed-clusters`. Paid execution additionally requires `--execute-paid-disposable-qualification`, a private state path and `LOOMPLANE_QUALIFICATION_TOKEN`. Those fixture tests make no provider purchases and establish no live qualification.
