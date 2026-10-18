# PostgreSQL services

Open **Services** to deploy PostgreSQL 17 on a checked Docker server. Choose a project name and target, leave daily backups enabled if you want them, then select **Deploy PostgreSQL**. Loomplane creates a repository-free project, checks its plan, and approves only the single database creation you requested. Unexpected actions or warnings require normal plan review.

This profile runs one private PostgreSQL instance with a digest-pinned image, a retained Docker volume and a separate application role. That role cannot create databases or roles and is not a superuser. Database credentials stay in Vault. Applications deployed in the same environment on the same target receive the resource URL through the existing resource binding workflow.

## Persistence and backup storage

A container replacement retains its named volume. Removing the container also retains the volume. Docker local storage shares host capacity: this profile does not reserve a disk quota or survive host loss on its own. Retained-volume inventory and deliberate destruction are still planned, so record the instance and target before removing a service.

Loomplane uses the Trove extension for artifacts and backup files. Set `LOOMPLANE_BACKUP_URL` and `LOOMPLANE_BACKUP_BUCKET` to choose a separate backup destination. Without a backup URL, backups use the artifact storage backend in a separate bucket. The development default is local storage.

For an S3-compatible service, the URL takes this form:

```text
s3://ACCESS_KEY:SECRET_KEY@REGION/BUCKET?endpoint=https://storage.example.com&path_style=true
```

Supply the URL through the deployment's secret configuration, not a committed file. URL-encode credential characters. Set `LOOMPLANE_BACKUP_BUCKET` to the same pre-created bucket. Remote buckets must already exist and allow the configured identity to list, read, write, and complete or abort multipart uploads. Local buckets are created at startup. Test the exact provider and endpoint before relying on it for recovery.

The current transfer path uses 8 MiB chunks, bounded retries and a SHA-256 digest. It caps a backup at about 78 GiB. A process crash can leave incomplete multipart uploads; configure provider-side incomplete-upload cleanup until durable multipart reconciliation is available. Do not configure expiry of completed recovery objects behind Loomplane's records.

## Back up and test recovery

In **Service recovery**, select your project, environment and database. **Back up now** queues a native custom-format PostgreSQL export. Set **Backup interval (hours)** from one to 168 and save your schedule. **Enable backups** and **Pause backup schedule** preserve that interval. All recovery points are retained; automatic retention is not implemented.

The lists show recovery points and copies for the selected database and environment. If you remove the source service, its backup history and restore controls remain available. Backup creation and scheduling need a live source.

A successful upload initially shows **Not tested**. **Test restore** creates an isolated copy with its own volume and credentials, verifies the file before restoring, runs PostgreSQL's restore command in a transaction, and checks SQL access. A successful copy records its verification time and duration. The live source and application bindings stay in place.

Copies have no published ports and use an internal network. They expire after 24 hours; cleanup failures remain visible and retry automatically after an hour. An authorized administrator can retry sooner with **Remove copy**. Scheduled cleanup uses the creator's current permissions; if that access has been revoked, another administrator must finish removal. Removing a copy deletes its temporary volume while preserving the backup and verification record. SQL verification does not establish that your application can recover correctly.

If a restore worker disappears, its operation becomes a failure after the lease expires. Remove the temporary copy and request a new restore. Each restore runs once; an interrupted command is not replayed over a possibly changed database.

## Use loomctl

You can prepare a PostgreSQL service without connecting a repository:

```sh
loomctl services create "Application database" --target conn_your_checked_server --json
loomctl approve PLAN_ID --yes
```

The first command creates a project, saves its resource manifest and returns a plan for review. Approval starts deployment. You can also save a resource-only manifest yourself:

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

With `--wait`, the CLI follows the persisted operation for up to 45 minutes. Set `--timeout 10m` to change that limit. A timeout stops waiting; you can follow the same ID with `show --wait`. Failed operations return a nonzero exit status and retain their JSON result for inspection. `--json` outputs a single JSON value without a request-ID prefix; request errors include the reusable key.

## API and permissions

| Operation              | Route                                                                           | Permission                                      |
| ---------------------- | ------------------------------------------------------------------------------- | ----------------------------------------------- |
| List/create backups    | `GET/POST /api/v1/projects/:id/service-backups`                                 | `service_backup:read` / `service_backup:manage` |
| Read/retry a backup    | `GET /api/v1/service-backups/:id`, `POST .../:id/retry`                         | Read / manage                                   |
| Set/list schedules     | `PUT/GET /api/v1/projects/:id/backup-policies`                                  | Manage / read                                   |
| Create a recovery copy | `POST /api/v1/service-backups/:id/restore`                                      | `service_restore:manage`                        |
| List/read copies       | `GET /api/v1/projects/:id/service-restores`, `GET /api/v1/service-restores/:id` | `service_backup:read`                           |
| Remove a copy          | `DELETE /api/v1/service-restores/:id`                                           | `service_restore:manage`                        |

Backup creation accepts `environment`, `resource` and `request_id`. Restore creation accepts `request_id`. Policy updates accept `environment`, `resource`, `every_hours` and `enabled`. Requests and worker execution are scoped to the recorded organization and project. Scheduled requests recheck the policy creator's permission.

## Qualification boundary

The verified profile is PostgreSQL 17.11 on a checked Docker target, with native logical backups, private recovery copies, and local Trove or the tested isolated MinIO endpoint. AWS S3 acceptance, Kubernetes, high availability, point-in-time recovery, retained-data inventory, automatic retention, upgrades, credential rotation and live binding cutover remain pending. A digest pin is not a claim that every architecture has passed live acceptance; the published recipe contains both amd64 and arm64 pins, while the local acceptance host exercised arm64.

See the [implementation plan](one-click-services-plan.md) for the exact verification ledger and remaining catalog work.
