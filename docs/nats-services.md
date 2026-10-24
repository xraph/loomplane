# NATS JetStream services

You can deploy NATS JetStream from Services or with `loomctl services create "Events" --kind nats --target <server-connection>`. Review and approve the plan before it runs. The service publishes a `NATS_URL` binding for applications in its environment.

The current recipe runs NATS 2.11.17 on a Docker server, with separate application and administrative credentials and an architecture-specific image digest. JetStream writes to a retained `/data` volume. Restarting the container preserves stream data and consumer acknowledgements. Removing the service removes compute and retains that volume.

## Back up and test recovery

Open the service's Recovery panel, or use the existing CLI workflow:

```sh
loomctl backups create <project-id> --request-id <unique-request-id> --wait
loomctl restores create <backup-id> --request-id <unique-request-id> --wait
loomctl restores show <restore-id>
loomctl restores delete <restore-id> --yes
```

You can also configure a backup policy with `loomctl backups policy <project-id> --resource database --every-hours 6`. Backup and restore mutations require write access. Read-only access lets you inspect their status.

A backup contains native JetStream snapshots, stream configuration and consumer state, including acknowledgement positions. The worker checks native snapshot integrity and stores the archive through the configured backup store. It records the archive size and SHA-256 digest, then verifies both before opening a restore connection.

Recovery creates a separate service with an empty volume and fresh credentials. It uses an internal Docker network with no published port and no extra network attachment. The worker restores each stream and verifies message counts, sequence bounds, consumer counts and application access. The source stays untouched, and you can restore a saved backup after removing the source service.

The recovery application account can inspect stream metadata and stored messages. It cannot publish, consume, acknowledge or change streams. Restored consumer delivery state belongs to the isolated copy. Push consumers are paused for the lifetime of the recovery copy, so inspection subscriptions cannot trigger delivery or remove messages under acknowledgement-free retention. Pull consumer acknowledgement positions remain intact. Recovery does not promote that copy or redirect applications. Delete the copy when you finish reviewing it; cleanup removes its temporary volume. The shared recovery workflow also expires copies after their recorded deadline.

## Supported profile and limits

This recipe is a single-server service. Kubernetes, clustering, cross-server replication, automatic failover, point-in-time recovery and production cutover are outside this profile.

Use file-backed streams with one replica. Native backups accept up to 100 streams with names containing letters, digits, underscores or hyphens, at most 128 characters. Each stream may have up to 1,000 consumers. Mirrors, stream sources and placement rules are rejected because their dependencies cannot be recovered inside this isolated profile. Memory-backed streams are not backed up.

The recipe caps JetStream file storage at 1 GB. Backups are limited to 2 GiB including their archive. The worker stages native snapshots in private temporary files before upload and downloads a complete archive before restore, so allow at least 2 GiB of free scratch space per active operation. Temporary files are removed when the operation ends.

Snapshots are taken one stream at a time. They do not form an atomic checkpoint across streams or applications. Pause publishers and consumers when your recovery point needs a coordinated business boundary. Keep time-based retention in mind: messages that expire during recovery can make verification fail. Server credentials and server configuration are recreated by the recipe and are not imported from the snapshot.

## Verification

The integration drill uses the real API, CLI, PostgreSQL control plane and pinned NATS container through the SSH/Docker fixture. It checks persistence after restart, message and consumer acknowledgement recovery, authorization denial, fresh credentials, inspection-only access, network isolation, copy cleanup, retained source storage and restoration after source removal. Archive tests reject corrupt, oversized or malformed input before connecting to a destination.

These checks qualify the single-server recipe. They do not establish clustering, Kubernetes support or operation on an external production server.
