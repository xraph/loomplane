# Database services

Open **Services** to deploy PostgreSQL 17 on a checked Docker server or Kubernetes cluster. Choose a project name and target, leave daily backups enabled if you want them, then select **Deploy PostgreSQL**. Loomplane creates a repository-free project, checks its plan, and approves only the single database creation you requested. Unexpected actions or warnings require normal plan review.

You can also choose **MySQL 8.4** or **MariaDB 11.8** in the database engine selector. Each engine uses its own pinned image and native backup client. You can also deploy **Redis 8.2** or **Valkey 8.1** on a checked Docker server. All five engines share the recovery controls and CLI commands.

The PostgreSQL profiles run one private instance with a digest-pinned image, retained storage and a separate application role. That role cannot create databases or roles and is not a superuser. Database credentials stay in Vault. Applications deployed in the same environment on the same target receive the resource URL through the existing resource binding workflow.

## Managed cloud services

In **Services**, you can also deploy PostgreSQL 17 through a checked AWS RDS or Google Cloud SQL account. Choose the account and region, then review the requested compute, disk, access and backup settings. RDS uses the region checked on its connection. Cloud SQL offers the provider's listed regions. Loomplane approves only one creation whose account identity, region and complete profile match that review.

The form keeps a link to your project if creation fails or takes longer than its wait limit. Once a project exists, use that project to inspect the result. **New service** starts a separate creation and requires a new name. Managed operations can take up to 45 minutes before the form stops waiting; this limit does not cancel a cloud operation.

Managed creation has been checked through the actual API and dashboard against controlled provider endpoints, at desktop and narrow widths. Live cloud creation and recovery remain unqualified. You can also use project manifests or prepare standalone plans with `loomctl`, as shown below. The export and file-restore controls below apply to the Docker and Kubernetes profiles. Managed services keep their provider-native backups separate from Trove exports.

| Connected provider | Existing resource adapters                                       |
| ------------------ | ---------------------------------------------------------------- |
| AWS                | RDS PostgreSQL/MySQL, ElastiCache Redis OSS and S3               |
| Google Cloud       | Cloud SQL PostgreSQL/MySQL, Memorystore Redis and Cloud Storage  |
| DigitalOcean       | Managed PostgreSQL/MySQL/Valkey through the App Platform adapter |
| Render             | Postgres and Key Value                                           |

Provider availability does not establish backup or restore support. RDS removal retains automated backups and waits until the original database is gone and its final snapshot is available. The snapshot must match the saved physical source, account and region. Missing, failed or mismatched snapshots keep the binding and credentials available for inspection and retry. Cloud SQL removal requests a final backup with a 30-day lifetime and waits for the operation to finish. If Google refuses the request or reports an operation failure, Loomplane keeps the resource credentials and reports the failure. An instance that is already missing does not prove its final backup completed. Loomplane keeps the credentials unless its captured deletion operation succeeds. There is no retry without the final backup. Retained cloud backups can incur storage charges.

RDS saves a creation intent before sending the request, then captures the provider's immutable physical database ID before waiting. Explicit Retry and app restart keep that checkpoint. If the create response is lost, Loomplane can follow the original database only when its creation tag matches the saved intent. A reused name with a different physical ID blocks the operation. A missing original database does not authorize another create.

RDS retries require the original password in Vault. They cannot reset it implicitly or publish a new password. Protected removal also keeps its intent before requesting deletion, so retry can inspect the original final snapshot without sending another delete. Older bindings without a physical ID pin the currently observed database during explicit execution; they cannot prove ownership of an earlier incarnation. These paths have been checked through the actual API, deployment store and Vault against controlled AWS endpoints, including restart and lost responses. Live AWS creation, SQL persistence and snapshot restore remain unqualified.

Cloud SQL deployments save the account, region, resource settings and each native operation before moving to the next setup step. A retry follows the captured operation for instance creation, database creation, user setup or protected removal. Checkpoint failures stop further work.

If a new instance's create response loses its operation ID, Retry can read Google's operation history using the saved request time and original service account. The scan must return every page within its limit of ten pages of 100 operations, with one possible CREATE after that request. The operation must have succeeded, its native time interval must contain the instance's creation time, and the instance must match the saved intent label, placement and engine. Multiple possible creates, missing metadata, provider warnings or failed reads block setup. Loomplane rereads the selected operation and instance, then saves the recovered operation and creation pin before database or user setup. It never sends the create request again.

This discovery uses the native history Google returns. It does not establish how long Google retains that history, or prove ownership after an external recreation whose earlier operation has disappeared. Reconcile externally recreated instances before Retry. A lost database, user or removal operation ID still requires reconciliation because those operation records do not identify the complete original mutation payload. Version 2 checkpoints can follow a captured operation, but cannot discover an unknown one without the original request context.

Cloud SQL saves a creation intent label with each new instance. Before it polls further or creates your database and user, it pins the provider's creation time, project, region and instance name. Retry checks that pin again, including after an app restart. A reused name or a missing original blocks setup and removal. Your bound instance and a resumed user operation require the original password in Vault; a missing password cannot trigger a reset. New services refuse an existing instance without a saved operation or binding.

This pin identifies an observed creation, not a provider-issued immutable UUID. Cloud SQL writes address instances by name, so the checks are fresh reads rather than atomic write preconditions. A replacement between a check and Google's write remains a provider API limitation. Older bindings can capture the currently observed creation during explicit execution, but cannot prove an earlier instance's ownership. Version 1 checkpoints have no creation pin and need reconciliation before retry; their saved operations are retained.

These paths have controlled endpoint and actual API restart tests. Live Google Cloud creation, SQL persistence and restore remain unqualified. Cloud SQL backup inventory is available for pinned, reviewed sources; isolated restore controls remain pending. See Google's [instance identity fields](https://docs.cloud.google.com/sql/docs/postgres/admin-api/rest/v1/instances) and [operation identities](https://docs.cloud.google.com/sql/docs/postgres/admin-api/rest/v1/operations).

Cloud SQL final backups remain in your Google Cloud project. Manage or restore them through the [provider's deleted-instance backup controls](https://docs.cloud.google.com/sql/docs/postgres/backup-recovery/manage-backups-deleted-instance). They are separate from the logical backup files stored through Trove. Loomplane can retain their metadata with matching source identity in Cloud snapshots. Isolated native restore controls and live cloud recovery qualification remain pending.

## Native cloud snapshot history

Open **Cloud snapshots** under **Service recovery** to inspect captured RDS and Cloud SQL histories in the selected environment. Choose the physical database history, then select **Refresh cloud snapshots**. This reads native recovery points from the original checked account and region. It does not create or remove a snapshot.

The history stays after you remove a service binding. Each saved point keeps its native reference and creation time, so a reused snapshot name gets a separate record. A complete scan can mark earlier points **No longer observed**. A partial scan cannot prove that a point disappeared, and a failed refresh preserves the last successful observations. Check the observation time and completeness before relying on the list.

When you open recovery for a named service, Cloud snapshots and copy history stay scoped to that resource, including after its live binding is removed. Changing the resource clears the previous page and any open copy review.

Creation times retain the provider's full nanosecond precision for identity and pagination. The `native_point_creation_identity` migration derives an exact key from the retained point JSON and keeps existing point IDs. Stop older API and worker processes before upgrading: their inventory writes use the previous uniqueness constraint. Allow time for the table rewrite and exclusive lock. The migration cannot recover points overwritten before this upgrade, and downgrade refuses while any native point history remains.

You need service backup read access. The saved managed account review must still match the connection; a changed account or unready credential blocks refresh. Old unpinned databases cannot refresh until an explicit execution captures their current identity. RDS discovery follows up to ten pages of 100 snapshots per refresh, reports an incomplete scan when more exist, and excludes shared and public snapshots.

Cloud SQL reads up to ten pages of 100 backups from the saved Google project. Each point must include source settings that match the original creation pin. If a list entry omits those settings, Loomplane reads that backup's own details. It never asks the current instance to prove an old backup's source. A backup from a newer instance that reused the name is excluded.

Missing source proof, unavailable point times, provider warnings or more pages leave the scan partial. Partial and failed scans preserve earlier observations. Only a complete scan can mark an earlier point no longer observed; the record stays in history. Cloud SQL's point time is the backup interval start, not a measured export completion or a promised PITR window. Google encrypts Cloud SQL backups by default, and Loomplane retains the optional KMS key reference. See Google's [backup fields](https://docs.cloud.google.com/sql/docs/postgres/admin-api/rest/v1/Backups), [project backup listing](https://docs.cloud.google.com/sql/docs/postgres/admin-api/rest/v1/Backups/ListBackups) and [backup encryption](https://docs.cloud.google.com/sql/docs/postgres/backup-recovery/backups).

Cloud SQL points retain the provider's absolute expiry time when reported. The dashboard and CLI show missing expiry as not reported, including RDS snapshots without an absolute expiry. Requested retention days do not supply that timestamp. Expired observations stay in history; refresh the original account to inspect current provider metadata before planning recovery. Inventory does not establish that a point can still be restored.

Your Google account needs permission to list and read these backups. A failed permission check leaves saved observations available so you can correct the account and retry. API, CLI, deletion and restart checks use controlled Google endpoints with a real local control-plane database. Live Google Cloud inventory and recovery remain unqualified.

You can read and refresh the same history with the CLI:

```sh
loomctl backups native-sources PROJECT_ID --environment-id ENVIRONMENT_ID --resource DATABASE_NAME --json
loomctl backups native-inventory SOURCE_ID --limit 50 --json
loomctl backups native-refresh SOURCE_ID --json
```

Pass the response's `next_before` as `--before` to read another source or point page. Saved points are provider metadata, separate from Trove backup files. Snapshot availability is not a tested restore. RDS native restore, PITR and native-backup deletion and retention controls remain unavailable in Loomplane.

The source and copy list commands accept `--environment-id` and `--resource` independently. Resource names match exactly. Each page and its cursor use the same scope, so a page cannot silently continue into another resource's history. Omit these filters to list the project's retained histories.

For Cloud SQL PostgreSQL 17, open **Cloud snapshots** in the project's recovery screen, then **Review native copy** on a saved point. Choose a project-granted Cloud and a managed Cloud SQL target in a separate recovery project. Review its compute, disk, region and lifetime, then acknowledge charges before creating the copy. A review lasts ten minutes; the copy's requested lifetime can be one to 24 hours. These capacity details do not include a price estimate.

You can save and submit the same review with `loomctl`:

```sh
loomctl backups native-copies review SOURCE_ID POINT_ID --cloud CLOUD_ID --target TARGET_ID --ttl-hours 2 --out recovery-review.json
loomctl backups native-copies create --review-file recovery-review.json --acknowledge-charges --wait
loomctl backups native-copies PROJECT_ID --environment-id ENVIRONMENT_ID --resource DATABASE_NAME --limit 25 --json
loomctl backups native-copies show COPY_ID --wait
loomctl backups native-copies cleanup COPY_ID --yes --wait
```

The review file is created with owner-only permissions and will not overwrite an existing file. Keep it if admission loses its response: retrying the same file returns the original copy without extending its expiry. In the dashboard, **Retry same request** does the same. Check retained copy history before starting a separate request.

**Native recovery copies** records status independently of snapshot inventory. A queued or available cloud instance is not SQL proof; the copy needs a recorded verification time. **Clean up copy** requires confirmation. **Cleanup needs attention** keeps the error and offers a retry. Charges can continue after expiry until deletion is confirmed. A revoked placement grant blocks new copy work, but an administrator with current recovery permission can still finish cleanup.

Native copy API, CLI, Dispatch and Vault checks use a real local control-plane database with controlled Google endpoints. The SQL verifier and backend also run against a private PostgreSQL fixture. Live Cloud SQL restore, IAM/TLS, privilege authority and cost qualification remain unverified; unsafe inherited SQL privileges cause verification to fail. Browser rendering of the native-copy UI and credential access for a verified copy remain unfinished. Live AWS snapshot and recovery qualification also remain pending.

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

The pin describes the configured namespace, not a physical disk or cloud account. Legacy points without a pin remain readable with checksum verification, but cannot be retired through the deletion helper. Automatic retention requires an explicit owner policy and safe deletion support from the captured storage driver.

## Back up and test recovery

In **Service recovery**, select your project, environment and database. **Back up now** queues an export using that engine's native backup client. Set **Backup interval (hours)** from one to 168 and save your schedule. **Enable backups** and **Pause backup schedule** preserve that interval. Recovery points stay until an owner requests deletion or enables a separate retention policy.

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

For managed PostgreSQL 17, choose a checked AWS or Google Cloud account connection:

```sh
loomctl services create "Orders RDS" --provider rds --target conn_your_aws_account --json
loomctl services create "Orders Cloud SQL" --provider cloud-sql --target conn_your_google_project --region europe-west1 --json
```

Here, `--target` chooses the environment's cloud account connection. Managed manifests use the provider's storage and contain no container target, image or mount. Selecting the account requires cloud account management access. If selection fails after project creation, the error includes that project's ID; no deployment plan is started.

`--region` is optional for managed services. Cloud SQL uses its default region when you omit it. RDS uses the selected AWS connection's region and blocks a plan that requests a different one. Review the returned plan, connection and provider charges before approving it with `loomctl approve PLAN_ID --yes`. Plan creation does not provision the database. The managed CLI profiles currently accept PostgreSQL only, and the Docker/Kubernetes backup commands do not apply to them.

Each new standalone RDS or Cloud SQL PostgreSQL 17 plan saves a versioned managed profile, the selected connection and its checked account identity. You can review the complete resource request in `--json` output before approval. Deployment checks that identity again. Moving an existing profiled database to another account or region, or changing its profile or engine version, requires a tested restore into a fresh service.

| Requested setting | AWS RDS                                       | Google Cloud SQL                                              |
| ----------------- | --------------------------------------------- | ------------------------------------------------------------- |
| Compute           | `db.t4g.micro`                                | `db-custom-1-3840`, Enterprise                                |
| Disk              | 20 GiB, encrypted gp3                         | 10 GiB, provider-encrypted SSD, automatic growth disabled     |
| Availability      | Single zone                                   | Single zone                                                   |
| Access            | Private VPC, environment ECS tasks            | Cloud SQL connector, IPv4 enabled without authorized networks |
| Automated backups | 7 days                                        | 7 retained backups                                            |
| Recovery logs     | 7 days through RDS backup retention           | 7 days, point-in-time recovery enabled                        |
| Removal           | Final snapshot and retained automated backups | Final backup requested for 30 days                            |

These are creation requests. They do not establish the actual retained recovery window or a successful restore, and they do not include a Loomplane point-in-time restore control. A Cloud SQL backup count is not a duration. RDS final snapshots have no Loomplane expiry; your account controls their lifetime. Provider availability and quotas can reject a requested class or region.

Existing databases without a managed profile keep their original settings when you replan. Loomplane does not label them with a new profile or silently reconfigure their storage and backups.

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

## Retire an old backup

In **Service recovery**, open **Review retention**. Select the history you want, set **Older than (days)** and **Keep newest**, then select **Update preview**. The table explains why each point stays or qualifies. Storage that cannot safely delete the captured file has no deletion action. An incomplete history also blocks deletion.

An organization owner can select **Delete backup** on an eligible point and confirm the file removal. **Deletion details** shows the current attempt, the protected recovery point and committed history. If deletion fails, check the retained point and storage permissions before selecting **Retry deletion**. Readers can inspect the same history but cannot request deletion or retry. A failed deletion keeps new restores blocked until it finishes.

You can also preview the selection from loomctl, then ask an organization owner to retire an eligible point:

```sh
loomctl backups retention BACKUP_ID --older-than-days 30 --keep-last 7 --json
loomctl backups retire BACKUP_ID --older-than-days 30 --keep-last 7 --request-id retirement-1 --yes --wait --json
loomctl retirements PROJECT_ID --limit 50 --json
loomctl retirements show RETIREMENT_ID --wait --json
loomctl retirements history RETIREMENT_ID --limit 50 --json
```

You need a write-capable owner key to request retirement or authorize a retry. Admins and read-only keys can inspect the records. Pass a response's `next_before` as `--before` to continue that list or history page.

Retirement deletes the selected backup file. The service and its retained data volume stay in place. The server rechecks the age/count rule, protects existing recovery copies and the newest restore-tested point, and verifies that retained point's current bytes through Trove before deleting anything. Concurrent restore admission and retirement are serialized. An accepted candidate becomes unavailable for new restores.

A failed retirement keeps that protection because deletion may have completed after its response was lost. Check the retained point and storage permissions, then authorize another bounded set of attempts:

```sh
loomctl retirements retry RETIREMENT_ID --request-id retirement-retry-1 --yes --wait --json
```

Reuse the key after a lost response. A new retry key authorizes up to three attempts from the lifetime count, with the current owner recorded separately from the original requester. History retains each committed transition and the previous failure.

The Docker PostgreSQL drill qualified retirement with local Trove storage, including a native SQL restore of the retained dump after deletion. Remote stores need captured-version deletion support in their Trove driver; the pinned S3 driver does not provide that capability yet. Legacy backups without a destination pin remain recoverable but cannot be retired. The dashboard retry and deletion flow has also been checked against native PostgreSQL backups with local Trove storage at desktop and phone widths. The same local profile also passed automatic retention through API and CLI: two scheduled native dump deletions, pause and a retained SQL restore. Dashboard enable, rule changes, revision history and pause were checked on that profile at desktop and phone widths, including deletion through the real scheduler. Kubernetes retirement and other engine/storage combinations need their own live qualification.

## Automatic retention policies

Open **Review retention**, choose the history and update its age/count preview. An owner can select **Enable automatic deletion** and confirm the displayed rule. When deletion is already enabled, **Update automatic policy** saves the reviewed settings after confirmation. You need a complete history, supported captured storage and a restore-tested recovery point before enabling it.

The summary shows the saved rule and revision. **Policy changes** lists each saved revision, its authorizing owner and date, with controls for older changes. **Last deletion** opens the existing deletion details. These reads keep refreshing while the review is open, including when the general live connection is healthy.

Select **Pause automatic deletion** to stop future selections while preserving the rule. You can pause even when storage is unavailable. If the policy changes while you confirm, close the dialog and review it again. If a save fails or its response is uncertain, select **Inspect current policy** before making another change.

Inspect the policy for any backup in the database's history, then enable it with an owner key:

```sh
loomctl backups retention-policy BACKUP_ID --json
loomctl backups retention-set BACKUP_ID --revision 0 --older-than-days 30 --keep-last 7 --yes --json
loomctl backups retention-history BACKUP_ID --limit 50 --json
loomctl backups retention-pause BACKUP_ID --yes --json
```

Revision zero creates a policy. To change one, pass the revision you just read. A conflicting update fails without replacing another owner's settings. If a response is lost, inspect the current policy before retrying. An unset policy returns `configured: false`; a failed inspection returns an error.

This authorizes recurring deletion. The minute reconciliation job selects at most one oldest eligible full backup per due evaluation and uses the same retained-byte checks as explicit retirement. Age/count settings, current owner authority and the captured physical source are checked again when work is admitted. A failed deletion holds that source until its bounded attempts complete or an owner checks the failure and authorizes a retry. Ordinary policy failures wait an hour before another evaluation.

Backup schedules and retention have separate switches. Pausing a backup schedule does not pause deletion. Pausing retention stops new selections; already accepted deletions may still finish. The policy records its last deletion ID, safe failure text and authorizing owner, and its history preserves every saved revision. Readers and admins can inspect that history. Only owners can change it. The Docker/local Trove drill also verifies a saved paused policy across control-plane restart and observes a completed scheduler pass after pause.

Worker loss keeps an accepted point unavailable for restore. On the Docker/local Trove profile, a restarted worker can finish the same deletion when its lease expires, including when the file was already removed. The crash drill covers process loss after file deletion and separately aborts the pending database completion. It advances the test lease expiry, so it does not measure the full timeout or qualify other interruption boundaries and storage profiles.

## API and permissions

| Operation                     | Route                                                                                                          | Permission                                      |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------- | ----------------------------------------------- |
| List/create backups           | `GET/POST /api/v1/projects/:id/service-backups`                                                                | `service_backup:read` / `service_backup:manage` |
| Read/retry a backup           | `GET /api/v1/service-backups/:id`, `POST .../:id/retry`                                                        | Read / manage                                   |
| Preview retention             | `GET /api/v1/service-backups/:id/retention-preview?older_than_days=30&keep_last=7`                             | `service_backup:read`                           |
| Request retirement            | `POST /api/v1/service-backups/:id/retire`                                                                      | `service_backup:retire` (owner)                 |
| List/read retirements         | `GET /api/v1/projects/:id/service-backup-retirements`, `GET /api/v1/service-backup-retirements/:id`            | `service_backup:read`                           |
| Read retirement history       | `GET /api/v1/service-backup-retirements/:id/history`                                                           | `service_backup:read`                           |
| Retry retirement              | `POST /api/v1/service-backup-retirements/:id/retry`                                                            | `service_backup:retire` (owner)                 |
| Read retention policy/history | `GET /api/v1/service-backups/:id/retention-policy`, `GET /api/v1/service-backups/:id/retention-policy/history` | `service_backup:read`                           |
| Save/pause retention policy   | `PUT /api/v1/service-backups/:id/retention-policy`                                                             | `service_backup:retire` (owner)                 |
| Set/list schedules            | `PUT/GET /api/v1/projects/:id/backup-policies`                                                                 | Manage / read                                   |
| Create a recovery copy        | `POST /api/v1/service-backups/:id/restore`                                                                     | `service_restore:manage`                        |
| List/read copies              | `GET /api/v1/projects/:id/service-restores`, `GET /api/v1/service-restores/:id`                                | `service_backup:read`                           |
| Remove a copy                 | `DELETE /api/v1/service-restores/:id`                                                                          | `service_restore:manage`                        |
| Review a native copy          | `POST /api/v1/native-recovery-sources/:id/points/:pointId/copy-review`                                          | `service_restore:manage`                        |
| Admit a native copy           | `POST /api/v1/native-recovery-copies`                                                                         | `service_restore:manage`                        |
| List/read native copies       | `GET /api/v1/projects/:id/native-recovery-copies`, `GET /api/v1/native-recovery-copies/:id`                       | `service_backup:read`                           |
| Clean up a native copy        | `DELETE /api/v1/native-recovery-copies/:id`                                                                    | `service_restore:manage`                        |

Backup creation accepts `environment`, `resource` and `request_id`. Restore creation accepts `request_id`. Retirement accepts `request_id` and a `policy` with `older_than_days` and `keep_last`; retry accepts a new `request_id`. Retirement lists and history accept `before` and `limit` (1 to 100). Project retirement lists also accept `backup_id` to resolve one recovery point without scanning other histories. Backup schedule updates accept `environment`, `resource`, `every_hours` and `enabled`. Retention policy updates accept the observed `revision`, `policy` (`older_than_days`, `keep_last`) and `enabled`. Retention history accepts `before` and `limit` (1 to 100). Requests and worker execution are scoped to the recorded organization and project. Scheduled requests recheck the policy creator's permission.

## Database gateway

A Vercel function can't open a TCP connection to Postgres. Even where a function could, a few hundred of them at once would use up the connection limit of a small database. The database gateway deals with both. Loomplane builds it, runs it on Cloud Run or ECS Fargate, and gives your app its address. Your app talks to the gateway over HTTPS or a WebSocket, and the gateway talks to Postgres over a short list of real connections, lending one out only for the length of a transaction.

Use it when a Vercel app needs a Postgres database on DigitalOcean or RDS. If your services run on the same cloud as their database and connect the usual way, you don't need it.

### What Phase 1 supports

| Database                        | Gateway on `cloud-run` | Gateway on `ecs-fargate`                                     |
| ------------------------------- | ---------------------- | ------------------------------------------------------------ |
| DigitalOcean managed PostgreSQL | Yes                    | Yes                                                          |
| RDS PostgreSQL                  | No                     | Yes, with the same AWS connection and region as the database |
| Everything else                 | No                     | No                                                           |

Everything else includes Cloud SQL, Render, and the Docker and Kubernetes profiles above. A plan that puts one of them behind the gateway stops with `db-gateway-backend-unsupported` and says why. The gateway itself only runs on Cloud Run and ECS, and any other provider gets `db-gateway-unsupported`.

DigitalOcean databases have a public address with TLS, so a gateway on either cloud reaches them. RDS sits inside its VPC, which is why only a gateway on ECS, deployed through the same AWS connection and in the same region, can get to it. When the gateway and the database are in different regions the plan warns (`db-gateway-region-mismatch`), because every query then crosses regions. You'll usually see that warning with DigitalOcean, whose region names (`nyc`, `ams`) never match a cloud's.

Apps are only ever handed an https address for the gateway. Cloud Run always has one. On ECS you get one when the AWS connection has a `certificate_arn`, an ACM certificate in the connection's region. Without it the plan stops with `db-gateway-needs-https`. Add the certificate, or run the gateway on Cloud Run. A gateway that ECS opened before the certificate existed keeps its plain HTTP port, so move `database_gateway` to another region or connection, or remove it and deploy again.

### Turn it on

Start with a signing key. Loomplane signs the routing snapshots that each gateway follows, and it can't deploy a gateway without a key to sign them with. Make one from 32 random bytes:

```sh
openssl rand -base64 32
```

Put the result in a JSON file that only its owner can read (mode `0600`), at an absolute path:

```json
{
  "version": 1,
  "private_key": "<the base64 you just made>",
  "not_before": "2026-10-01T00:00:00Z",
  "not_after": "2028-10-01T00:00:00Z"
}
```

Then point Loomplane at the file with `dbgateway.signing_key_file` in its configuration, or set `LOOMPLANE_DBGATEWAY_SIGNING_KEY_FILE`. The file has to be a regular file, not a symlink, no bigger than 16 KiB, with exactly those four fields. (A version 2 file holds a list of keys instead. You'll want that when you rotate, as [Rotate the signing key](#rotate-the-signing-key) shows.) The path has to be clean: no `..`, and no doubled or trailing slashes. `not_before` must come first. Give yourself a long window, because the key only signs between the two dates.

Loomplane reads the file once, at startup. If the file itself is wrong, Loomplane won't start. You get `dbgateway.signing_key_file: invalid database gateway signing key configuration` and nothing more, so the message can't leak the file. Leave the setting empty and Loomplane starts, but the gateway endpoints answer 503 and a deployment that includes a gateway fails.

A key outside its window is treated the same way as no key, and Loomplane still starts. Past `not_after`, it logs an error at startup, the gateway endpoints answer 503 with a message saying the key has expired, and a deployment that includes a gateway fails with that reason. That also happens the moment the key expires while Loomplane is running: it stops signing snapshots then, and never signs one with an expired key. Before `not_before` you get the same, saying the key isn't valid yet, until that date passes. Deployed gateways keep serving the routes they last applied, but they won't hear about a changed password or a paused route until a valid key signs again. From 30 days before the last key's `not_after`, Loomplane logs a warning at every start. Add the next key before then, as [Rotate the signing key](#rotate-the-signing-key) describes.

Gateways are handed the public halves of the keys when they're deployed. The plan records which keys those are, so when they change (you add one, renew one, or one expires) the next plan shows `db-gateway` as an update, and deploying it hands the gateway the keys Loomplane has now. The apps behind it stay as they are, since its address doesn't move. If the keys change between making a plan and deploying it, the deployment fails and asks you to plan again, so a gateway never runs with keys other than the ones you reviewed. The first plan after you upgrade to a Loomplane that records them updates each gateway once, for the same reason.

A gateway also has to reach Loomplane to fetch its routes, and it does that at your `public_url`. That has to be https (plain http is accepted for `localhost` only), or the deployment is refused.

Next, tell the manifest where the gateway runs and which resources go behind it:

```yaml
database_gateway:
  provider: cloud-run
  region: us-central1

resources:
  db:
    kind: postgres
    provider: digitalocean
    gateway: true

services:
  web:
    source: apps/web
    provider: vercel
    needs: [db]
```

The gateway names each database from its resource and your DNS zone, as `<resource>.<region>.db.<zone>`. So `db` in `us-central1` on `example.com` becomes `db.us-central1.db.example.com`. That needs a verified zone granted to the project, which you add under Domains. With two or more, Loomplane keeps the ones this environment's domain routes use. If that still leaves several, the plan stops with `db-gateway-domain-ambiguous`, and with none it stops with `db-gateway-domain-missing`. Nothing has to resolve these names. They tell the gateway which database a connection is for.

Create a plan and review it. Turning the gateway on adds one `db-gateway` service, deploys it before anything that needs a resource behind it, and redeploys those apps with their new variables. Taking a resource off the gateway redeploys them too, and they go back to the database's own address before the gateway stops serving it. A new region or DNS zone renames every host behind the gateway, so the same plan that updates the gateway redeploys every app that reaches a database through it. An app you've held back can't follow, and the plan stops with `db-gateway-held-user` until you release it.

Opting a database in changes `DATABASE_URL` for every service that needs it, not just the Vercel app. Apps on Cloud Run and ECS get the gateway's host in `DATABASE_URL` too, so a service that connects over TCP stops working until it reads `DATABASE_URL_DIRECT` instead. The next section says where that comes from. The plan stops with `db-gateway-held-user` if it would take a resource off the gateway while a [held](manifest.md#held) app, one the plan leaves alone, still reaches it there.

### Rotate the signing key

You rotate by running the old key and the new one side by side for a while. A version 2 key file holds both:

```json
{
  "version": 2,
  "keys": [
    {"private_key": "<the old key>", "not_before": "2026-10-01T00:00:00Z", "not_after": "2028-10-01T00:00:00Z"},
    {"private_key": "<the new key>", "not_before": "2028-09-01T00:00:00Z", "not_after": "2030-10-01T00:00:00Z"}
  ]
}
```

Each entry takes the same three fields as a version 1 file, and a file holds up to eight. Loomplane signs with the newest key whose window is open, the one with the latest `not_before`, and hands gateways every key that hasn't expired, including one whose window hasn't opened yet. So a rotation goes like this:

1. Make a new key with `openssl rand -base64 32` and add it beside the old one. Give it a `not_before` far enough out that you can deploy every environment with a gateway before it arrives, and let its window overlap the old key's, or Loomplane has nothing to sign with in the gap. Restart Loomplane.
2. Plan and deploy each environment that has a gateway. The plan shows `db-gateway` as an update. The redeployed gateway trusts both keys, and Loomplane is still signing with the old one, so it gets its routes straight away.
3. When the new key's `not_before` passes, Loomplane signs with it from then on, without a restart. Gateways from step 2 take what it signs. A gateway you missed refuses it and keeps serving the routes it has, the same as one whose key expired, until you deploy it.
4. Take the old key out of the file and restart Loomplane, or just let it expire. Either way the next plan redeploys each gateway trusting the new key alone. There's no hurry: a gateway ignores a key that has expired.

To extend a key rather than replace it, add the same `private_key` again with a later window, and follow the same steps. A gateway takes a snapshot while any window of that key is open.

### Connect from Vercel

An app that needs `db` gets three variables. `DATABASE_URL` keeps the user and password but carries the gateway's name for the database as its host. `DATABASE_URL_GATEWAY` is the gateway's own https address. `DATABASE_URL_DIRECT` is the database's own address, exactly as it was before you opted in, and it's a secret because it holds the credentials. The last two are always the first's name with `_GATEWAY` or `_DIRECT` on the end. Only Postgres can opt in today.

Install `@neondatabase/serverless` and `ws`. The gateway speaks the protocol this driver uses, and we've verified version 1.2.0 against it. All you do is tell the driver where the gateway is:

```js
import { Pool, neon, neonConfig } from "@neondatabase/serverless";
import ws from "ws";

const gateway = new URL(process.env.DATABASE_URL_GATEWAY);

neonConfig.webSocketConstructor = ws;
neonConfig.wsProxy = (host, port) =>
  `${gateway.host}/v2?address=${host}:${port}`;
neonConfig.fetchEndpoint = () => `${gateway.origin}/sql`;

// Over a WebSocket: transactions, LISTEN, prepared statements.
export const pool = new Pool({ connectionString: process.env.DATABASE_URL });

// Over HTTP, one request per query.
export const sql = neon(process.env.DATABASE_URL);
```

The driver adds the `wss://` itself, so `wsProxy` returns a host and path with no scheme. `fetchEndpoint` is a full URL. The gateway picks the route from the `address` it's given on a WebSocket, or from the connection string it's sent over HTTP. Leave `useSecureWebSocket` alone, because the default is right for an https gateway. A runtime with a global `WebSocket` doesn't need `ws` or the `webSocketConstructor` line.

Queries work the way the driver documents them:

```js
const { rows } = await pool.query("select now()");
const result = await sql.query("select $1::int as n", [7]);
```

Use the WebSocket for transactions and anything that holds state between statements. Use HTTP for single queries, keeping in mind that a request has a 60 second limit and a 10 MiB body, caps the size of its result, and can't run `COPY FROM STDIN`, and that when a result is too large the error tells you to switch to the WebSocket.

### Pooling and what pins a session

The gateway pools by transaction. Your session gets a real connection when a transaction begins and hands it back at the end, so a function that sits idle between requests holds nothing at the database, and each gateway instance sizes its pool from the database's `max_connections`, keeps back at least five (or a tenth) for everything else, and splits the rest between the running instances.

Most session state survives that. The gateway remembers `SET` on these parameters and replays it on whichever connection serves your next transaction: `application_name`, `search_path`, `timezone`, `statement_timeout`, `lock_timeout`, `idle_in_transaction_session_timeout`, `datestyle`, `intervalstyle`, `extra_float_digits`, `standard_conforming_strings`, `default_transaction_isolation`, `default_transaction_read_only` and `work_mem`. Named prepared statements from your driver are prepared again wherever they next run. `LISTEN` and `UNLISTEN` work too, through a listener the gateway shares between sessions.

State the gateway can't replay pins the session instead. A pinned session keeps one connection until it disconnects, and that connection counts against the database like any other. These pin:

- Session advisory locks: `pg_advisory_lock` and its `try` and `shared` forms, and any `pg_advisory_unlock` call.
- Temporary tables and objects, and anything that names `pg_temp`.
- Cursors declared `WITH HOLD`, and SQL-level `PREPARE`, including `PREPARE TRANSACTION`.
- `SET` of any parameter not in the list above, `set_config()`, and a `SET` that runs inside an open transaction, where a rollback would undo it.
- `DO`, `LOAD`, `setseed`, `dblink_connect` and `dblink_connect_u`.
- `query_to_xml`, `query_to_xmlschema`, `query_to_xml_and_xmlschema` and `ts_stat`, which run SQL held in a string that the gateway doesn't read.
- The protocol's fast-path function call.
- A text with several statements that includes `SET`, `RESET`, `LISTEN` or `UNLISTEN`, since the gateway can't tell which one you meant to keep.

`COPY` holds a connection only until the statement finishes. The classifier errs towards pinning, so now and then something pins that didn't need to.

The gateway only sees the SQL you send. A function you wrote yourself that takes an advisory lock, creates a temporary table or calls `set_config` inside its body looks like any other function call, so the session doesn't pin, and that state stays on a connection the next transaction may not get. Keep session state out of your own functions, or take it with one of the statements above in the same session.

### Limits

Phase 1 speaks Postgres to clients over WebSocket and HTTP only. There is no TCP listener, so `pg` and other drivers that open a plain socket can't connect through the gateway from Vercel yet. The database names the gateway hands out don't resolve for them either. Use the Neon driver as above.

Anything else that opens a TCP connection, a migration step for example, should read `DATABASE_URL_DIRECT`. Prisma's `directUrl` is the usual place for it. That's the database's own address, so it works only from somewhere that can reach the database itself: a DigitalOcean database is public, but RDS is private to its VPC, and a Vercel build can't get into it. If your migrations run from Vercel against RDS, run them from a machine inside the VPC instead.

Putting a database behind the gateway makes it reachable from the internet. The gateway's endpoint is public, so anyone who has a database's host name and its user and password can connect through it. That includes RDS, which is otherwise private to its VPC: once it's behind a gateway on ECS, its password is all that stands between it and the internet. Phase 1 doesn't rate-limit failed logins either, so nothing slows down someone guessing. Keep the database passwords long and random, and leave a database off the gateway if nothing outside its network needs it.

`currval()` and `lastval()` don't work across transactions. The sequence value belongs to a connection, and your next transaction may run on another one. Get the value in the same statement with `INSERT ... RETURNING id`. PgBouncer in transaction mode has the same limit.

`LISTEN` and `NOTIFY` go through a listener shared by every session on the route. If that listener loses its connection and redials, or a `LISTEN` is refused on the way back, notifications sent in the gap can be dropped. Postgres doesn't keep notifications for absent listeners anyway, so don't rely on a notification as your only signal. Read the table again after a reconnect.

Loomplane runs at least two instances of the gateway, so one that's draining or starting cold never leaves your apps without a database. That's the minimum instance count on Cloud Run and the desired count on ECS. It sets no maximum, so the platform's own applies.

On Cloud Run, Loomplane doesn't set the request timeout or keep CPU allocated between requests yet, so the gateway runs with Cloud Run's defaults. Cloud Run counts a WebSocket session as one request, which means it cuts the session when the request timeout runs out (five minutes by default) and your driver has to connect again. Keep transactions short and let the pool reconnect. Outside requests Cloud Run also throttles the CPU, so an idle instance polls Loomplane for its routes slowly. A paused route or a changed password can take a while to reach an instance that's had no traffic. ECS has neither limit.

When an instance shuts down, for a deploy or because the platform scales it in, it stops taking connections straight away and gives the sessions it has 8 seconds to finish. A session still in a transaction after that gets `57P01` and is closed, so your driver should retry on a new connection. The whole shutdown fits inside the 10 seconds Cloud Run allows between SIGTERM and stopping the instance. The gateway reads that limit from `LOOMPLANE_DBGW_DRAIN_SECONDS`, which Loomplane leaves at its default.

A gateway keeps the last routes it was given in memory, and in a sealed file under `/tmp/dbgw`. Cloud Run and Fargate start every new instance with an empty disk, so a new instance has to fetch its routes from Loomplane before it serves anything. While Loomplane is unreachable, running instances carry on with what they have. Instances that start in that window, whether from scaling up, a crash or a deploy, can't start serving until Loomplane answers. Keep `public_url` reachable from the cloud the gateway runs on.

You'll find each gateway on the project's Services tab, with the instances seen in the last hour and a route for each database. Anyone with `infrastructure:modify` can pause a route there, and resume it from the same place. While a route is paused, new transactions wait up to 30 seconds and then fail.

## Qualification boundary

PostgreSQL 17.11 has passed the Docker catalog and recovery drill, with local Trove and a separate native adapter drill against an isolated MinIO endpoint. The Kubernetes catalog, CLI, API and browser drill passed on arm64 Kubernetes 1.36.4, Cilium 1.20.3 and a retained local-path class. That drill covered pod replacement, backup persistence after a control-plane restart, isolated SQL recovery, scheduled backups and deletion of the exact temporary PV.

AWS S3 acceptance, managed cloud recovery, production CSI and cross-node qualification, high availability, point-in-time recovery, explicit source-data destruction, Kubernetes and other-engine retention, upgrades, credential rotation and live binding cutover remain pending. A digest pin is not a claim that every architecture has passed live acceptance; the published recipe contains both amd64 and arm64 pins, while the local acceptance host exercised arm64.

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
