# Running Loomplane

What it takes to keep a Loomplane running: backups, restores, upgrades, and knowing when something goes wrong. The commands here are `loomplane` commands, run with the same configuration the server runs with.

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

| Metric | Labels | What it counts |
|---|---|---|
| `deployments_total`, `deployment_duration_seconds` | `status` | Deployments that finished, and how long they took |
| `deployment_steps_total` | `provider`, `op`, `status` | Each action of a deployment |
| `deployment_step_duration_seconds` | `provider`, `status` | How long each provider takes |
| `plans_total` | `status` | Plans worked out: ready, blocked or failed |
| `plan_decisions_total` | `decision` | Approved, rejected, stale or expired |
| `drift_findings_total` | `provider`, `state` | Bindings found missing or changed |
| `analyses_total`, `analysis_duration_seconds` | `status` | Repository analyses, from loomplane-worker |
| `api_key_authentications_total` | `result` | Requests with an API key, accepted or rejected |
| `connection_checks_total` | `cloud`, `result` | Credential checks: ready, incomplete, rejected or unavailable |

Worth an alert: `deployment_steps_total{status="failed"}` rising for one provider, which usually means its connection lost a permission; `api_key_authentications_total{result="rejected"}` climbing, which is a revoked key still in a pipeline or someone guessing; and `drift_findings_total`, which means something changed in a cloud outside Loomplane.

`/_/health` reports each part, provider plugins included: a plugin that stops answering makes it unhealthy within one check interval. `/_/health/live` and `/_/health/ready` are for your orchestrator's probes.

## Pausing a target

To stop Loomplane deploying to a target, during an incident on a cloud for instance, name it in `providers.paused`:

```yaml
providers:
  paused: [render, cloud-run]
```

or set `LOOMPLANE_PAUSED_PROVIDERS=render,cloud-run`. Plans that use a paused target are still made, and can be reviewed and approved, but they say they're for review only, and approving one deploys nothing. What already runs there keeps running, and drift checks still read it. A name that isn't a target stops Loomplane from starting, so a typo can't leave a target running that you meant to pause. Restart Loomplane to pause or unpause one.
