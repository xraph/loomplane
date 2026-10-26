# Running Loomplane

What it takes to keep a Loomplane running: backups, restores, upgrades, and knowing when something goes wrong. The commands here are `loomplane` commands, run with the same configuration the server runs with.

## Live deployment state

Proxy `/api/v1/realtime/ws` with WebSocket upgrades and leave `/api/v1/realtime/events` unbuffered for SSE. Both routes use the same authenticated Forge contracts and organization-scoped cursor. The dashboard falls back to SSE after a WebSocket failure, then to REST recovery when the stream is stale. A Live badge confirms a recent status heartbeat; a disconnected page must not be treated as current.

Events and build output are committed in Postgres before delivery. Conduit notifications wake other API replicas, which read the durable sequence. You can restart an API or deployment worker without relying on its memory for status or checkpoints. An interrupted Docker build may need rebuilding; its earlier attempt stays visible as incomplete.

If a database timeout stops Dispatch's durable worker, the API starts a replacement on its own: one second later at first, doubling up to thirty seconds while the database stays down. Until a replacement is polling, `/_/health` reports the dispatch extension as unhealthy and the log has a `durable workflow worker stopped` warning for each attempt. A replacement only claims tasks whose lease has run out, so a plan the failed worker had claimed picks up again once that lease expires. If the extension stays unhealthy after the database is back, restart the API. Watch this health result in your process supervisor, because a running HTTP listener doesn't tell you deployment work is moving.

Build output is retained for seven days, with 16 KiB per line and 10 MiB per action attempt. The deployment log endpoint pages retained output. A cursor older than retention produces a gap and a current snapshot. Collector checkpoints and cloud credentials are excluded from public events; runtime variables stay out of Docker build arguments.

Keep a connection while a plan, build, binding or deployment references it. Changing a host key, namespace UID, CA or reviewed registry endpoint requires another connection check and plan. Rotating a credential can keep the same destination. Removal uses each binding's recorded connection and ownership labels, including resources left pending cleanup after a replacement.

## Backups

```bash
loomplane backup --out loomplane-backup.tar.gz
```

A backup is one file: every table in Loomplane's database, Loomplane's own and every library's (authsome, Warden, Chronicle, Dispatch, Ctrl Plane, Vault, Keysmith), copied in a single snapshot, so the tables agree with each other however long the copy takes. You can run it while Loomplane is serving. The file is written beside its final name and renamed once it's complete, and only you can read it.

```bash
loomplane backup inspect loomplane-backup.tar.gz
```

shows which Loomplane made a backup, when, and how many rows each table has, without restoring anything.

A backup holds secrets the way the database does: cloud credentials and variables as Vault ciphertext, API keys as hashes. Reading the secrets needs the `secret_key` Loomplane ran with. Keep that key, and keep it somewhere other than beside the backups, or a restored Loomplane will start without being able to deploy.

What isn't in a backup: the source tarballs in `storage.url`, which Loomplane fetches again from GitHub when it needs them, and anything in your clouds. A restore brings back Loomplane's record of what runs where; the next drift check compares it with what's live.

Schedule it however you schedule things. With cron, for one a night kept for two weeks:

```bash
0 3 * * * cd /srv/loomplane && loomplane backup --out backups/loomplane-$(date +\%F).tar.gz && find backups -name 'loomplane-*.tar.gz' -mtime +14 -delete
```

## Restoring

Stop Loomplane, then:

```bash
loomplane restore loomplane-backup.tar.gz
```

into an empty database. Restore migrates it to this Loomplane's schema, then loads every table in one transaction, so a restore that fails leaves nothing half done. It refuses a database a Loomplane has already run on; pass `--force` to replace what's there. Start Loomplane again with the `secret_key` it ran with before.

A backup made by a newer Loomplane can't be restored by an older one: its rows fit a schema the older one doesn't have. Restore it with the same version or a later one.

## Upgrading

When building from source, use `make build VERSION=your-version`, or `make build-cli VERSION=your-version` for `loomctl` alone. The API, worker and CLI share `internal/buildinfo.Version`. Custom build scripts should set `-ldflags "-X github.com/xraph/loomplane/internal/buildinfo.Version=your-version"`. The CLI reads this small metadata package without importing the server's extensions.

1. Back up.
2. Stop Loomplane and loomplane-worker.
3. Replace the binaries, or the image.
4. Start loomplane. It applies the new migrations before it starts serving.
5. Start loomplane-worker.

```bash
loomplane migrations
```

shows how many migrations the database has and which are pending, before you start. `loomplane migrate` applies them without starting the server, if you'd rather do that step on its own.

Loomplane won't start against a database a newer Loomplane migrated. It would read and write a schema it doesn't know, so going back a version means restoring the backup you made before upgrading. In development it only warns: branches share a local database, and one branch's migrations are often unknown to another's checkout.

Migrations only move forward. Each one is written to be safe to run again, and none is ever edited once released; a fix is a new migration.

## Watching it

`/_/metrics` serves Prometheus metrics: the Go runtime's, Dispatch's jobs and workflows, and Loomplane's own, all prefixed `forge_loomplane_`:

| Metric                                             | Labels                     | What it counts                                                |
| -------------------------------------------------- | -------------------------- | ------------------------------------------------------------- |
| `deployments_total`, `deployment_duration_seconds` | `status`                   | Deployments that finished, and how long they took             |
| `deployment_steps_total`                           | `provider`, `op`, `status` | Each action of a deployment                                   |
| `deployment_step_duration_seconds`                 | `provider`, `status`       | How long each provider takes                                  |
| `plans_total`                                      | `status`                   | Plans worked out: ready, blocked or failed                    |
| `plan_decisions_total`                             | `decision`                 | Approved, rejected, stale or expired                          |
| `drift_findings_total`                             | `provider`, `state`        | Bindings found missing or changed                             |
| `analyses_total`, `analysis_duration_seconds`      | `status`                   | Repository analyses, from loomplane-worker                    |
| `api_key_authentications_total`                    | `result`                   | Requests with an API key, accepted or rejected                |
| `connection_checks_total`                          | `cloud`, `result`          | Credential checks: ready, incomplete, rejected or unavailable |

Worth an alert: `deployment_steps_total{status="failed"}` rising for one provider, which usually means its connection lost a permission; `api_key_authentications_total{result="rejected"}` climbing, which is a revoked key still in a pipeline or someone guessing; and `drift_findings_total`, which means something changed in a cloud outside Loomplane.

`/_/health` reports each part, provider plugins included: a plugin that stops answering makes it unhealthy within one check interval. `/_/health/live` and `/_/health/ready` are for your orchestrator's probes.

## Pausing a target

To stop Loomplane deploying to a target, during an incident on a cloud for instance, name it in `providers.paused`:

```yaml
providers:
  paused: [render, cloud-run]
```

or set `LOOMPLANE_PAUSED_PROVIDERS=render,cloud-run`. Plans that use a paused target are still made, and can be reviewed and approved, but they say they're for review only, and approving one deploys nothing. What already runs there keeps running, and drift checks still read it. A name that isn't a target stops Loomplane from starting, so a typo can't leave a target running that you meant to pause. Restart Loomplane to pause or unpause one.

## Email and deployment notifications

Open Notifications to read deployment results, change your deployment email preference and, as an owner or admin, manage signed webhooks. Deployment mail goes to the person who started the deployment. Loomplane checks their current organization membership and project access before delivery. Account recovery and verification mail do not follow that deployment preference.

An empty SMTP host disables email. Invitations then show a manual link and explicitly report that email is disabled. To test delivery with Mailpit:

```sh
docker run --rm --name loomplane-mailpit \
  -p 127.0.0.1:1025:1025 -p 127.0.0.1:8025:8025 \
  axllent/mailpit:v1.30.0
```

Set these values in your environment before starting the API:

```sh
export LOOMPLANE_NOTIFICATIONS_SMTP_HOST=127.0.0.1
export LOOMPLANE_NOTIFICATIONS_SMTP_PORT=1025
export LOOMPLANE_NOTIFICATIONS_FROM=accounts@example.com
export LOOMPLANE_NOTIFICATIONS_SMTP_TLS=false
```

Open Mailpit at `http://127.0.0.1:8025`. Set `public_url` to the dashboard address you use; recovery and invitation links come from this trusted setting. Production requires implicit SMTP TLS, commonly port 465, and a valid sender address. Set `LOOMPLANE_NOTIFICATIONS_SMTP_USERNAME` and `LOOMPLANE_NOTIFICATIONS_SMTP_PASSWORD` together when the server requires authentication. Keep the password outside committed configuration.

Herald stores encrypted SMTP credentials and sanitized delivery outcomes. It does not retain recipient addresses, rendered security mail, reset tokens or invitation links in its message history. Invitations report `sent`, `disabled` or `failed` independently of creation, so a delivery failure does not discard a valid invitation. Password reset always returns the same public response for known and unknown addresses; inspect sanitized server diagnostics when delivery fails.

A database trigger captures each future terminal deployment attempt in the transaction that writes it. Notification processing retries up to ten times, then marks its health check unhealthy. Retry notification processing on the Notifications page after fixing the cause. Replay keeps completed channels and Relay's durable acceptance identity, so it does not create another webhook event. SMTP has at-least-once delivery: a crash after the mail server accepts a message but before Loomplane records success can produce a duplicate.

Webhook endpoints require public HTTPS on port 443. Loomplane blocks private and reserved addresses, resolves again when connecting and refuses redirects. The signing secret appears once after creation or rotation. Store it in your receiver's secret manager and verify Relay's `X-Relay-Signature` against the exact request body and `X-Relay-Timestamp`, with a receiver-side timestamp tolerance. Relay owns HTTP retries and durable delivery history; the page shows attempts, HTTP status and the next retry without retaining response bodies in the public API. A receiver that returns 410 disables its endpoint.

## Fabriq projection rebuilds

The `loomplane.fabriq.rebuild` job runs the graph or search rebuild requested from a Fabriq service's Engines tab. It checks the requester's current permissions and the resource's current configuration before running the pinned rebuild command. Failed runs retain a redacted output tail. Retryable failures can be retried from the same tab.

The `loomplane.fabriq.rebuild.reconcile` cron runs every minute to re-enqueue queued work after an interruption. Keep Dispatch workers running on at least one replica. Rebuilds do not replace Postgres backups; see [Fabriq](fabriq.md) for recovery and library-mode setup.
