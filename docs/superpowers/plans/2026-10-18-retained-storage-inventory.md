# Retained storage inventory

Continue phases 0 and 1 of the approved service plan. You need to find retained data after a workload is removed, before any destructive lifecycle control can be offered.

1. Add read-only Docker volume and Kubernetes PVC inventory adapters. Verify the checked daemon or namespace identity, scope every record by organization and connection, and inspect all container/pod references before reporting a volume as detached. Report unknown Docker capacity honestly.
2. Expose the inventory through the existing connection service and authorization. Require infrastructure/account read permission and filter records by project access before returning safe metadata. Do not expose host paths or credentials.
3. Add generated Forge API bindings, `loomctl storage <connection-id>`, and a compact Infrastructure view with loading, error, retry and ZeroState behavior. Use the same endpoint and model across clients.
4. Verify native adapters, API authorization, CLI and dashboard behavior with TDD. Run a real Docker retained-volume lifecycle and inspect desktop/mobile layouts. Run format, lint, tests, integration and build checks before focused commits and pushes.

Inventory is read-only. Detached storage is not a backup or evidence that deletion is safe. Explicit destruction and recovery cutover retain their separate design and qualification requirements.
