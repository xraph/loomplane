# RabbitMQ services

Create a RabbitMQ service from the catalog, select a Docker server target, then approve its plan. You get a private AMQP endpoint through `AMQP_URL` and a retained volume mounted at `/var/lib/rabbitmq`. The recipe pins RabbitMQ 4.3.6 for amd64 and arm64. Kubernetes and managed cloud targets are not supported.

Publish persistent messages to durable classic or quorum queues and wait for publisher confirms. Removing the service retains its volume. Deleting a recovery copy removes that copy's volume.

## Message backups

Plan for an outage. A backup suspends listeners, closes client connections, records the queue inventory, and stops the RabbitMQ application before copying its message store. The broker starts again before the archive is uploaded. Your clients must reconnect, and consumers must handle redelivery of messages that were not acknowledged before the backup.

You can create a backup in the service's recovery panel or with the CLI:

```sh
loomctl backups create PROJECT_ID --request-id rabbit-backup-1 --wait
loomctl restores create BACKUP_ID --request-id rabbit-copy-1 --wait
loomctl restores delete RESTORE_ID --yes
```

The archive contains the node's data directory, including definitions, queued messages and password hashes. Restrict access to your backup storage. A definitions export alone does not contain messages; see [RabbitMQ's backup documentation](https://www.rabbitmq.com/docs/backup).

This recipe supports one node named `rabbit@localhost`, the default `/` virtual host, and the generated `admin` and `app` users. Backups reject transient messages, non-durable or auto-delete queues, streams, policies, runtime parameters, and queue arguments other than `x-queue-type`. The limits are 100 queues, 1 GiB of data directory storage, and a 2 GiB archive. Allow at least 3 GiB of free temporary storage on the Docker host for staging and the archive. Backups that fall outside this profile fail without publishing an artifact.

## Isolated recovery

A restore creates a separate container and volume using the same pinned recipe. You can restore after the source service has been removed. The restore checks the archive checksum, validates its layout and profile before changing the destination, and compares restored queue types, arguments and message counts with the saved inventory.

The copy listens only on container loopback, has no published ports, and uses an internal Docker network without external egress. Both passwords change during recovery. Its application user can authenticate but cannot configure queues, publish, or consume; inspecting messages requires administrative access inside the copy. A verified restore is a recovery check, not a traffic switch or an application endpoint.

There is no automatic promotion, clustering, or point-in-time recovery. You must keep the pinned version and node identity when recovering this physical archive.

Per-message expiration still applies after recovery. If messages expire between backup and verification, the queue counts differ and the restore fails verification. We cannot distinguish expiration from missing messages by inspecting the archive inventory, so you should use this recovery profile for messages without expiration. A failed count check never marks the copy verified.
