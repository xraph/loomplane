# Fabriq in a project

Open **Services**, choose **Fabriq**, then pick your project, environment and checked target. Choose Hosted server if your apps connect through the Fabriq API, or Library only if your Go app embeds Fabriq. Review the plan before deploying it.

You can run either mode on a connected Docker server or Kubernetes cluster. The stack provisions PostgreSQL 16 with TimescaleDB, pgvector and PostGIS, plus Redis. Migration jobs check the database, apply the Fabriq schema and create the restricted `fabriq_app` role before the server or worker starts. Images are pinned for amd64 and arm64. The server and admin console stay private.

FalkorDB and Elasticsearch are optional. Switch them on in Settings, review the resulting plan, then deploy. Their graph and search projections can be rebuilt from Postgres on the Engines tab. A rebuild shows its state and output; a retryable failure offers a retry. You need permission to create deployments to request one.

## Connect an engine you already run

Choose **Connect existing** for Postgres or Redis. You can select a compatible resource in the project on the same target, or check and save an external connection. Connection strings are secrets.

An external Postgres must be version 16 or newer, have the `timescaledb`, `vector` and `postgis` extensions installed, preload TimescaleDB and allow the connecting role to create roles. Fabriq uses that connection for migrations and creates its own application role. Redis must be version 7 or newer; TLS Redis endpoints are not supported by this profile yet. The connection check runs from the selected target.

External engines remain yours to operate. Loomplane does not back them up or restore them. A project resource keeps its own recovery controls.

## Connect a service to hosted Fabriq

Link the service on the Tenants and keys tab, or add the need to your manifest:

```yaml
services:
  api:
    source: cmd/api
    needs: [fabriq]
    need_options:
      fabriq:
        tenant: acme
```

Keep your service's existing provider and target settings. On its next release, Loomplane creates the tenant if needed and injects `FABRIQ_URL`, a `fabriq://` DSN containing that service's key, the private server address and its default tenant. Keys stay in Vault and are not shown in the panel.

Each service gets a separate key. The tenant in the DSN is a default, not a restriction on which tenants that key may access: your app can choose another tenant per request, using `client.WithTenant(id)` in the Go client. Fabriq applies row-level security for the selected tenant. Service keys cannot manage other keys.

**Rotate** creates a pending replacement. The old key keeps working until the service's next release installs the replacement and writes its binding. **Revoke** cuts access immediately. A later release issues a new key if the service still needs Fabriq, so remove that need when you want access to stay revoked. Rotation and revocation require permission to manage secrets.

Use **Open Fabriq admin** to open a short-lived console session through Loomplane. You need infrastructure modification permission. The console uses Loomplane's operator key internally; that key is never sent to your app or exposed to the browser.

## Recovery

On Docker, the Recovery tab opens the existing backup and isolated restore controls for the stack's Postgres and Redis resources. You can configure a backup schedule and verify a restore into a separate copy. TimescaleDB restores preserve extension-owned objects, hypertables and application ownership.

These engines do not have catalog backups on Kubernetes yet. The panel says so. FalkorDB and Elasticsearch use projection rebuilds instead of separate backups. The Redis profile persists AOF every second and caps memory at 128 MiB with `noeviction`; writes fail when it runs out of space, so watch capacity before relying on it for a large outbox.

## Embed Fabriq in Go

Choose **Library only** and add the resource to your service's `needs`. You do not set a tenant option on this link. Loomplane supplies:

| Engine                      | Variables                                                                                      |
| --------------------------- | ---------------------------------------------------------------------------------------------- |
| Postgres                    | `FABRIQ_POSTGRES_DSN`, using `fabriq_app`                                                      |
| Redis                       | `FABRIQ_REDIS_ADDR`, `FABRIQ_REDIS_USERNAME`, `FABRIQ_REDIS_PASSWORD`                          |
| FalkorDB, when enabled      | `FABRIQ_FALKORDB_ADDR`, `FABRIQ_FALKORDB_USERNAME`, `FABRIQ_FALKORDB_PASSWORD`                 |
| Elasticsearch, when enabled | `FABRIQ_ELASTICSEARCH_ADDRS`, `FABRIQ_ELASTICSEARCH_USERNAME`, `FABRIQ_ELASTICSEARCH_PASSWORD` |

Load these into your Fabriq configuration and enable `fabriq.DynamicConfig{Persist: true}` when defining dynamic entities. The stock worker reloads persisted definitions every 15 seconds. It does not contain your Go entity structs, validation hooks or embedding code. Those need your own worker binary built with `forgeext.WithWorker(true)`.

Library mode has no hosted admin console or service-key controls. Your application owns its tenant and request authorization.
