# Catalog and recovery completion

Goal: finish the existing PostgreSQL catalog, backup and isolated-restore workflows through the dashboard and loomctl.

Use the current generated clients, scoped recovery APIs, deployment plan approval and shared product components. Preserve concurrent work on main. Other database recipes, Kubernetes persistence, retention and PITR keep their existing qualification gates.

1. Complete CLI recovery controls with regression tests for machine-readable output, waiting for terminal backup/restore results and reading saved schedules. Reuse the client polling contract and existing recovery endpoints.
2. Complete dashboard recovery behavior with tests for environment/resource scope, saved schedule cadence, error recovery and isolated-copy cleanup. Use the existing catalog page and shared components after its concurrent implementation is committed.
3. Verify the real Docker PostgreSQL flow through CLI and browser: deployment, backup, isolated restore, schedule persistence, denied access and copy removal. Inspect desktop and phone layouts. Remove only disposable test resources.
4. Run configured formatting, lint, unit tests, relevant database integration tests, generated-client checks and builds. Commit and push only owned changes and record any remaining verification limits.
