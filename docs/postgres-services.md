# Database services

Open **Services** to deploy PostgreSQL 17 on a checked Docker server or Kubernetes cluster. Choose a project name and target, leave daily backups enabled if you want them, then select **Deploy PostgreSQL**. Loomplane creates a repository-free project, checks its plan, and approves only the single database creation you requested. Unexpected actions or warnings require normal plan review.

You can also choose **MySQL 8.4** or **MariaDB 11.8** in the database engine selector. Each engine uses its own pinned image and native backup client. You can also deploy **Redis 8.2** or **Valkey 8.1** on a checked Docker server. All five engines share the recovery controls and CLI commands.

The PostgreSQL profiles run one private instance with a digest-pinned image, retained storage and a separate application role. That role cannot create databases or roles and is not a superuser. Database credentials stay in Vault. Applications deployed in the same environment on the same target receive the resource URL through the existing resource binding workflow.

## Persistence and backup storage

A container replacement retains its named volume. Removing the container also retains the volume. Docker local storage shares host capacity: this profile does not reserve a disk quota or survive host loss on its own. You can inspect retained data under **Infrastructure**, **Persistent storage**, or with `loomctl storage CONNECTION_ID`. See the [inventory guide](storage-inventory.md). Deliberate destruction remains a separate operation; the inventory is read-only.

Kubernetes uses a StatefulSet and a retained PVC. Its PostgreSQL recipe requests 10 GiB, with the checked namespace, storage-class identity and architecture recorded in the plan. The connection must report PostgreSQL recovery permissions ready. Each restore separately proves that the cluster's network policies enforce isolation before reading the backup. A storage request does not establish replication or recovery after losing a host or zone.

Loomplane uses the Trove extension for artifacts and backup files. Set `LOOMPLANE_BACKUP_URL` and `LOOMPLANE_BACKUP_BUCKET` to choose a separate backup destination. Without a backup URL, backups use the artifact storage backend in a separate bucket. The development default is local storage.

For an S3-compatible service, the URL takes this form:

```text
s3://ACCESS_KEY:SECRET_KEY@REGION/BUCKET?endpoint=https://storage.example.com&path_style=true
```

Supply the URL through the deployment's secret configuration, not a committed file. URL-encode credential characters. Set `LOOMPLANE_BACKUP_BUCKET` to the same pre-created bucket. Remote buckets must already exist and allow the configured identity to list, read, write, and complete or abort multipart uploads. Local buckets are created at startup. Test the exact provider and endpoint before relying on it for recovery.

The current transfer path uses 8 MiB chunks, bounded retries and a SHA-256 digest. It caps a backup at about 78 GiB. A process crash can leave incomplete multipart uploads; configure provider-side incomplete-upload cleanup until durable multipart reconciliation is available. Do not configure expiry of completed recovery objects behind Loomplane's records.

New recovery points also record a destination identity derived from the configured driver, local directory or S3 region/endpoint, and explicit bucket. Credentials are excluded, so rotating a backup access key does not change that identity. Changing the directory, endpoint or bucket does not migrate existing points: reconnect their original destination to read them. S3 endpoints must be absolute HTTP or HTTPS URLs without query strings or fragments.

The pin describes the configured namespace, not a physical disk or cloud account. Legacy points without a pin remain readable with checksum verification, but cannot be retired through the deletion helper. Automatic retention is still unavailable.

## Back up and test recovery

In **Service recovery**, select your project, environment and database. **Back up now** queues an export using that engine's native backup client. Set **Backup interval (hours)** from one to 168 and save your schedule. **Enable backups** and **Pause backup schedule** preserve that interval. All recovery points are retained; automatic retention is not implemented.

The lists show recovery points and copies for the selected database and environment. If you remove the source service, its backup history and restore controls remain available. Backup creation and scheduling need a live source.

A successful upload initially shows **Not tested**. **Test restore** creates an isolated copy with its own volume and credentials, verifies the file before restoring, runs the matching restore client, and checks application access. PostgreSQL imports run in one transaction; MySQL and MariaDB imports can leave a partial copy on failure, as described below. A successful copy records its verification time and duration. The live source and application bindings stay in place.

Copies have no published ports. Docker copies use an internal network; Kubernetes copies use independently enforced deny-all network policies. They expire after 24 hours; cleanup failures remain visible and retry automatically after an hour. An authorized administrator can retry sooner with **Remove copy**. Scheduled cleanup uses the creator's current permissions; if that access has been revoked, another administrator must finish removal. Removing a copy deletes its temporary volume while preserving the backup and verification record. A verified restore does not establish that your application can recover correctly.

If a restore worker disappears, its operation becomes a failure after the lease expires. Remove the temporary copy and request a new restore. Each restore runs once; an interrupted command is not replayed over a possibly changed database.

## Use loomctl

You can prepare a PostgreSQL service without connecting a repository:

```sh
loomctl services create "Application database" --target conn_your_checked_server --json
loomctl approve PLAN_ID --yes
```

For a checked Kubernetes connection, select its provider explicitly:

```sh
loomctl services create "Cluster database" --provider kubernetes --target conn_your_checked_cluster --json
```

The Kubernetes catalog currently accepts PostgreSQL only. Review and approve the returned plan with the same `approve` command.

Select MySQL or MariaDB when you create the service project:

```sh
loomctl services create "Orders MySQL" --kind mysql --target conn_your_checked_server --json
loomctl services create "Orders MariaDB" --kind mariadb --target conn_your_checked_server --json
```

Each command returns a plan for you to review and approve. MySQL uses version `8.4`, and MariaDB uses `11.8`. These choices also work as a resource manifest's `kind` and `version`, with `provider: server`. Changing the engine, image or target of an existing database requires a tested migration into a fresh service.

Service creation saves the resource manifest and returns a plan for review. Approval starts deployment. You can also save a resource-only manifest yourself:

```yaml
version: 1
resources:
  database:
    kind: postgres
    provider: server
    version: "17"
    target: conn_your_checked_server
```

Create a project, save the manifest and approve its plan:

```sh
loomctl projects create "Application database"
loomctl manifest save PROJECT_ID -f loomplane.yaml
loomctl plan PROJECT_ID --catalog --env production
loomctl approve PLAN_ID
```

Manage recovery from the same project:

```sh
loomctl backups create PROJECT_ID --resource database --request-id nightly-manual-1 --wait --json
loomctl backups PROJECT_ID
loomctl backups show BACKUP_ID --wait --json
loomctl backups policy PROJECT_ID --resource database --every-hours 24
loomctl backups policies PROJECT_ID --json
loomctl restores create BACKUP_ID --request-id recovery-test-1 --wait --json
loomctl restores show RESTORE_ID --wait --json
loomctl restores delete RESTORE_ID --yes
```

Reuse a request ID after a lost response to recover the original operation. Use a new ID for a new backup or restore. Failed backups can be retried with `loomctl backups retry BACKUP_ID`. Add `--disable` to the policy command to stop its schedule.

With `--wait`, the CLI follows the persisted operation for up to 45 minutes. Set `--timeout 10m` to change that limit. Timeouts must be positive; invalid values are rejected before any API call. A timeout stops waiting and leaves the server job running. With `--json`, the CLI preserves the operation ID and its earlier response, then exits nonzero. Follow that ID with `show --wait` to read current status. Failed operations also return a nonzero exit status and retain their JSON result for inspection. `--json` outputs a single JSON value without a request-ID prefix; request errors include the reusable key.

## Preview backup retention

You can inspect which independent full backups meet an age and minimum-count rule:

```sh
loomctl backups retention BACKUP_ID --older-than-days 30 --keep-last 7 --json
```

Use any recovery point from the database history you want to inspect. The preview keeps at least the newest requested count and the newest restore-tested point. It also protects backups linked to copies that have not been removed. Without a tested point, none qualify for retirement. Age is measured from completion, and a backup exactly on the age boundary stays.

This command only reads metadata. It does not delete files, save a policy or change your backup schedule. Incomplete inventories select nothing; the current preview covers up to 1,000 points from one physical database source. The result reports each decision and the bytes that meet the rule. Actual deletion still needs a qualified storage capability, current permission checks and protection against concurrent restores.

## API and permissions

| Operation              | Route                                                                              | Permission                                      |
| ---------------------- | ---------------------------------------------------------------------------------- | ----------------------------------------------- |
| List/create backups    | `GET/POST /api/v1/projects/:id/service-backups`                                    | `service_backup:read` / `service_backup:manage` |
| Read/retry a backup    | `GET /api/v1/service-backups/:id`, `POST .../:id/retry`                            | Read / manage                                   |
| Preview retention      | `GET /api/v1/service-backups/:id/retention-preview?older_than_days=30&keep_last=7` | `service_backup:read`                           |
| Set/list schedules     | `PUT/GET /api/v1/projects/:id/backup-policies`                                     | Manage / read                                   |
| Create a recovery copy | `POST /api/v1/service-backups/:id/restore`                                         | `service_restore:manage`                        |
| List/read copies       | `GET /api/v1/projects/:id/service-restores`, `GET /api/v1/service-restores/:id`    | `service_backup:read`                           |
| Remove a copy          | `DELETE /api/v1/service-restores/:id`                                              | `service_restore:manage`                        |

Backup creation accepts `environment`, `resource` and `request_id`. Restore creation accepts `request_id`. Policy updates accept `environment`, `resource`, `every_hours` and `enabled`. Requests and worker execution are scoped to the recorded organization and project. Scheduled requests recheck the policy creator's permission.

## Qualification boundary

PostgreSQL 17.11 has passed the Docker catalog and recovery drill, with local Trove and a separate native adapter drill against an isolated MinIO endpoint. The Kubernetes catalog, CLI, API and browser drill passed on arm64 Kubernetes 1.36.4, Cilium 1.20.3 and a retained local-path class. That drill covered pod replacement, backup persistence after a control-plane restart, isolated SQL recovery, scheduled backups and deletion of the exact temporary PV.

AWS S3 acceptance, production CSI and cross-node qualification, high availability, point-in-time recovery, deliberate data destruction, automatic retention, upgrades, credential rotation and live binding cutover remain pending. A digest pin is not a claim that every architecture has passed live acceptance; the published recipe contains both amd64 and arm64 pins, while the local acceptance host exercised arm64.

## MySQL and MariaDB recovery

The Docker recipes pin MySQL 8.4.11 and MariaDB 11.8.9. Application credentials grant access to the selected database, without global user-management privileges. Backups use the matching `mysqldump` or `mariadb-dump` client and include database tables, views, routines, triggers and events. Server users and global settings are outside the backup.

Logical snapshots require InnoDB tables. Backup creation refuses other table engines; you must also avoid schema changes while a dump runs because a consistent transaction does not protect concurrent DDL. The profiles disable binary logging and do not provide replication or point-in-time recovery. See the native [MySQL dump guidance](https://dev.mysql.com/doc/refman/8.4/en/mysqldump.html) and [MariaDB dump guidance](https://mariadb.com/docs/server/clients-and-utilities/backup-restore-and-import-clients/mariadb-dump).

A test restore uses a fresh private container, volume and credentials. It verifies the backup bytes before SQL import and imports as the database-scoped application user. Scheduled events remain disabled in the recovery copy, including after restart. MySQL and MariaDB imports can leave a partial copy after an SQL error; the operation records a failure, and you can remove the copy before starting another test. The live source and its application bindings stay in place.

## Redis and Valkey recovery

Choose Redis 8.2 or Valkey 8.1 in Services, or prepare a service with the CLI:

```sh
loomctl services create "Application Redis" --kind redis --target conn_your_checked_server --json
loomctl services create "Application Valkey" --kind valkey --target conn_your_checked_server --json
```

Review and approve the returned plan. The Docker recipes pin Redis 8.2.10 and Valkey 8.1.10 by architecture. Both expose a private `redis://` application binding on port 6379, with database zero and separate administrative and application credentials in Vault. The application role cannot change server configuration, manage users or run dangerous administrative commands.

You get one instance with retained `/data`, AOF persistence and an fsync interval of one second. A sudden host failure can lose recent writes; this is not a zero-loss profile. The dataset limit is 128 MiB with eviction disabled, inside a 512 MiB container. Once it reaches the limit, writes that need more memory fail. Plan capacity before using it as a cache or primary store.

Backups use each engine's native RDB snapshot format. A restore verifies the saved bytes and the native RDB structure before loading an empty isolated copy. The copy uses new credentials and a read-only application role. AOF and automatic snapshots are disabled in that copy so restarting it keeps the checked RDB. Strings, hashes, lists, streams and expiring keys are covered by the native recovery drill.

Use the same **Back up now**, **Test restore** and schedule controls, or the backup and restore commands above. A successful test shows **Restore verified**. It checks database access; you still need to test your application's recovery.

Redis and Valkey backups stay within their recorded engine and recipe. Cross-engine restores, upgrades, replication, failover, point-in-time recovery, credential rotation and automatic application cutover are outside these profiles. Kubernetes currently accepts PostgreSQL only. The native Redis and Valkey drills run on arm64; the amd64 manifests are pinned but have not passed live acceptance here.
