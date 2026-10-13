# loomplane.yaml

`loomplane.yaml` says what your application deploys, where, and what it needs. Loomplane drafts one from a project's analysis; you review it, edit it in the dashboard or with `loomctl manifest save`, and every save is a new revision. A plan names the revision it was made from.

Nothing in it is Forge, or Go. Point a YAML language server at `pkg/manifest/schema.json` (its `$id` is `https://loomplane.dev/schemas/loomplane.v1.json`) and you get completion and checks as you type.

```yaml
version: 1

defaults:
  provider: cloud-run
  region: us-central1

services:
  api:
    source: go:services/api
    needs: [db, cache]
    scale: {min: 1, max: 5}
    size: {cpu: 1, memory: 512}
  worker:
    source: services/worker/cmd/worker
    provider: ecs-fargate
    needs: [api]
  web:
    source: apps/web
    provider: vercel
    needs: [api]
    environments:
      preview:
        enabled: false

resources:
  db:
    kind: postgres
    provider: cloud-sql
    version: "17"
  cache:
    kind: redis
    provider: memorystore

gateways:
  edge:
    routes:
      - {path: /api, to: api, strip_prefix: true}
      - {path: /, to: web}

ignore:
  resources: [kafka]
```

## version

Always `1`. Loomplane refuses a manifest of another version, so a newer format can never be half read.

## defaults

`provider` and `region` for every service, resource and gateway that doesn't set its own. A default region only comes with the default provider: a service that names another provider doesn't take the default region along.

## services

What the application runs, by name. Names are lower case letters, digits and hyphens, starting with a letter, at most 40 long, and they're how services, resources and gateways refer to each other.

| Field | What it does |
|---|---|
| `source` | Where the service's code is, as analysis found it: a Go main package's directory, a Rust binary's source file, a Node package's, Python project's or JVM module's directory, or a Dockerfile. A Python project's Procfile process other than `web` is the project's directory, `/Procfile/` and its name: `shop/Procfile/worker`. When two ecosystems have a service at one path, prefix it: `go:services/api`, `python:services/api`. Required. |
| `kind` | Corrects what analysis decided the service is: `http-service`, `web-app`, `static-site` or `worker`. |
| `build` | `{context}`: the directory its Dockerfile builds from, from the repository's root (`.` is the root). Without it, the context a Compose file gives the Dockerfile, or the Dockerfile's own directory. |
| `provider`, `region` | Where it runs. |
| `port` | The port it listens on. Without it, the port in its Dockerfile, or 8080 with a warning. |
| `public` | Whether it gets an address on the internet. Yes for anything that serves HTTP, no for a worker, unless you say. |
| `scale` | `{min, max}` instances, on targets that let you choose. |
| `size` | `{cpu, memory}`: virtual CPUs, such as 0.5 or 2, and MiB. |
| `needs` | The resources and services it talks to. Each one it names gets it an address in a variable, and it deploys after them. |
| `enabled` | `false` leaves it out. |
| `environments` | Overrides by environment name: any of the fields above but `source`, `kind`, `build` and `needs`. |

A Dockerfile that copies from across the repository, such as a Go service that needs the module's `go.mod` at the root, has to build from the root:

```yaml
services:
  api:
    source: services/api
    build: {context: .}
```

Loomplane passes the Dockerfile to the build by its path from the context, so it can sit anywhere. A service without a Dockerfile is built from its source, and a `build` on it is ignored with a warning.

A service gets a variable for each thing it `needs`. A resource's is named for its kind: `DATABASE_URL` for Postgres and MySQL, `REDIS_URL`, `MEMCACHED_SERVERS`, `MONGODB_URI`, `AMQP_URL`, `KAFKA_BROKERS`, `NATS_URL`, `STORAGE_URL`, `SEARCH_URL`, `CLICKHOUSE_URL`. Another service's is its name in capitals and `_URL`: `API_URL`. When two needs would share a name, each gets its own name's instead (`ANALYTICS_URL`). A variable you set yourself wins, so you can point a service somewhere else.

Resource addresses hold credentials, so they're secrets: they reach the service through its cloud's secret store, never as plain variables.

How a service reaches another depends on where both run. Two services on ECS in the same environment reach each other inside the VPC, at `http://name:port`, whether or not the one they need is public. A private Cloud Run service only takes calls from the project's own services, which have to send a Google ID token. A private service needed from another cloud is a problem on the plan: nothing there could reach it.

## resources

Backing services the application needs, by name. A resource can't share a service's name.

| Field | What it does |
|---|---|
| `kind` | `postgres`, `mysql`, `redis` and so on, the names analysis uses. Required. |
| `provider`, `region` | Where it's provisioned. |
| `version` | The engine's version, such as `"17"` for Postgres. Without it, the provider's newest. |
| `environments` | `provider`, `region` or `enabled` by environment name. |

A managed resource is only reachable from services on its own cloud. A Vercel app that needs a Cloud SQL database is a problem on the plan.

## gateways

One public address in front of several services, routing each request by its path. Loomplane builds each gateway on Bastion and runs it on Cloud Run or ECS like a service; your traffic never passes through Loomplane.

| Field | What it does |
|---|---|
| `provider`, `region` | Where it runs: `cloud-run` or `ecs-fargate`. |
| `routes` | `{path, to, strip_prefix}`: a path prefix such as `/api`, which matches `/api` and anything under `/api/`; the service it goes to; and whether to remove the prefix first, so `/api/users` arrives as `/users`. The longest matching path wins, so `/` catches the rest. |
| `environments` | `provider`, `region` or `enabled` by environment name. |

A gateway gets the addresses it routes to and none of the project's variables. On ECS it reaches private services inside the environment; elsewhere a route to a private service is a problem, since a gateway can't present Cloud Run's ID token.

## ignore

`resources` lists kinds analysis found that the application doesn't use, so plans stop asking about them.

## Targets

| Provider | Cloud | Runs | Provisions |
|---|---|---|---|
| `vercel` | Vercel | web apps, static sites | |
| `cloud-run` | Google Cloud | HTTP services, web apps | |
| `cloud-sql` | Google Cloud | | Postgres 17, 16, 15; MySQL 8.4, 8.0 |
| `memorystore` | Google Cloud | | Redis |
| `cloud-storage` | Google Cloud | | Object storage |
| `ecs-fargate` | AWS | HTTP services, web apps, workers | |
| `rds` | AWS | | Postgres 17, 16, 15; MySQL 8.4, 8.0 |
| `elasticache` | AWS | | Redis |
| `s3` | AWS | | Object storage |
| `render` | Render | HTTP services, web apps, workers, static sites | Postgres 17, 16, 15; Key Value (Redis) |

Buckets are private on both clouds, and services reach them as their own identity, so `STORAGE_URL` carries no credentials: `s3://bucket?region=...` for an ECS task's environment role, `gs://bucket` for Cloud Run's runtime account. Redis gets an auth token in `REDIS_URL`: `rediss://...` on ElastiCache, which also encrypts in transit and at rest, and `redis://...` on Memorystore, which only has an address on the project's default network, so Cloud Run services that need it send their private traffic there.

On AWS, the region is the connection's. An AWS connection names one, and everything Loomplane runs or provisions with it goes there, since ECS tasks reach RDS and ElastiCache over that region's network. Leave `region` out and you get the connection's. Name another and the plan stops on it, because the deployment would land in the connection's region anyway. To move an environment to another AWS region, choose a connection in that region for it. On Google Cloud you pick the region per service or resource, and a move starts the new one first: Cloud SQL instances and Cloud Storage buckets get names with their region in them, so the new one never collides with the one it replaces.

Render builds each service itself, from your repository through its own GitHub connection, so give Render access to the repository in its dashboard before the first deployment. Loomplane creates each service with automatic deploys off, cancels the deploy Render starts on its own when a service is made, and deploys the commit the plan approved. A service with a Dockerfile builds from it; otherwise Render's native runtime builds it, which it has for Go, Node and Python. A Node service starts with `npm start`, so it needs a start script, and a Rust or JVM service needs a Dockerfile. A private service is reached on Render's private network by the other services on Render, at `http://` its name there and its port. Postgres and Key Value are reachable from services on Render only.

Provider plugins add targets of their own ([providers.md](providers.md)). An operator can pause any target with `providers.paused` in the configuration ([operations.md](operations.md#pausing-a-target)): plans that use it are then for review only.

## Environments

Every project has `production` and `preview`, and you can add others. A plan is for one environment, and each field's environment override applies there. `enabled: false` in an environment leaves a service, resource or gateway out of it, and removes it if it was deployed.
