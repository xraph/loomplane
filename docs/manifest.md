# loomplane.yaml

`loomplane.yaml` says what your application deploys, where, and what it needs. Loomplane drafts one from a project's analysis; you review it, edit it in the dashboard or with `loomctl manifest save`, and every save is a new revision. A plan names the revision it was made from.

Nothing in it is Forge, or Go. Point a YAML language server at `pkg/manifest/schema.json` (its `$id` is `https://loomplane.dev/schemas/loomplane.json`) and you get completion and checks as you type.

## Choose managed deployment targets

Open your project's Deploy tab and save the drafted manifest. The Deployment targets panel lets you select a provider, region and enabled status for each service, resource and gateway in the selected environment. Only available providers that support the workload or resource are offered. Vercel runs web apps and static sites; workers need a target such as ECS on Fargate or Render.

Save each placement. These edits create manifest revisions and preserve your YAML comments, source paths, dependencies and other environment settings. Changing provider clears the environment's region override. You can leave the region empty to inherit the provider's placement rules, or enter a region and review any warnings in the plan. Expand Edit loomplane.yaml for settings outside the placement controls.

Choose the cloud accounts below the placements, or in the New deployment dialog. Each environment uses one connection per cloud, shared by its services and resources. With one ready account, Loomplane selects it automatically. With several, you choose. Account choices are saved on the server and used by dashboard, CLI and API plans.

Create a plan. Review its destinations, changes and findings before approving it. Saving a placement or selecting an account doesn't provision anything; approval starts the deployment. Turning an existing service off can produce a deletion in the next plan, so review that change too. Existing servers and Kubernetes clusters need a compatible provider plugin.

```yaml
version: 1

defaults:
  provider: cloud-run
  region: us-central1

services:
  api:
    source: go:services/api
    needs: [db, cache]
    scale: { min: 1, max: 5 }
    size: { cpu: 1, memory: 512 }
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
      - { path: /api, to: api, strip_prefix: true }
      - { path: /, to: web }

ignore:
  resources: [kafka]
```

## version

Existing workflows use `1`. Version `2` adds the optional federation intent described below. Loomplane rejects unsupported versions and unknown fields; federation plans remain blocked until their runtime gates are qualified.

## defaults

`provider` and `region` for every service, resource and gateway that doesn't set its own. A default region only comes with the default provider: a service that names another provider doesn't take the default region along.

## services

What the application runs, by name. Names are lower case letters, digits and hyphens, starting with a letter, at most 40 long, and they're how services, resources and gateways refer to each other.

| Field                | What it does                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `source`             | Where the service's code is, as analysis found it: a Go main package's directory, a Rust binary's source file, a Node package's, Python project's or JVM module's directory, or a Dockerfile. A Python project's Procfile process other than `web` is the project's directory, `/Procfile/` and its name: `shop/Procfile/worker`. When two ecosystems have a service at one path, prefix it: `go:services/api`, `python:services/api`. Required. |
| `kind`               | Corrects what analysis decided the service is: `http-service`, `web-app`, `static-site` or `worker`.                                                                                                                                                                                                                                                                                                                                             |
| `build`              | `{context}`: the directory its Dockerfile builds from, from the repository's root (`.` is the root). Without it, the context a Compose file gives the Dockerfile, or the Dockerfile's own directory.                                                                                                                                                                                                                                             |
| `provider`, `region` | Where it runs.                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| `port`               | The port it listens on. Without it, the port in its Dockerfile, or 8080 with a warning.                                                                                                                                                                                                                                                                                                                                                          |
| `public`             | Whether it gets an address on the internet. Yes for anything that serves HTTP, no for a worker, unless you say.                                                                                                                                                                                                                                                                                                                                  |
| `scale`              | `{min, max}` instances, on targets that let you choose.                                                                                                                                                                                                                                                                                                                                                                                          |
| `size`               | `{cpu, memory}`: virtual CPUs, such as 0.5 or 2, and MiB.                                                                                                                                                                                                                                                                                                                                                                                        |
| `needs`              | The resources and services it talks to. Each one it names gets it an address in a variable, and it deploys after them.                                                                                                                                                                                                                                                                                                                           |
| `enabled`            | `false` leaves it out.                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `environments`       | Overrides by environment name: any of the fields above but `source`, `kind`, `build` and `needs`.                                                                                                                                                                                                                                                                                                                                                |

A Dockerfile that copies from across the repository, such as a Go service that needs the module's `go.mod` at the root, has to build from the root:

```yaml
services:
  api:
    source: services/api
    build: { context: . }
```

Loomplane passes the Dockerfile to the build by its path from the context, so it can sit anywhere. A service without a Dockerfile is built from its source, and a `build` on it is ignored with a warning.

A service gets a variable for each thing it `needs`. A resource's is named for its kind: `DATABASE_URL` for Postgres and MySQL, `REDIS_URL`, `MEMCACHED_SERVERS`, `MONGODB_URI`, `AMQP_URL`, `KAFKA_BROKERS`, `NATS_URL`, `STORAGE_URL`, `SEARCH_URL`, `CLICKHOUSE_URL`. Another service's is its name in capitals and `_URL`: `API_URL`. When two needs would share a name, each gets its own name's instead (`ANALYTICS_URL`). A variable you set yourself wins, so you can point a service somewhere else.

Resource addresses hold credentials, so they're secrets: they reach the service through its cloud's secret store, never as plain variables.

How a service reaches another depends on where both run. Two services on ECS in the same environment reach each other inside the VPC, at `http://name:port`, whether or not the one they need is public. A private Cloud Run service only takes calls from the project's own services, which have to send a Google ID token. A private service needed from another cloud is a problem on the plan, unless a [link](#links) joins the two clouds' networks and the caller is on Google Cloud.

## resources

Backing services the application needs, by name. A resource can't share a service's name.

| Field                | What it does                                                                          |
| -------------------- | ------------------------------------------------------------------------------------- |
| `kind`               | `postgres`, `mysql`, `redis` and so on, the names analysis uses. Required.            |
| `provider`, `region` | Where it's provisioned.                                                               |
| `version`            | The engine's version, such as `"17"` for Postgres. Without it, the provider's newest. |
| `environments`       | `provider`, `region` or `enabled` by environment name.                                |

A managed resource is only reachable from services on its own cloud. A Vercel app that needs a Cloud SQL database is a problem on the plan.

## gateways

One public address in front of several services, routing each request by its path. Loomplane builds each gateway on Bastion and runs it on Cloud Run or ECS like a service; your traffic never passes through Loomplane.

| Field                | What it does                                                                                                                                                                                                                                                             |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `provider`, `region` | Where it runs: `cloud-run` or `ecs-fargate`.                                                                                                                                                                                                                             |
| `routes`             | `{path, to, strip_prefix}`: a path prefix such as `/api`, which matches `/api` and anything under `/api/`; the service it goes to; and whether to remove the prefix first, so `/api/users` arrives as `/users`. The longest matching path wins, so `/` catches the rest. |
| `environments`       | `provider`, `region` or `enabled` by environment name.                                                                                                                                                                                                                   |

A gateway gets the addresses it routes to and none of the project's variables. On ECS it reaches private services inside the environment; elsewhere a route to a private service is a problem, since a gateway can't present Cloud Run's ID token.

## links

A link joins an environment's Google Cloud network to its AWS VPC, so a Cloud Run service can need a private ECS service, an RDS database or an ElastiCache cache:

```yaml
links:
  vpn:
    between: [gcp, aws]
    region: us-central1
```

| Field          | What it does                                                                           |
| -------------- | -------------------------------------------------------------------------------------- |
| `between`      | The two clouds it joins: `gcp` and `aws`. Required.                                    |
| `region`       | Where the Google Cloud side runs. Put it where the Cloud Run services that use it run. |
| `environments` | `region` or `enabled` by environment name.                                             |

Each environment gets its own link: an HA VPN gateway and a Cloud Router on the project's default network, a virtual private gateway on the account's default VPC, and four IPsec tunnels between them, each carrying a BGP session, so losing an endpoint on either side keeps the link up. The default network (`10.128.0.0/9`) and VPC (`172.31.0.0/16`) don't overlap, so their routes need no translating. Both clouds bill a VPN by the hour, whether or not anything uses it, so turn a link off in environments that don't need it.

With a link, a Cloud Run service that needs something on AWS sends its private traffic into the default network. A private ECS service it needs gets an address on the environment's internal load balancer, which has private addresses only; RDS and ElastiCache are reached at their own private addresses. The environment's database, cache and internal load balancer security groups let in Google's network, and nothing else from outside the VPC.

It works one way. AWS has a route back, but a private Cloud Run service checks Google identity tokens rather than addresses, so a service on ECS still can't call one. Make it public, or move the caller.

A link needs more than a connection's check asks for. The Google Cloud key needs Compute Network Admin (`roles/compute.networkAdmin`) for the HA VPN, router and tunnels. The AWS key needs `ec2:CreateVpnGateway`, `ec2:AttachVpnGateway`, `ec2:DetachVpnGateway`, `ec2:DeleteVpnGateway`, `ec2:DescribeVpnGateways`, `ec2:CreateCustomerGateway`, `ec2:DeleteCustomerGateway`, `ec2:CreateVpnConnection`, `ec2:DeleteVpnConnection`, `ec2:DescribeVpnConnections`, `ec2:DescribeRouteTables`, `ec2:EnableVgwRoutePropagation` and `ec2:CreateTags`.

Removing a link from the manifest takes it down on both clouds, Google's side first. A link with a tunnel down shows as drift, and the next plan brings back what's missing.

## ignore

`resources` lists kinds analysis found that the application doesn't use, so plans stop asking about them.

## held

`same_address` is what a scoped plan does when a held service uses a service that was deleted outside Loomplane and that the plan creates again. The held service isn't deployed again, so it keeps the old address. With `block`, the default, that's a problem until you add the held repository to the scope. With `warn` it's a warning instead, but only when the planner can tell the address won't change: a Cloud Run service created again with the name, region and connection it had, which Cloud Run gives the same URL. Anything else, including a replacement, is still a problem.

```yaml
held:
  same_address: warn
```

## Targets

| Provider          | Cloud                | Runs                                           | Provisions                             |
| ----------------- | -------------------- | ---------------------------------------------- | -------------------------------------- |
| `vercel`          | Vercel               | web apps, static sites                         |                                        |
| `cloud-run`       | Google Cloud         | HTTP services, web apps                        |                                        |
| `cloud-sql`       | Google Cloud         |                                                | Postgres 17, 16, 15; MySQL 8.4, 8.0    |
| `memorystore`     | Google Cloud         |                                                | Redis                                  |
| `cloud-storage`   | Google Cloud         |                                                | Object storage                         |
| `ecs-fargate`     | AWS                  | HTTP services, web apps, workers               |                                        |
| `rds`             | AWS                  |                                                | Postgres 17, 16, 15; MySQL 8.4, 8.0    |
| `elasticache`     | AWS                  |                                                | Redis                                  |
| `s3`              | AWS                  |                                                | Object storage                         |
| `cross-cloud-vpn` | Google Cloud and AWS |                                                | [Links](#links) between them           |
| `render`          | Render               | HTTP services, web apps, workers, static sites | Postgres 17, 16, 15; Key Value (Redis) |

Buckets are private on both clouds, and services reach them as their own identity, so `STORAGE_URL` carries no credentials: `s3://bucket?region=...` for an ECS task's environment role, `gs://bucket` for Cloud Run's runtime account. Redis gets an auth token in `REDIS_URL`: `rediss://...` on ElastiCache, which also encrypts in transit and at rest, and `redis://...` on Memorystore, which only has an address on the project's default network, so Cloud Run services that need it send their private traffic there.

On AWS, the region is the connection's. An AWS connection names one, and everything Loomplane runs or provisions with it goes there, since ECS tasks reach RDS and ElastiCache over that region's network. Leave `region` out and you get the connection's. Name another and the plan stops on it, because the deployment would land in the connection's region anyway. To move an environment to another AWS region, choose a connection in that region for it. On Google Cloud you pick the region per service or resource, and a move starts the new one first: Cloud SQL instances and Cloud Storage buckets get names with their region in them, so the new one never collides with the one it replaces.

Render builds each service itself, from your repository through its own GitHub connection, so give Render access to the repository in its dashboard before the first deployment. Loomplane creates each service with automatic deploys off, cancels the deploy Render starts on its own when a service is made, and deploys the commit the plan approved. A service with a Dockerfile builds from it; otherwise Render's native runtime builds it, which it has for Go, Node and Python. A Node service starts with `npm start`, so it needs a start script, and a Rust or JVM service needs a Dockerfile. A private service is reached on Render's private network by the other services on Render, at `http://` its name there and its port. Postgres and Key Value are reachable from services on Render only.

Provider plugins add targets of their own ([providers.md](providers.md)). An operator can pause any target with `providers.paused` in the configuration ([operations.md](operations.md#pausing-a-target)): plans that use it are then for review only.

## Environments

Every project has `production` and `preview`, and you can add others. A plan is for one environment, and each field's environment override applies there. `enabled: false` in an environment leaves a service, resource or gateway out of it, and removes it if it was deployed.

Choosing another connection for an environment moves its services: each one starts again through the new connection, and the old one is removed once it's up, so what uses it gets the new address. Resources don't move, since their data would stay behind. A plan that would provision one through another connection than the one it was provisioned through has a problem until you choose that connection again.

## Version 2 federation intent

Version 2 records federation proposals. Its plans remain blocked until the destination agents, network drivers and policy enforcement pass qualification. A version change does not make private traffic available.

Use `pkg/manifest/schema.json` (`https://loomplane.dev/schemas/loomplane.json`) for the combined editor schema. `schema.v1.json` retains the original version 1 schema. Generated manifests still use version 1. To convert an existing file, review its destinations and public exposure first, change `version` explicitly, add the federation section, and review the resulting blocked plan. Keep the original file for rollback. The editor never upgrades it automatically.

```yaml
version: 2
services:
  api: { source: cmd/api, provider: kubernetes }
  worker: { source: cmd/worker, provider: kubernetes, public: false, port: 8080 }
federation:
  destinations:
    east: { id: dest_01m4va49zwe9pvk24sm9h8v0ta }
    west:
      id: dest_01m4va49zwe9pvk24sm9h8v0tb
      environments:
        preview: dest_01m4va49zwe9pvk24sm9h8v0tc
  network_links:
    east-west: { from: east, to: west, transport: imported }
  service_exports:
    worker-http:
      service: worker
      destination: west
      exposure: private
      port: 8080
      protocol: http
      identity: workload
  access_grants:
    api-worker:
      source_service: api
      source_destination: east
      export: worker-http
      methods: [GET]
      paths: [/health]
      expires_at: "2027-01-01T00:00:00Z"
  placements:
    api-east: { service: api, destination: east }
    worker-west: { service: worker, destination: west }
```

Destination IDs must resolve in the selected organization, project and environment. Overrides select a separately registered destination for that environment. Cross-environment and cross-organization grants are rejected by this initial format. Connections supply credentials through Vault; manifests contain no tokens, kubeconfigs or certificate keys.

Exports require explicit private exposure. Set `public: false` and an explicit service `port` matching the export in the selected environment. Remove public endpoints and gateway routes before exporting a service. HTTP selectors are exact paths and methods; gRPC selectors use `/package.Service/Method`; opaque TCP accepts neither. UDP is unavailable. Every export and source grant needs a matching placement. Runtime call relationships belong in access proposals, so two services can propose calling each other without creating a deployment dependency cycle. Existing `needs` still controls configuration and startup ordering and grants no access.

The preview compares each placement with the service's effective environment settings. Its provider must belong to the destination's cloud, and a direct target or resolved Cloud target must use the destination's checked connection. A disabled service or a repository held out of the plan blocks its placement. This also checks the deployed repository when the manifest moves a service to another source. If the repository cannot be identified while any repository is held, include the source before reviewing federation changes.

You can review each placement's service, source, destination identity and generation. Exports depend on their placements, grants depend on the source placement and export, and activation waits for all prerequisites. The review revision includes the manifest, destination generations, deployed binding revisions and resolved Cloud revisions. These actions describe prerequisites only; they do not deploy workloads or authorize traffic.

The five federation maps each allow at most 100 entries. A grant allows 16 HTTP methods, 32 paths of at most 1024 bytes, or 32 gRPC methods of at most 256 bytes. The existing 256 KiB manifest limit also applies. An expiry must be RFC3339; execution must reject an expired grant even if its manifest still parses.

Version 2 is the shared transition for federation and the one-click services workstream. Federation owns the optional `federation` section. Recipe sources, storage and protection will add separately typed optional sections as their implementations land; unsupported fields are rejected today. Neither workstream implicitly converts existing version 1 files.

Destination management is an internal service registered with the API process. Set `federation.enabled: true` in the local configuration to enable authorized in-process management; the default is false. This flag does not enable traffic or add public management routes. Those routes and the dashboard follow the runtime qualification milestones.
