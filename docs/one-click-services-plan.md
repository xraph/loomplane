# One-click services, persistence, and recovery

Status: implementation in progress. Source review: 18 October 2026. The verification ledger below records completed slices; the remaining sections describe the target design.

## Implementation and verification ledger

### Trove extension foundation, 18 October 2026

Implemented: published Trove root, extension and S3 driver v1.7.1 pins; extension-owned artifact and backup stores; central Trove metadata migrations; configurable independent backup URL and bucket; existing artifact bucket/key compatibility; borrowed artifact handles do not close the shared driver. Remote S3 buckets must already exist. Local buckets are created at startup.

Verified: local artifact contents survive extension restart; artifact and backup stores remain separate; shared storage survives closing an artifact handle; the full Forge application starts, migrates and survives restart against real PostgreSQL. `make test` passed, including 227 dashboard tests. Targeted storage/configuration tests passed.

Not yet verified: a live AWS S3 connection, persistent deployed-service volumes, PostgreSQL service deployment/backup/restore, UI and CLI service management. This slice does not qualify any new catalog service.

Formatting uses the repository's configured `golangci-lint fmt` on owned Go files and Prettier on this document. There are no `make f` or `make l` aliases; lint uses `make lint`, or its identical Go command with `--allow-serial-runners` followed by `make lint-dashboard` when another checkout holds the global linter lock.

You should be able to pick a service, choose a connected target, and deploy it with working credentials, durable storage, health checks, and a recovery policy. Once you've configured an environment's defaults, the normal path is one Deploy action. The same recipe must work from the dashboard, loomctl, and the API.

We should build the storage and recovery foundation first, then qualify services in batches. A catalog entry becomes supported only after its deployment, upgrade, backup, restore, and removal paths pass on the targets it advertises.

### Bounded backup transfer, 18 October 2026

Implemented: backup transfer through Trove's selected driver, 8 MiB multipart chunks, bounded part retries, abort on failure/cancellation, SHA-256 and size metadata, version-pinned reads, and verification while downloading to an isolated destination. Local writes use Trove's atomic file replacement. The multipart path currently caps objects at 10,000 parts (about 78 GiB); crash-resumable upload checkpoints and orphan reconciliation remain pending.

Verified: corruption detection, interrupted-source rejection, multipart failure cleanup and chunk bounds in focused tests. A real isolated MinIO S3 endpoint accepted and returned 129 MiB through Trove with matching SHA-256 while the test ran with `GOMEMLIMIT=64MiB`. This is a Go soft memory target, not a measured process-memory ceiling or AWS S3 acceptance. The test container is isolated from existing application storage.

Validation: Go lint and the full Go suite passed. The shared checkout dashboard checks are blocked by concurrent deployment UI edits: a `setup` prop type error and a deployment-target selection test failure. This backend slice does not edit those files.

Pending: AWS S3 credentials/acceptance, process-crash recovery of incomplete uploads, policy-managed retention/encryption, PostgreSQL recovery workflows and restore authorization. Successful object transfer alone does not establish a usable database backup.

### Docker persistent volumes, 18 October 2026

Implemented: stable named volumes per instance and mount role, organization/project/environment/connection ownership checks, read-back after idempotent creation, explicit persistent mounts and retention on container removal. Rollouts reject changed mount roles or paths. Host binds and implied disk quotas are rejected. Docker local volumes share host capacity; this adapter does not promise a reserved size or host-loss durability.

Verified: ownership collision and invalid mount tests; a real PostgreSQL 17 container retained an inserted row through replacement, removal and reattachment. The acceptance test used a unique container, network and volume, then removed its own resources.

Validation: server package tests passed. Full lint and test runs encountered concurrent federation tests referencing unfinished types. These checks must be repeated after that work settles; this slice does not modify federation.

Pending: Kubernetes PVC support, capacity monitoring, retained-volume inventory and explicit destruction, cross-host recovery, catalog wiring and database-aware rollout protection. Container persistence alone does not qualify PostgreSQL end to end.

### PostgreSQL native recovery adapter, 18 October 2026

Implemented: PostgreSQL protocol readiness, native custom-format exports streamed through Trove, checked native command exit status, isolated restore networks without published ports or external egress, integrity verification before pg_restore, transactional restore and a SQL probe. Diagnostics cannot enter backup bytes. Temporary restore dumps are removed after each attempt. The adapter reconstructs a separate application role without superuser, database-creation or role-creation privileges and verifies password authentication over TCP.

Verified: a real PostgreSQL 17.11 database exported to the isolated MinIO endpoint and restored its row into a fresh instance. A corrupt object was rejected before restore; the live source refused restore; command failure was detected. The recovery network was separate and internal. These are adapter acceptance tests, not yet dashboard/API workflow acceptance.

Validation: focused server lint and tests passed. Dashboard lint passed on the shared checkout. Full checks were rerun while catalog contracts and concurrent federation work were changing; the complete suite is not yet qualified for this slice.

Pending: durable recovery records and retry reconciliation, scheduled backups, authorization, API/CLI/dashboard integration, production S3 acceptance and recovery objectives. Only database contents are exported; server roles and global settings are not part of this format.

### Standalone PostgreSQL API and durable backups, 18 October 2026

Implemented: explicit catalog plans for repository-free projects; PostgreSQL 17.11 digest pins for checked amd64/arm64 Docker targets; persistent private instances; separate restricted application credentials held in Vault; scoped, idempotent backup requests; durable attempt leases and reconciliation through Dispatch; backup read/manage permissions and audit events. `loomctl plan --catalog` selects this planning path. Recipe changes and target moves fail review rather than silently creating an empty replacement.

Verified: the real API created a project, saved its resource manifest, approved and deployed a plan, wrote SQL using the application role, uploaded a native backup through Trove, and recovered its backup record after a full control-plane restart. Repeating the same request returned the same backup. Another organization could neither read the recovery point nor request a backup of the source project. The application role has no superuser, role-creation or database-creation privileges.

Validation: Go lint, dashboard typecheck/lint and Go tests passed. The full dashboard suite first had one 5-second timeout in the concurrently updated deployment-target test; rerunning the suite passed all 232 tests. Native S3-compatible recovery remains covered by the earlier isolated MinIO test; this API test uses local Trove storage.

Pending: durable restore/drill workflows, scheduled policies, retention, service dashboard controls, Kubernetes, PITR and production S3 acceptance. This slice qualifies the Docker deployment and backup API path, not the complete phase 2 release gate.

### Isolated restore copies and backup schedules, 18 October 2026

Implemented: scoped and idempotent restore requests, durable single-use restore claims, separate temporary instances and volumes, SQL verification records, explicit copy cleanup, and a 24-hour cleanup deadline. Interrupted restore workers become visible failures; the same operation is never silently replayed over an existing copy. Cleanup checks the organization, project, environment, connection and recovery identity before removing storage. Copies remain isolated and never replace a live service binding.

Implemented: configurable full-backup schedules from hourly to weekly, persisted due times and idempotency keys, permission checks at execution, visible request failures, and enable/disable controls. `loomctl backups` and `loomctl restores` expose these workflows with reusable request IDs and explicit temporary-copy deletion. All recovery points are retained for now. No automatic retention or continuous recovery is implied by a schedule.

Verified: the API exported a seeded database, deleted the source row, restored the row into a fresh isolated copy, and confirmed that the live source stayed empty. Duplicate restore requests reused one record. Cleanup removed the copy's volume while retaining its verification time. Cross-organization reads and restore requests failed. A daily policy survived restart; the real persisted Dispatch cron created its due backup, advanced the next run and accepted disabling the policy.

Validation: full Go lint passed after concurrent federation edits settled. The full Go suite and all 232 dashboard tests passed. Targeted recovery, deployment, server, manifest and CLI tests passed. The optional browser fixture uses a disposable acceptance project and writes no credentials.

Pending: publication/cutover, application-specific integrity probes, quotas, retention, credential rotation, Kubernetes and PITR. SQL verification establishes that the native restore completed and accepted queries; it is not an application-level recovery guarantee.

### Recovery cleanup review, 18 October 2026

The final source review found two cleanup defects. Manual removal now uses the authorized administrator who requested it, so removal can finish after the original creator loses access. Automatic cleanup retains the creator's current permissions and records a visible failure when access is revoked. Failed cleanups persist a one-hour retry delay and sort by their next eligible time, so ten unreachable copies cannot permanently block later copies. Administrators can retry immediately.

Verified: the caller regression failed before the fix and passed afterward. A real PostgreSQL store test reproduced starvation with ten failed cleanups, then verified that the eleventh copy proceeds, retry timing survives reopening the store, manual retry works during backoff, and deleted copies are excluded. The live catalog API acceptance passed again after these fixes, including restore cleanup and scheduled backups. Recovery-package lint and the full Go suite passed; the final full-repo lint run reported formatting and a signed-to-unsigned conversion in concurrently edited `internal/runtimecatalog` files. Those files are outside this slice.

### PostgreSQL dashboard and client workflow, 18 October 2026

Implemented: a compact Services page with checked-target selection, one-click creation of a dedicated PostgreSQL project, strict plan-shape approval, manual backups, daily scheduling, isolated restore tests, verification timestamps, and copy cleanup confirmation. The page uses generated Forge clients and the shared ZeroState. Recovery polling remains active independently of the general realtime connection because recovery jobs do not yet publish realtime events.

Verified: a real browser deployed PostgreSQL, enabled daily backups, created a backup of a seeded SQL row, and started an isolated restore. The copy contained the row after it was deleted from the live source; the live source remained empty. The page showed SQL verification and the cleanup dialog; cancelling the dialog preserved the copy. API cleanup is covered by the earlier acceptance test. Desktop and 390 px layouts were inspected; recovery actions remain visible and the narrow document has no horizontal overflow.

Validation: generated-client drift checks, `tsc --noEmit`, `tsc -b`, and dashboard lint passed. All 235 dashboard tests passed with two workers, including catalog read failure/retry and denied recovery access. An earlier default-parallel run timed out in the existing deployment-target test.

Scope: this is the Docker single-instance logical-backup profile. The remaining phase 0-2 gates below are still open. The broader catalog, Kubernetes, PITR, retention, upgrades and binding cutover are not advertised as supported. The [PostgreSQL guide](postgres-services.md) documents the UI, CLI, API and Trove configuration.

### RDS removal retention, 18 October 2026

Implemented: RDS removal requests a final snapshot and retains automated backups. Snapshot names derive from AWS's physical database identity, so retries use the same name and a replacement database gets a different snapshot. An already missing or deleting instance is idempotent. Snapshot refusal blocks removal; no fallback deletes the database without recovery data. Failed instances that AWS cannot snapshot require a separate operator recovery decision.

Verified: the real AWS SDK against a controlled HTTP endpoint sends `SkipFinalSnapshot=false`, `DeleteAutomatedBackups=false` and a final snapshot identifier. The regression reproduced destructive requests before the fix, then covered accepted removal, repeated removal, missing instances and snapshot refusal. This verifies the request contract. Live AWS snapshot creation, retention expiry and fresh-account restore are still unverified. Go lint, AWS/deployment tests and the full Go suite passed. The shared dashboard checks encountered a concurrent test type error (`service-recovery.test.tsx` uses an unsupported `exact` option) and a 5-second timeout in `deploy-dialog.test.tsx`. This backend slice does not edit those files.

### Kubernetes persistence foundation, 18 October 2026

Implemented: shared mount validation and stable volume names across owned adapters; a private, single-writer Kubernetes StatefulSet path; independently owned filesystem PVCs; explicit retained storage-class selection and identity pins; namespace lifecycle and cluster volume-read permission checks; native TCP readiness; stop/start, removal and reattachment. Claims have no workload owner references and remain after failed deployment, rollback or removal. The adapter checks the bound PV's claim identity, capacity and reclaim policy.

Persistent role, mount, capacity and image changes require a reviewed migration, including after the workload has been removed. The current path rejects public exposure, autoscaling and multiple writers. It does not equate replica count with database HA. Deployment and StatefulSet pod construction share environment, secret, port and resource handling.

Verified: TDD regressions cover foreign claims and connection ownership, changed storage classes and bound-volume retention, unsafe layout/image changes, scale restrictions, failed-rollout rollback and retained reattachment. A real arm64 kind v1.37.0 cluster ran PostgreSQL 17.11 and retained an inserted row through pod replacement, rollout, removal and reattachment to the same PVC. The tested provisioner was `rancher.io/local-path`, with a separate retained class. The final-code repeat passed in 18.92 seconds, then its namespace, class, cluster, node volume, network and imported image tag were removed. This qualifies that storage combination only. The full Go suite, all 240 dashboard tests, dashboard typecheck/lint and `make build` passed. Lint passed with zero issues across the owned Kubernetes, Docker, shared cloud, deployment and planning packages. The full Go lint attempt reports concurrent agent/workload-identity issues outside this slice.

Pending: PostgreSQL catalog/API/CLI/dashboard wiring for Kubernetes; native backup and isolated restore on Kubernetes; production CSI and zone-loss qualification; expansion, capacity reporting and retained-data inventory/destruction. The broader phase gates remain open.

### Shared native PostgreSQL adapter, 18 October 2026

Implemented: one transport-independent PostgreSQL export, verified restore, role setup, authentication probe and temporary-file cleanup path. Docker delegates to this adapter. Runtime transports remain responsible for ownership and isolation before issuing commands. Failed native exports cannot publish a recovery object, and the shared restore path refuses live-source restoration.

Application-role setup uses SQL values encoded as hex and a mode-0600 temporary pgpass file. Credentials stay out of command arguments and diagnostic output. Temporary files are removed on success, failure and cancellation; they are placed outside persistent database mounts. Runtime-provided credentials remain in Vault. Invalid passwords fail before opening the target.

Verified: the new adapter tests first failed for the missing shared implementation, then passed for failed-export rejection, recovery-only restore, secret-free command arguments and temporary-file cleanup. A wrapper regression reproduced target access before invalid-password rejection and now passes. The real Docker PostgreSQL/Trove restore drill passed after the refactor, including native role authentication, corrupt-file rejection, retained data and source-restore denial. The full API deployment/backup/restart/restore/cleanup/schedule drill passed again in 62.93 seconds. The full Go suite, all 240 dashboard tests, dashboard lint, `make build` and shared-adapter lint passed. The full-repo Go lint attempt is blocked by concurrent storage-inventory formatting and lint edits; no finding in that run points to this shared adapter.

Pending: the Kubernetes command transport, enforced recovery isolation, catalog routing and Kubernetes recovery acceptance. These changes share database semantics; they do not qualify another target by themselves.

## 1. Proposed catalog

This is a coverage list, not a market-share ranking. PostgreSQL, MySQL, Redis, MariaDB, MongoDB, SQL Server, and Elasticsearch are priorities informed by the [Stack Overflow database survey](https://survey.stackoverflow.co/2026/technology/data/databases). Fabriq and Octopus are explicit product priorities. Other entries cover common infrastructure needs and require their own qualification before release.

| Category                  | First qualification batch                                                | Later qualification batches                                                                                              |
| ------------------------- | ------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------ |
| Relational databases      | PostgreSQL, MySQL, MariaDB                                               | SQL Server, CockroachDB; Oracle through an external connection first                                                     |
| PostgreSQL variants       | pgvector and TimescaleDB profiles                                        | Supabase as a separate composed stack                                                                                    |
| Document databases        | MongoDB, after the initial foundation pilot                              | CouchDB                                                                                                                  |
| Cache and key-value       | Redis, Valkey, Memcached                                                 | Dragonfly; compatibility tested separately                                                                               |
| Search and analytics      | Elasticsearch for Fabriq's search profile                                | OpenSearch, ClickHouse, Meilisearch, Typesense, InfluxDB                                                                 |
| Vector and graph          | pgvector, FalkorDB for Fabriq                                            | Qdrant, Weaviate, Neo4j                                                                                                  |
| Distributed wide-column   | None in the first batch                                                  | Cassandra, ScyllaDB                                                                                                      |
| Brokers and streaming     | NATS with JetStream, RabbitMQ                                            | Kafka in KRaft mode, Redpanda, Pulsar, EMQX, Mosquitto                                                                   |
| Object storage            | AWS S3 provisioning and existing S3-compatible connections through Trove | SeaweedFS, Garage, GCS, Azure Blob; R2, B2, and Spaces as qualified remote stores                                        |
| Enterprise object storage | Existing MinIO/AIStor connections                                        | AIStor deployment where distribution and license requirements are met; Ceph RGW on an existing qualified storage cluster |
| Gateways and proxies      | Octopus, existing Bastion gateway                                        | Traefik, Caddy, NGINX, HAProxy, Envoy, Kong, Apache APISIX                                                               |
| Xraph data platform       | Fabriq core, then graph/search/files profiles                            | Fabriq catalog/sharding and analytics profiles                                                                           |
| Embedded databases        | SQLite persistence attached to an application                            | DuckDB application/analytics profile                                                                                     |

SQLite and DuckDB need application file ownership and coordinated snapshots; they aren't generic remote database servers. Memcached is disposable by design, so its supported recovery procedure is cache rebuilding. Don't offer an imaginary backup switch.

MinIO's community repository is [archived and marked unmaintained](https://github.com/minio/minio). Preserve access to existing installations, but qualify maintained storage options before choosing a new default. [SeaweedFS](https://github.com/seaweedfs/seaweedfs) and [Garage](https://garagehq.deuxfleurs.fr/) are candidates, with S3 feature compatibility tested individually.

Managed products belong to a separate delivery mode. Extend existing RDS, Cloud SQL, ElastiCache, and S3 integrations first. Then evaluate adapters for Aurora, Memorystore, MongoDB Atlas, managed Kafka, and hosted PostgreSQL providers. Products such as DynamoDB, Firestore, BigQuery, and Snowflake begin as external bindings; a working connection must not be labelled a Loomplane-managed deployment.

## 2. What exists today

The baseline is Loomplane main at `62fc4cc`. These findings come from source inspection, not a new live acceptance run.

| Surface                 | Current implementation                                                                                                  | Required work                                                                                            |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| Provider catalog        | `internal/providers/catalog.go` describes server, Kubernetes, cloud runtime, and managed resource targets               | Add service recipes and per-recipe capabilities without conflating them with provider availability       |
| Manifest and planning   | `pkg/manifest/manifest.go` is strict version 1; plans pin approved revisions; source-backed services depend on analysis | Add a versioned catalog source and standalone service instances that don't need a Git repository         |
| Docker runtime          | `internal/clouds/server/runtime.go` rejects volume configuration                                                        | Stable volume identities, mount ownership, safe replacement, retention, host-loss restore                |
| Kubernetes runtime      | `internal/clouds/kubernetes/runtime.go` uses Deployments and rejects volume configuration                               | StatefulSet/PVC support or an explicitly supported operator, CSI capabilities, storage topology          |
| Managed database backup | RDS creation sets seven-day retention; Cloud SQL enables backup configuration                                           | Policy APIs, recovery visibility, restore jobs, verification, provider-specific PITR                     |
| RDS deletion            | `internal/clouds/aws/rds.go` skips the final snapshot and deletes automated backups                                     | Separate service removal from data destruction; retain by default                                        |
| File artifacts          | `internal/artifacts/artifacts.go` opens Trove directly; local and memory drivers are directly wired                     | Register the Trove extension, pin remote drivers, migrate existing artifacts without changing their keys |
| Control-plane backup    | `internal/ops` exports/restores Loomplane tables                                                                        | Keep this distinct from deployed-service backups; include file manifests and a recovery procedure        |
| Gateway                 | `internal/gateway` generates a fixed-route Bastion application                                                          | Add Octopus as an explicit engine with its own configuration, validation, and lifecycle                  |

Trove's root module is pinned at `v1.7.0`; its extension is a separate module and is not currently a direct dependency. The sibling Trove checkout contains named stores, Grove metadata, hooks, and multipart/version/lifecycle interfaces. Those findings must be checked against a published version before implementation pins them.

Fabriq's current Helm chart deploys a worker and migration job with external databases. Its example configuration requires PostgreSQL and Redis for the live/projection workflow, with optional FalkorDB and Elasticsearch. File storage, catalog databases, shards, and analytics need explicit profiles based on the actual runtime entrypoint. Do not assume that deploying the worker also supplies Fabriq's remote API.

## 3. One-click behavior

1. You connect infrastructure and choose environment defaults once: region, network, resource size, storage class, backup store, retention, and budget.
2. The catalog shows compatible recipes. Selecting one reveals a compact summary of dependencies, persistent capacity, public endpoints, backup policy, and expected recurring charges. Estimates carry a source and timestamp, or are shown as unavailable.
3. Deploy generates and approves an immutable plan, then starts its durable workflow. Approval uses the existing revision checks and permissions. A stale plan, additional dependency, or unapproved cost requires a new review.
4. Loomplane allocates storage, creates scoped credentials, starts dependencies, runs migrations, checks the native protocol, and publishes application bindings. Failed initialization remains visible and retryable.
5. The service page shows runtime health, disk capacity, latest recovery point, latest successful restore test, and actions together. Runtime health and backup health are separate states.

The normal flow needs no repository. A deployment can later be exported to a manifest or attached to a project's dependency graph without changing its resource identity. A double click or retried request must resolve to the same operation.

Use compact shared components and the shared ZeroState for empty, filtered, and missing-resource views. Keep failures and access denial distinct. Register any walkthrough before showing its start action, then verify it at desktop and narrow widths. Continue using Authsome and Forge-generated clients/state.

## 4. Recipe and lifecycle contracts

Create a versioned recipe registry alongside the provider catalog. A recipe describes the product; a target adapter describes where and how it can run. Keep recipe definitions declarative and validate them before they become available.

Each recipe needs:

- Stable ID, recipe version, product version, image digest per architecture, upstream source, supported upgrade paths, and license/distribution metadata.
- Supported delivery modes: self-hosted, provisioned managed service, or external connection. Availability is recorded separately for each target and topology.
- Typed configuration, native protocol ports, readiness checks, minimum resources, dependencies, initialization jobs, secret outputs, and connection binding names.
- Persistent mount roles, ownership and filesystem requirements, expansion rules, backup adapter, restore procedure, and retention policy.
- Explicit topology choices: standalone, replica set, quorum cluster, or managed HA. A replica count alone cannot select HA.
- Documented restart, upgrade, rollback, export, import, and deletion behavior, including irreversible data-format changes.
- Qualification evidence pinned to recipe, image, adapter, and target versions, with experimental, qualified, deprecated, and blocked states.

Suggested domain records are `ServiceInstance`, `ServiceRecipe`, `VolumeClaim`, `BackupPolicy`, `BackupRun`, `RecoveryPoint`, `RestoreRun`, and `ConnectionBinding`. These are proposed names, not existing APIs. Store durable metadata in Grove and execute long operations through Dispatch. Preserve Ctrl Plane ownership of instance runtimes and Loomplane ownership of managed resources.

Every record carries organization, project/environment scope, resource identity, and revision. Build migrations and policy checks before exposing APIs. Backup and restore permissions are separate from viewing a service; file prefixes alone do not enforce tenant isolation.

Extend the existing plan graph with storage, service dependencies, initialization, protection, and binding actions. Fingerprint recipe/image versions, volume identity, backup policy, and secret references. Use a per-resource operation lease so restore, resize, upgrade, and deletion cannot mutate the same data concurrently.

Extend the manifest through a deliberate schema-version change with backward-compatible reading of version 1. Model catalog sources, service storage, protection, lifecycle, gateway engine, and dependency outputs. Publish examples only after the parser accepts them; no speculative YAML should appear to be executable today.

## 5. Persistence across targets

| Target                                 | Live-data storage                                                       | Required lifecycle behavior                                                                                                                                |
| -------------------------------------- | ----------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| SSH/Docker server                      | Named volumes owned by service ID, or explicitly enrolled block devices | Keep across replacement and removal; validate UID/GID, capacity, filesystem, ownership labels; fence the old writer before reattachment                    |
| Kubernetes                             | StatefulSet/PVC or qualified engine operator with CSI block storage     | Validate storage class, access mode, expansion, zone affinity and reclaim policy; retain claims; use per-member volumes and quorum-aware disruption limits |
| Managed service                        | Provider storage and native recovery APIs                               | Discover actual capabilities, keep provider resource identity, reconcile out-of-band changes, and verify backups independently                             |
| Existing external service              | Existing owner's persistence                                            | Connect and inspect within granted rights; backup/restore only through an explicit supported adapter                                                       |
| Cloud Run, Vercel, current ECS adapter | External or managed stateful dependencies                               | Do not offer self-hosted stateful recipes until the target's required disk and recovery semantics are implemented and qualified                            |

Storage identities outlive deployment revisions. A failed rollout cleans up its new disposable resources, never the last usable volume. Removal retains data by default; destruction is a separate scoped operation with the exact volumes and recovery points listed. Managed database deletion must enforce this too.

Docker local volumes survive container recreation but not loss of the host. Show that limitation and require off-host backups for a production profile. Kubernetes StatefulSets provide stable identities and volume association, but storage replication and application consistency require separate support. The [StatefulSet documentation](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/) describes the retention controls that the adapter must set and verify.

Never mount a generic S3 bucket as a database data directory. Use the filesystem/block interface required by the engine; S3 is a backup or application-object destination. Snapshot support must identify whether it is crash-consistent or application-consistent, and multi-volume snapshots need a coordinated procedure.

Support disk growth only when the target and engine allow it. Reject automatic shrink, alert on free-space and growth thresholds, and estimate snapshot/backup capacity separately. Restoring onto another host or zone creates a replacement volume and retains the original until validation and cutover finish.

## 6. Trove owns files and backup objects

Register `github.com/xraph/trove/extension` through Forge, with explicit published pins for the extension and chosen drivers. Resolve its named stores through DI and adapt `internal/artifacts` to that instance. Use separate stores or buckets for deployment artifacts, service files, and backups so their retention and permissions can differ.

The first remote backend is AWS S3. Qualify other S3-compatible endpoints individually for multipart upload, version reads, checksums, encryption, retention, and restore. Existing GCS, Azure, local, and SFTP drivers are useful foundations, not proof of end-to-end support in Loomplane.

There are two distinct responsibilities:

- Trove transports and manages backup bytes, manifests, file versions, and downloadable exports.
- Infrastructure adapters provision buckets, IAM, storage encryption policies, replication, and provider recovery resources. Provider-native snapshots may have no downloadable object; track their provider IDs honestly.

The current Trove S3 `Put` reads the whole object into memory, and multipart upload reads each supplied part into memory. Build a bounded multipart path through Trove, with limited part size/concurrency, backpressure, retry checkpoints, cancellation, and abandoned-upload cleanup. Avoid dashboard upload handlers and whole-object compression/encryption middleware for large backups. Qualify memory usage with an object much larger than the runner's memory limit.

If upstream needs a streaming encryption, retention, or Object Lock capability, implement it in Trove and consume its published release. Don't create a second file-storage SDK inside Loomplane. Until that support exists, use a preconfigured compliant bucket and verify the required settings through the provider adapter; do not claim unsupported guarantees.

Use Vault-backed credential references, workload identity where available, and scoped runtime access. Resolve credentials only in the worker that needs them. Redact DSNs, tokens, signed URLs, and backup contents from plans and logs. Retain the key IDs needed to decrypt old backups through key rotation.

Use unique immutable object keys with organization/environment/service/run IDs. A completed recovery point references exact object versions, checksums, engine versions, configuration versions, and key references. Publish its completion manifest only after all required objects are committed and verified. A multipart ETag is not a universal content checksum.

S3 Object Lock protects object versions and requires versioning. Retention must be checked on the actual destination; it is not implied by an S3-compatible URL. Separate backup-writer credentials from retention/deletion authority. See [AWS Object Lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html).

## 7. Backup and recovery adapters

Use the engine's supported consistency procedure before handing files to Trove. The table is the intended adapter contract; each row still needs version-specific implementation and restore qualification.

| Service family                              | Backup procedure to qualify                                                                      | Recovery constraints                                                                                               |
| ------------------------------------------- | ------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------ |
| PostgreSQL, TimescaleDB, pgvector           | Small-database logical exports; physical base backups plus continuous WAL for PITR               | Logical dumps are not a WAL-replay base; preserve roles, extension versions, configuration, and timeline history   |
| MySQL and MariaDB                           | Engine/version-compatible physical backup plus binary logs; logical exports for portable copies  | Separate engine tools and upgrade paths; preserve log positions and required users/configuration                   |
| MongoDB                                     | Consistent replica-set backup with supported oplog handling; coordinated sharded procedure later | A plain live directory copy or uncoordinated shard dumps are insufficient                                          |
| Redis and Valkey                            | Native RDB snapshot; AOF where the durable profile needs it                                      | Treat crash persistence, replication, and historical backups separately; qualify each engine/version               |
| Memcached                                   | Configuration plus a documented cache rebuild path                                               | No durable-data or historical-restore claim                                                                        |
| NATS JetStream                              | Native stream/account snapshot workflow, consumer state and identity/configuration inventory     | Verify messages, sequence/ack state and credentials after restore; replication is not backup                       |
| RabbitMQ                                    | Definitions plus a supported message-data backup/recovery procedure                              | Definitions alone omit messages; queue type, node identity and shutdown requirements constrain restoration         |
| Kafka, Redpanda, Pulsar                     | Product-specific metadata/configuration recovery, durable logs, and qualified DR/export strategy | Tiered storage or replication alone is not a complete backup; offsets, schemas, ACLs and topology need coverage    |
| Search, vector, graph and analytics engines | Native snapshot/export with matching metadata and configuration                                  | Restore compatibility differs by engine; rebuild derived indexes only when a verified source and replay path exist |
| Object storage                              | Version-aware copy or replication to an independent destination, inventory and integrity checks  | Protect credentials, metadata, delete markers and object versions; replication can propagate unwanted deletion     |
| SQLite and DuckDB                           | Engine-supported consistent backup/checkpoint under coordinated application access               | Copying an open database file alone is not a supported recipe                                                      |
| Gateways                                    | Versioned configuration, plugin/image pins, certificate/key references, discovery settings       | Recover certificates safely; re-establish live discovery and test actual routes                                    |
| SQL Server and other enterprise engines     | Vendor-supported full/log backup and restore tooling                                             | License, platform, version, key recovery and native format support gate availability                               |

PostgreSQL recovery requires a usable base backup and an uninterrupted WAL chain. The UI should expose the recoverable time range and missing segments, following [PostgreSQL's recovery model](https://www.postgresql.org/docs/current/continuous-archiving.html). Redis's RDB and AOF serve different persistence needs, as described in [Redis persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/). RabbitMQ's [backup guidance](https://www.rabbitmq.com/docs/backup) requires queue-aware handling; use [JetStream's persistence model](https://docs.nats.io/concepts/jetstream) when qualifying NATS.

Proposed default policies, adjustable per environment:

| Profile     | Starting policy                                                                                                           | What you can expect                                                                                    |
| ----------- | ------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| Development | Persistent disk; manual backup, optional daily backup                                                                     | Restart survival; recovery only as recent as the last verified copy                                    |
| Standard    | Daily application-consistent backup; seven daily and four weekly points; weekly restore drill                             | Nominal daily recovery cadence, with actual age and failures shown                                     |
| Critical    | Engine-native continuous log archival where supported; daily base backup; independent immutable copy; daily restore drill | Target RPO of five minutes where measurable; RTO measured by drills, never promised from configuration |

RPO is the amount of recent data you can lose. RTO is the time needed to recover. Display both as targets alongside observed results. Engines without qualified continuous recovery must say so and offer a supported snapshot cadence.

Retention must preserve complete restore chains, including the oldest base and logs needed by retained points. Don't let generic bucket lifecycle expiry break a database recovery chain. A failed or corrupt new backup never causes deletion of the last usable point.

## 8. Useful recovery features

The default restore creates a new, isolated service instance. Run native integrity and application probes, then offer an explicit binding cutover. Fence the previous writer and define how writes after cutover affect rollback. Do not silently overwrite a live database.

For whole-stack recovery, save a recovery-set manifest linking database points, object versions, queue policy, secret key references, and recipe versions. For coordinated recovery, pause writes and consumers or use a verified application watermark. Backups taken near each other in time do not prove consistency.

For development clones, restore a recovery point into a private temporary environment, apply mandatory masking where the data requires it, suppress outgoing jobs/webhooks, and attach a TTL. Native volume clones can reduce time and cost where available.

A scheduled recovery drill uses a Dispatch job that restores into a temporary network, verifies data and application behavior, records the measured duration, and cleans up its own resources. Cap drill concurrency, runtime and spend, and report cleanup failures. Show "backup uploaded" and "restore verified" separately.

Offer a separate account or provider for critical backups. A self-hosted object store cannot be the only destination for its own backup. Recover the Loomplane database, Trove metadata, object manifests and key references from an external recovery bundle with a documented bootstrap order.

Logical exports provide a migration path across targets where engine semantics permit it. Physical snapshots stay tied to their documented engine, architecture, and provider constraints. Use deduplication only within an authorized storage scope, with reference-aware garbage collection; never introduce cross-tenant content disclosure.

## 9. Fabriq and Octopus recipes

### Fabriq

Offer named profiles whose dependencies are visible before deployment:

- Core: PostgreSQL, required Redis workflow, versioned migration job, and the selected Fabriq worker/API entrypoints.
- Graph: core plus FalkorDB and verified graph projection/rebuild behavior.
- Search: core plus Elasticsearch and verified search projection/rebuild behavior. OpenSearch requires explicit Fabriq compatibility testing.
- Files: core plus a Trove-backed object store and the actual Fabriq blob/archive configuration. Cover object metadata, CAS references and archived document history.
- Catalog/analytics: separately qualified catalog control database, tenant shards, routing maps, analytics database and recovery ordering. Do not infer this support from the basic Helm chart.

For recovery, stop writers, relays and projection workers; capture an authoritative database/object recovery set; restore metadata and referenced files; validate referential integrity; rebuild eligible projections; then resume workers from a known checkpoint. Preserve tenant routing and identity throughout. Replaying an event must not repeat an external side effect.

Verify the chosen release's binary, image contents and migration commands. A worker becoming healthy does not establish remote API availability or working live subscriptions.

### Octopus

Add Octopus as a gateway engine alongside Bastion. Generate validated static routes from Loomplane service bindings first, then enable the FARP discovery profile with scoped backend access. Use Octopus's actual CLI/configuration and digest-pinned images.

Version route configuration in Trove. Keep private keys in the existing secret system; persist or recover certificate state where the selected certificate workflow requires it. Pin plugins and schemas, validate before activation, retain the last working configuration, and expose route/TLS/discovery health separately.

Acceptance covers HTTP, WebSocket and each advertised additional protocol, authenticated routes, upstream failure, certificate reload, configuration rollback, and service registration/removal. Preserve existing Bastion manifests and bindings during migration.

## 10. Delivery sequence and gates

| Phase                                | Implementation scope                                                                                                                               | Gate before moving on                                                                                                        |
| ------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| 0. Contracts and retention           | Recipe schema, catalog-origin instances, capability matrix, manifest evolution, authorization model, retained-data lifecycle, RDS deletion changes | Reviewed contracts; planning/idempotency/denial tests; retained data survives removal                                        |
| 1. Trove and storage                 | Trove extension/driver pins, artifact migration, named stores, bounded multipart, Docker volumes, Kubernetes PVC/StatefulSet path                  | Real S3 round trip and restart; interrupted upload recovery; large-file memory test; data survives container/pod replacement |
| 2. Complete PostgreSQL pilot         | Catalog UI/API/CLI, native probes, bindings, backup schedules, PITR profile, restore clone and cutover                                             | Seed/write/restart/backup/delete-row/restore/drill on Docker and Kubernetes; managed-provider path separately qualified      |
| 3. First catalog batch               | MySQL/MariaDB, Redis/Valkey/Memcached, NATS, RabbitMQ; S3 service binding; Octopus and Fabriq core                                                 | A per-recipe lifecycle and restore report, including native client behavior and dependent applications                       |
| 4. Composed and specialist services  | MongoDB, Fabriq graph/search/files, SQL variants, search/vector/analytics, self-hosted storage and other gateways                                  | Dependency-aware recovery; file/database consistency; protocol and version compatibility tests                               |
| 5. Distributed and managed expansion | Kafka/Redpanda/Pulsar, distributed databases, Fabriq catalog/shards, enterprise and managed adapters                                               | Quorum/fencing/failover, multi-zone loss, managed recovery, rolling upgrade, and scoped tenant recovery                      |
| 6. Recovery operations               | Independent immutable copies, stack recovery, masking/clones, restore drills, retention/cost reporting, deprecation and upgrade maintenance        | Disaster exercise including a fresh Loomplane control plane; published per-product support matrix                            |

Each phase ships coherent reviewed slices. PostgreSQL is the first complete implementation because it exercises most of the persistence and recovery contract and supplies Fabriq's authoritative database. Stop for design decisions that change data ownership or recovery guarantees; ordinary implementation choices follow the approved contracts.

## 11. Verification and release definition

A qualification record covers the exact recipe/image, adapter, platform, storage backend, and topology. Passing on Docker does not qualify Kubernetes; a simulated S3 endpoint does not qualify AWS S3 or every compatible provider.

Required scenarios include deploy and reconnect; native client writes; process and control-plane restart; redeploy without data loss; credential rotation; backup during writes; interrupted transfer; restore onto a fresh target; corruption/missing-key failure; retention; attempted cross-tenant reads; disk full; provider outage and retry; failed migration; supported upgrade and rollback boundaries; removal with retained data; and explicit destruction.

For brokers, verify delivery/acknowledgement semantics after recovery. For Fabriq, verify actual API, subscription and projection behavior. For gateways, verify real routed traffic. For HA profiles, inject member and zone failure and confirm a single valid writer/quorum.

UI acceptance runs at desktop and narrow widths and covers denied, empty, loading, failed and retry states. Generated clients must pass drift checks, `tsc --noEmit`, and the project's `tsc -b` path after contract changes. Code slices use `make lint` (the repo has no `make l` alias), documented formatting commands, relevant tests and builds before focused commits.

Every catalog entry keeps this checklist with evidence links:

| Capability                              | Initial status |
| --------------------------------------- | -------------- |
| Recipe and dependency model             | Planned        |
| Provisioning and native connectivity    | Unverified     |
| Persistent storage and restart/redeploy | Unverified     |
| Backup and retention                    | Unverified     |
| Fresh-target restore and data integrity | Unverified     |
| Upgrade and rollback procedure          | Unverified     |
| Authorization, failure and retry paths  | Unverified     |
| Desktop and narrow UI                   | Unverified     |
| Live target/topology qualification      | Unverified     |

## 12. Source pointers and decisions to carry forward

Local source reviewed:

- Loomplane: `pkg/manifest/manifest.go`, `pkg/provider/provider.go`, `internal/providers/catalog.go`, `internal/planning/plan.go`, `internal/clouds/server/runtime.go`, `internal/clouds/kubernetes/runtime.go`, `internal/clouds/aws/rds.go`, `internal/clouds/gcp/cloudsql.go`, `internal/artifacts/artifacts.go`, `extensions/planning/extension.go`, `internal/ops`, and `internal/gateway`.
- Trove sibling checkout: `extension/config.go`, `extension/extension.go`, `extension/go.mod`, `driver/capability.go`, `drivers/s3driver/s3.go`, and `drivers/s3driver/multipart.go`.
- Fabriq sibling checkout: `config.example.yaml`, `config.go`, `deploy/helm/fabriq/values.yaml`, and its worker/migration chart layout.
- Octopus sibling checkout: `README.md`, its configuration examples and Cargo workspace layout. Executable behavior still needs qualification against the selected release.

Defaults proposed for implementation: retain data on removal; private database/broker endpoints; AWS S3 as the first off-host backup destination; Trove extension for all Loomplane-managed files; Docker and Kubernetes as the first self-hosted targets; PostgreSQL as the pilot; Octopus as the first new gateway; Fabriq as the first composed Xraph recipe. Exact image and dependency versions are selected and pinned during qualification, not guessed in this plan.
