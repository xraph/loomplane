# Persistent storage inventory

Open **Infrastructure**, then **Persistent storage**. Choose a Docker server or Kubernetes connection and select **Refresh storage** to read its current volumes and workload references. You can inspect the same inventory from the CLI:

```sh
loomctl storage CONNECTION_ID
loomctl storage CONNECTION_ID --json
```

The table shows the volume name, owning project and environment, role, storage class and status. Docker local volumes show **Shared host capacity**. Kubernetes shows the claim's reported capacity, which is not a measurement of used disk space.

| Status   | Meaning                                                                                           |
| -------- | ------------------------------------------------------------------------------------------------- |
| In use   | A container or pod still references the volume. Stopped containers and completed pods count.      |
| Retained | The volume was found without a current workload reference. A Kubernetes claim must also be bound. |
| Pending  | Kubernetes has not bound the claim. A pending pod reference does not override this state.         |
| Lost     | Kubernetes reports the claim as lost.                                                             |
| Deleting | Kubernetes has accepted deletion of the claim.                                                    |

This is a read-only snapshot. Detached storage is not a backup or proof that removal is safe. The inventory does not delete data, reattach volumes, measure disk usage or qualify recovery. Those actions need their own ownership and recovery checks.

## Scope and permissions

`GET /api/v1/connections/:id/storage` uses the checked connection's credential internally. It requires `infrastructure:read` and `cloud_account:read`, then filters every record through `project:read`. A read-only API key can inspect permitted storage. Another organization's connection returns not found. Authorization outages and unreadable target state produce errors rather than empty inventories.

Each record must have matching organization, connection, project, environment, instance and volume-role metadata. The name must match the instance's stable volume identity. Docker daemon and Kubernetes namespace identities are checked again when reading. Host storage paths, credentials and other tenants' workload metadata are never returned.

Docker inventory checks all containers, including stopped and unlabelled consumers. Kubernetes checks every pod in the selected namespace. These references are conservative: an old workload object can keep a volume marked in use until that object is removed. The adapters use the [Docker Engine API](https://docs.docker.com/reference/api/engine/version/v1.53/) and [Kubernetes claim phases](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#phase).

## Verification, 18 October 2026

The real Docker API/CLI test created owned volumes, wrote data, detected a stopped container's mount, removed that container, and reported the retained volume. Reattachment read the original contents. The same test checked a read-only API key, rejected a cross-organization request, and removed only its own test resources.

The Kubernetes adapter ran against a disposable arm64 kind cluster with `rancher.io/local-path` storage. Inventory reported the same 1 GiB claim as in use, retained after workload removal, and in use after reattachment. A PostgreSQL row survived pod replacement, rollout, removal and reattachment. The cluster, namespace, class and temporary image tag were removed afterwards. This qualifies that storage combination; production CSI and zone-loss behavior remain unverified.

Regression tests cover wrong daemon/namespace identity, foreign ownership, project access, authorization failure, pending claims referenced by pods, CLI JSON output, and UI denied/empty/retry behavior. The live Docker dashboard was checked at desktop, 390 px and 320 px widths without horizontal overflow. Its target selector and refresh action worked, and the user's original browser route was restored.

Dashboard typecheck (`tsc -b` and `tsc --noEmit`), lint, all 243 UI tests, generated-client drift checks, the database integration suite and the complete app/CLI build passed. Targeted inventory Go tests passed. Full-repository reruns encountered concurrent native Kubernetes recovery changes outside this slice, first unfinished tests/formatting and then missing module checksums for `k8s.io/streaming` and `github.com/gorilla/websocket`. The inventory files have no reported lint diagnostics. Formatting uses `golangci-lint fmt` on owned Go files and `pnpm --dir apps/dashboard format`; this repository has no `make f` or `make l` aliases.

Managed-provider snapshot inventory, deliberate data destruction, storage expansion and capacity alerts remain separate work.
