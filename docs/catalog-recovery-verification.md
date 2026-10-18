# Catalog and recovery verification

Verified on 18 October 2026. You can deploy PostgreSQL 17 on a checked Docker server through Services or `loomctl`, then create native logical backups and test them in isolated copies. See the [PostgreSQL guide](postgres-services.md) for commands and configuration.

## CLI acceptance

`TestAcceptanceCatalogCLIRecovery` passed against a real PostgreSQL control-plane database and Docker daemon. It runs the CLI against the real API and durable worker:

- Create a repository-free catalog project and approve its plan.
- Insert a SQL row, create a backup with `--wait --json`, and repeat its request ID to confirm the same recovery point is returned.
- Remove the source row and restore the backup. The copy contains the row; the source stays empty.
- Read backup evidence with a read-only API key. Backup creation, restore creation and policy updates return forbidden errors for that key.
- Save a six-hour policy, list its persisted cadence, and disable it.
- Delete the temporary copy through the CLI. Its volume is gone and its verification record remains readable.

The timeout regression rejects zero and negative durations before any API request for backup creation, retry and inspection, restore creation and inspection, and catalog project creation. A real Docker/PostgreSQL repeat rejected zero-duration backup/restore requests without creating records, then used a one-nanosecond wait to force both accepted operations to time out locally. Their JSON IDs remained available; `show --wait` followed the same jobs through successful backup and SQL-verified restore. The live source stayed unchanged and copy cleanup passed.

The timeout fix passed CLI/client lint and tests. Full-repository reruns at this checkpoint were blocked by concurrent Cloud target contracts and notification UI/extension work. Those files are outside this fix.

Run it with your test database and Docker socket:

```sh
LOOMPLANE_TEST_DATABASE_URL=postgres://... \
LOOMPLANE_TEST_DOCKER_SOCKET=/path/to/docker.sock \
go test -tags=integration -run '^TestAcceptanceCatalogCLIRecovery$' -count=1 -v ./tests/integration
```

The SSH transport in this test forwards to the real local Docker daemon. Remote-host SSH acceptance is separate. Cleanup is limited to the new test project's containers, volumes and networks.

## Browser acceptance

The live Services page created a backup, enabled a six-hour schedule, paused it without changing the cadence, and restored the new recovery point. Direct SQL confirmed the restored row and unchanged source. The copy used an internal network with no published ports.

The browser displayed SQL verification, status and the cleanup deadline. The removal confirmation opened and cancelled correctly at 320 px. Actual removal is covered by CLI and API acceptance, plus the dashboard request test.

Desktop, 390 px and 320 px layouts were inspected. Both narrow document widths matched the viewport without horizontal overflow. The test server and its Docker resources were removed afterwards; the user's original browser route was restored.

Regression tests use the API's object-shaped binding configuration. They cover database/environment scope, recovery history after source removal, saved schedule cadence and explicit copy cleanup confirmation.

## Validation

Formatting uses `golangci-lint fmt` on owned Go files and `pnpm --dir apps/dashboard format`. This repository has no `make f` or `make l` aliases. Lint uses `make lint`, with `--allow-serial-runners` on its Go command when another run holds the linter lock.

The Go unit suite and `make test-integration` passed, with the database integration suite completing in 310 seconds. All 240 dashboard tests passed on rerun after the existing deployment-dialog test exceeded its five-second timeout during a concurrent build. Dashboard typecheck/lint, generated-client drift checks and the complete app/CLI build passed. CLI and client lint/tests passed.

Full-repository lint encountered concurrent changes in `internal/agent`, Kubernetes persistence and its integration test. Those files are outside this change. Their diagnostics do not appear in the owned CLI, client or recovery UI files.

## Remaining qualification

This completes the current Docker PostgreSQL UI and CLI profile. Other catalog recipes, Kubernetes catalog support, production AWS S3 acceptance, PITR, retention, upgrades, credential rotation and live binding cutover remain separate work. Local Docker volumes cannot recover from host loss without an off-host backup.
