# Running Loomplane

What it takes to keep a Loomplane running: backups, restores, upgrades, and knowing when something goes wrong. The commands here are `loomplane` commands, run with the same configuration the server runs with.

## First run

A new instance has no users, so every signed-out dashboard page sends you to `/setup`. Give it your name, email, password and the name of your first organization. Loomplane creates you as a normal user who also holds the `platform-owner` role, creates the organization with you as its owner, and signs you in.

Setup works once. A second attempt is refused, and so is a call to `GET`/`POST /api/v1/setup` after any user exists. Until it has run, the server refuses every other sign-up, so nobody can get in ahead of you. Open a new instance in a browser as soon as it's up.

## Who can sign up

`LOOMPLANE_WAITLIST_ENABLED` (config key `identity.waitlist`) decides whether sign-up is open or goes through a waitlist.

| Value   | What happens                                                                              |
| ------- | ----------------------------------------------------------------------------------------- |
| unset   | The instance's stored setting decides                                                     |
| `true`  | The waitlist is on, whatever is stored                                                    |
| `false` | Anyone can sign up, whatever is stored                                                    |

With it unset, a fresh instance stores "on" when setup completes. An instance that existed before this release has nothing stored, so it stays open after you upgrade. Set `true` if you want the waitlist there.

When the waitlist is on, the sign-up screen becomes "join the waitlist" and asks for an email only. People get in with an approved waitlist entry or an organization invitation. Invitations always work, so anyone who can invite someone can widen access. When it's off, anyone can sign up, as before.

You approve or reject entries on the Waitlist page of the console. Approved people get an email with a sign-up link, if mail is configured (see [Email and deployment notifications](#email-and-deployment-notifications)). Approval still works without mail; you'd pass the news on yourself.

## The console

The console is the Forge dashboard, served at `LOOMPLANE_CONSOLE_PATH` (config key `console.path`). The default is `/__console`. The value must be a single path segment starting with `__`, such as `/__ops-7f3`. Its API lives under `/api/_console`.

Only users holding `platform-owner` or `platform-admin` can open it. To everyone else it looks like a page that doesn't exist, and they get a 404. Nothing in the dashboard links to it; admins find it through the public config.

From it you can:

- view, ban and unban users
- view and revoke sessions
- approve or reject waitlist entries
- read apps, environments, settings and roles

It's read-only for those last four. You can't delete the app, change settings, toggle sign-in methods, assign roles or delete users from there.

Pick your own path per instance if you like, but the role check is what protects the console, not the path. Platform admins have equal powers, so one can ban another, the owner included. Keep the list short.

## Granting platform access on an existing instance

After you upgrade an instance that already had users, nobody holds a platform role, so nobody can open the console. Run this once, with the same configuration and database the server uses:

```bash
loomplane admin grant-platform-admin you@example.com
```

It grants `platform-owner` to that existing user and changes nobody else. Running it twice is safe. Loomplane must have started against the database at least once first, so the roles exist.

## Live deployment state

Proxy `/api/v1/realtime/ws` with WebSocket upgrades and leave `/api/v1/realtime/events` unbuffered for SSE. Both routes use the same authenticated Forge contracts and organization-scoped cursor. The dashboard falls back to SSE after a WebSocket failure, then to REST recovery when the stream is stale. A Live badge confirms a recent status heartbeat; a disconnected page must not be treated as current.

Events and build output are committed in Postgres before delivery. Conduit notifications wake other API replicas, which read the durable sequence. You can restart an API or deployment worker without relying on its memory for status or checkpoints. An interrupted Docker build may need rebuilding; its earlier attempt stays visible as incomplete.

When the database stops answering, Dispatch's durable worker keeps trying to claim work and backs off between attempts, and `/_/health` reports the dispatch extension as unhealthy until a claim gets through. A timeout while it saves a task's result can still stop the worker. The API then starts a replacement on its own: one second later at first, doubling up to thirty seconds while the database stays down. Until a replacement is polling, the extension stays unhealthy and the log has a `durable workflow worker stopped` warning for each attempt. A replacement only claims tasks whose lease has run out, so a plan the failed worker had claimed picks up again once that lease expires. If the extension stays unhealthy after the database is back, restart the API. Watch this health result in your process supervisor, because a running HTTP listener doesn't tell you deployment work is moving.

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

## A server that refuses every deployment

If every deployment to one SSH/Docker server fails with `runtime effect fence could not establish quiescence`, that server's fence journal is stuck. Loomplane keeps a small journal on each Docker host, under `/var/tmp/loomplane-fence-<hash>/`, so a deployment can tell whether the last write to that daemon finished. While a write is in flight the journal is marked dirty, and Loomplane won't touch the daemon again until the mark clears. Builds of main from late October 2026 left it dirty after a database readiness check, and nothing clears it on its own.

Check that nothing Loomplane started is still working on that server (no deployment, route change or pool recovery in flight). Then run this on the server, as the SSH user Loomplane connects with:

```bash
D=/var/tmp/loomplane-fence-$(printf %s "$(docker info -f '{{.ID}}')" | sha256sum | cut -d' ' -f1)
flock "$D/daemon.lock" python3 -c 'import json,sys;p=sys.argv[1]+"/state";s=json.load(open(p));s["dirty"]=False;open(p,"w").write(json.dumps(s))' "$D"
```

It takes the same lock the deployment helper takes, clears the mark and keeps the recorded generations, so an older attempt still can't write. Plan again afterwards.

A journal can also go dirty for a reason that still matters: the host rebooting or the helper being killed in the middle of a Docker change. If that's what happened, look at the containers on that server first, because the change may be half done.

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

The `loomplane.fabriq.rebuild.reconcile` cron runs every minute. It re-enqueues queued rebuilds after an interruption, and it ends rebuilds that never reported back. A run is capped at 65 minutes, so a rebuild still marked running 75 minutes after a worker claimed it belongs to a worker that stopped. The cron marks it failed with "The rebuild stopped before Loomplane heard how it ended. Rebuild again." You'll see that text in the output on the Engines tab, with the retry offered. Request the rebuild again; Loomplane never resumes the old run. Keep Dispatch workers running on at least one replica. Rebuilds do not replace Postgres backups; see [Fabriq](fabriq.md) for recovery and library-mode setup.
