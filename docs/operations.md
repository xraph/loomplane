# Running Loomplane

What it takes to keep a Loomplane running: backups, restores and upgrades. Everything here is a `loomplane` command, run with the same configuration the server runs with.

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
