# Acme, a polyglot monorepo

A sample repository for Loomplane's repository discovery. Nothing here is
meant to build: each file holds just enough for a detector to find, and the
tests in `internal/discovery` and `internal/graph` check what they find.

| Path | What it is |
|---|---|
| `services/api` | Go HTTP API, built by its Dockerfile, using `libs/go/store` |
| `services/worker` | Go queue worker on Redis |
| `libs/go/store` | Go library over Postgres |
| `crates/ingest` | Rust axum server, plus a `backfill` binary |
| `crates/core` | Rust library |
| `apps/web` | Next.js app on Postgres through Prisma |
| `apps/docs` | Vite single-page app |
| `apps/gateway` | Fastify server on Redis |
| `packages/ui` | React component library |
| `docker-compose.yml` | Postgres and Redis for local development |
