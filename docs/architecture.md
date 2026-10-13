# Architecture

Loomplane is two Forge processes over one Postgres, with a React dashboard in front: `loomplane`, which serves the API and the dashboard, and `loomplane-worker`, which analyzes repositories. This document covers how a process starts and stops, how the Forge ecosystem libraries connect, which component owns which state, and where the trust boundaries sit. [forge-integration.md](forge-integration.md) has the per-library detail and [implementation-plan.md](implementation-plan.md) has what comes next.

## The process

```
cmd/loomplane                 config.yaml + config.local.yaml + LOOMPLANE_*
      │                                   │
      ▼                                   ▼
internal/platform ◄──────────── internal/config (typed, validated)
      │
      ▼
forge.App (central migrations)
  ├─ loomplane-schema-guard     refuses a database a newer Loomplane migrated
  ├─ grove                      Postgres pool, one migration pass
  ├─ loomplane-identity         authsome, Warden, Chronicle, Keysmith; the guard
  ├─ dispatch                   jobs, cron and the durable deployment workflow
  ├─ ctrlplane                  lifecycle engine, routes off
  ├─ loomplane-projects         /api/v1/projects, the initialize job
  ├─ loomplane-discovery        /api/v1/.../analyses, /api/v1/github; queues analyses
  ├─ loomplane-environments     /api/v1/variables and each project's; secrets in Vault
  ├─ loomplane-connections      cloud connections, their checkers, credentials in Vault
  ├─ loomplane-planning         manifests, plans, deployments, executors, drift
  ├─ loomplane-infrastructure   Ctrl Plane's identity bridge, /api/v1/infrastructure
  └─ loomplane-web              apps/dashboard/dist on every other path

cmd/loomplane-worker          same config, no HTTP API, no migrations
      │
      ▼
forge.App
  ├─ loomplane-schema-guard
  ├─ grove                      the same Postgres
  ├─ dispatch                   queue "loomplane-analysis" only
  └─ loomplane-discovery-worker the analysis job: fetch, read, store the graph

cmd/loomctl                   the CLI and MCP server, over the API with a key
```

The API never runs an analysis. It queues one on `loomplane-analysis`, a queue only the worker works, so code nobody vetted is read in a process that holds no sessions and serves nothing but health checks. Start `loomplane` first: it owns the schema, and the worker doesn't migrate.

Starting goes in three passes, which is what `forge.WithCentralMigrations` buys:

1. Forge registers every extension in dependency order. Nothing reads or writes the database yet. Engines get built, routes get registered, and the grove extension collects every migration group.
2. One pass applies every pending migration: Loomplane's own, then authsome, Warden, Chronicle, Dispatch, Ctrl Plane, Vault and Keysmith. Each library's self-migration is off, so nothing else touches the schema.
3. Forge starts every extension in the same order. authsome's engine starts and its routes mount, Dispatch starts working its queue, Ctrl Plane starts its workers. Then Forge binds HTTP.

A job can't arrive before its handler exists: handlers register in pass 1, and Dispatch only takes work in pass 3.

Stopping runs the other way. Forge stops each extension in reverse order, Dispatch finishes what it's running, and the grove extension closes the pool last.

The dashboard is a separate build (`apps/dashboard`) that the same process serves. In development, Vite serves it and proxies the API, so your browser still sees one origin.

## How the libraries connect

```
                 browser (apps/dashboard)
                 │  @authsome/ui-* ── sign-in, sessions, org switching
                 │  @forge-go/client-* ── every API call, one normalized cache
                 ▼
  ┌──────────── Forge router ─────────────────────────────────────┐
  │ /authsome/*   authsome API (own router, FirstJSON filter)      │
  │ /api/v1/*     guard: API key or authsome session → Principal   │
  │ /*            the built dashboard                              │
  └───────────────┬───────────────────────────────────────────────┘
                  ▼
     internal/projects.Service ── Warden check on every method
          │              │                     │
          ▼              ▼                     ▼
     loomplane_*    Dispatch job          Chronicle event
     (Postgres)     (initialize)          (org's stream)
                         │
                         ▼
                 projects.Service.Initialize

     authsome ──hooks──► security.Sync ──► Warden roles per organization
     Ctrl Plane ──auth──► infrastructure.AuthProvider ──► authsome + Warden
```

Domain packages (`internal/projects`, `internal/security`, `internal/orchestration`, `internal/config`) don't import Forge. `internal/projects` marks entity IDs with a `forge:"id"` struct tag, which costs it no import. Everything Forge-specific lives in `extensions/` and `internal/platform`.

## Who owns what

Each kind of state has one owner, and only the owner writes it.

| State | Owner | Tables |
|---|---|---|
| Users, sessions, organizations, members, invitations | authsome | authsome's |
| Roles, permissions and who holds them, per organization | Warden, written by Loomplane's `security.Sync` and `security.Authorizer` | `warden_*` |
| The audit trail | Chronicle | `chronicle_*` |
| Projects and environments (every project has at least one) | Loomplane | `loomplane_projects`, `loomplane_environments` |
| Variables on every layer: organization, project, service | Loomplane | `loomplane_variables` |
| Secret variable values, encrypted and versioned | Vault | `vault_secrets` |
| A project's sources: the repositories it builds from, each with an alias | Loomplane | `loomplane_project_sources` |
| Analyses, each reading one source at one commit and storing that repository's graph | Loomplane | `loomplane_analyses` |
| The commit of each source that is deployed to each environment | Loomplane | `loomplane_environment_sources` |
| GitHub App installations each organization connected | Loomplane | `loomplane_github_installations` |
| Manifest revisions (`loomplane.yaml`) | Loomplane | `loomplane_manifests` |
| Plans, each with its lockset, and the decision on each | Loomplane | `loomplane_plans`, `loomplane_plan_sources` |
| Deployments and their steps | Loomplane | `loomplane_deployments` |
| What runs each service and resource in each environment, and its drift | Loomplane | `loomplane_bindings` |
| Cloud connections, and the one each environment uses | Loomplane | `loomplane_cloud_connections`, `loomplane_environment_connections` |
| Cloud credentials, and the passwords of databases Loomplane provisions | Vault | `vault_secrets` |
| API keys, as hashes | Keysmith | `keysmith_*` |
| Each deployment's sources, one tarball per repository it moves | Trove | files under `storage.url` |
| Job queue, cron, workflow history and progress | Dispatch | `dispatch_*` |
| The lifecycle of long-running services on Cloud Run and ECS | Ctrl Plane | `cp_*` |

A project builds from one or more sources. A source is a GitHub repository, an optional directory inside it, a tracked branch and an alias. The alias is part of every service ID (`svc:api:go:cmd/server`), so it never changes and is never reused, even after the source is removed. An analysis reads one source at one commit and stores that repository's graph on its own.

Nothing stores the project's graph. It's composed when something asks for it, from the newest successful analysis of each source: IDs are qualified by alias, a source's directory filters what it contributes, resources of the same kind merge, and a dependency that one repository declares on a package another provides becomes an edge between them. A plan pins a lockset, which is one analysis and commit per source, and composes from those, so it always sees the graph it was made from. A scope picks the sources a plan moves. The rest stay at the commits `loomplane_environment_sources` says are deployed, and their services are kept as they run.

Vercel deployments, databases and gateways aren't Ctrl Plane instances: Ctrl Plane models long-running instances and nothing else, so Loomplane's own executors carry those out, and a binding records what they made either way.

Projects belong to an organization by its authsome ID, with no foreign key into authsome's tables, because Loomplane never writes those.

### One reconciler per resource

Two controllers mutating the same resource fight. So each resource type gets exactly one reconciliation authority:

- Ctrl Plane reconciles the instances it manages. Its instance drift correction is a stub upstream, so Loomplane's drift check reads what's live through each target's executor and records it, without writing. The next plan, approved like any other, puts things back.
- Loomplane reconciles its own records against the approved plan, through Dispatch cron jobs.
- Dispatch owns workflow progress and retries. A workflow step asks Ctrl Plane to act; it never edits Ctrl Plane's records.

A failed provider call surfaces as a failed Dispatch activity with the provider's error. After its retries are spent, the workflow ends in an explicit failed state that the deployment's history shows, and Ctrl Plane's instance state says what's actually running.

## Trust boundaries

```
 untrusted              │ Loomplane process                 │ external
 ───────────────────────┼───────────────────────────────────┼──────────────
 browser, CLI, agents ──┼─► guard ──► service ──► Warden    │
                        │      │                            │
                        │      ├─► authsome (sessions)      │
                        │      └─► Keysmith (API keys)      │
                        │                                   │
                        │ secret_key ─HKDF─► Vault keys     │
                        │ Vault ─► cloud APIs ──────────────┼─► Vercel, GCP, AWS
                        │ source ─► cloud builders ─────────┼─► Cloud Build, CodeBuild,
                        │                                   │   Vercel (your accounts)
                        │ plugin token ─► provider plugins ─┼─► your infrastructure
                        │                                   │
 analysis worker ───────┼── a tarball per source, read only ◄┼── GitHub (a token for
                        │                                   │   one repo, one hour)
```

Who you are comes from your authsome session. The dashboard holds it in the SDK's local storage, and sends it as a bearer token; authsome also sets an HttpOnly, SameSite=Lax cookie that Loomplane's guard accepts. Every route under `/api/v1` except `/public-config` sits behind the guard.

The organization you act in also comes from the session, never from the browser, and authsome only lets a member switch a session to an organization. The dashboard sends `X-Loomplane-Org` with the organization it believes it's in, and a mismatch answers 409, so a stale tab finds out instead of acting in the wrong place. The header never selects anything.

Whether you may do something is Warden's call, made in the service layer for every method. The dashboard hides what your role can't do, but the API checks regardless. Organization roles map to Warden roles: owner to owner, admin to admin, member to developer, and anything else to viewer, so an unexpected role never grants more than reading.

Ctrl Plane and Dispatch don't expose their own HTTP APIs. Ctrl Plane's auth provider is bound to authsome and Warden and never grants `system:admin`.

Repository analysis reads code, and never runs it: no build scripts, no package managers, nothing from the repository. A checkout is one repository's tarball at one commit, extracted with hard limits (size, file count, regular files only, no paths outside the checkout, no dependency or build output directories) into a directory the worker deletes afterwards. For a private repository, the worker mints a GitHub App installation token limited to that one repository and to reading it, and stores it nowhere. An organization's analyses only ever use installations that organization connected, and connecting one requires GitHub to confirm the person connecting can see it ([github-app.md](github-app.md)).

Secrets: `secret_key` never leaves the process. Every key Loomplane needs is derived from it with HKDF under a label of its own: authsome's token encryption, the GitHub install state's signature, and Vault's encryption of variable secrets. A secret variable's value goes straight into Vault, AES-256-GCM encrypted and versioned, and the API never returns it; a check constraint keeps it out of `loomplane_variables` entirely. Vault's secrets carry no tenant, so Loomplane builds every Vault key from its own organization and variable IDs, never from a caller's input. Deleting a project or an environment cascades its variables in Postgres, and a sweep then removes their secrets from Vault. Cloud credentials live in Vault too, referenced by connection ID, never in `loomplane.yaml`, logs or anything a build sees. Builds run on the clouds' own builders, in your accounts, so nothing from a repository runs on a Loomplane machine at all. A service's secrets go to its cloud's secret store and reach it by reference, readable by its runtime identity only, and never pass through Ctrl Plane, which stores what it's given as plain JSON. Loomplane's own API keys are Keysmith's, stored as hashes, and a different thing from cloud credentials. [security.md](security.md) has the whole picture.

## How a deployment runs

A deployment is a Dispatch durable workflow, running beside the API. Each step is an activity, so it's retried, and a crash resumes from the last completed step. Long steps heartbeat and checkpoint the cloud's operation ID, so a retry waits on the build or database already under way instead of starting another.

| Step | Where it runs |
|---|---|
| Pin the lockset, one analysis and commit for each source; load the manifest revision; compute the plan from the graph those compose | Loomplane's planner, which never touches a cloud |
| Wait for a decision | a durable signal, with a durable timer as the deadline |
| Plan again, and check the revision still matches what was approved | Loomplane; a mismatch ends it as stale |
| Store one tarball for each source the plan moves, at its pinned commit | Trove, so every builder gets the same bytes |
| Provision resources | Loomplane's executors: Cloud SQL, RDS, or a provider plugin |
| Build each service from its own source's tarball | Cloud Build, CodeBuild or Vercel, in your accounts |
| Deploy each service, its secrets in the cloud's secret store | Ctrl Plane for Cloud Run and ECS; Loomplane's adapters for Vercel and gateways; a plugin for its own targets |
| Check each public service answers; record what runs it | Loomplane, as bindings |
| Finish, recording the commit now deployed for each source that moved, or fail with the step and its reason | a failed deployment can be retried from that step |

Steps run in order, each after everything it needs, so a service gets the address of the database and services it needs when it deploys.

### When something moves

A service or resource that moves to another provider or region is replaced, and so is a service that moves to another connection, since what one connection deployed can't be updated through another. The new one starts first, with nothing bound to it. Once it's up, and answers if it's public, the binding names it and lists the old one as still to remove, with the provider, region and connection that reach it. Then the old one goes, through the executor and the connection that deployed it.

That list lives on the binding, not on the deployment, so a removal that fails isn't lost when you plan again instead of pressing Retry. Every plan warns about what's on it, and pins a connection to its cloud when it can, even if nothing else in the plan deploys there. Retrying the deployment that moved it removes it, and a failure there fails the step, as it did the first time. Any later deployment of that service or resource tries too, after its own work, but a failure there only adds a warning to the step: you'll see it on the step and on the next plan, and a permanent one, such as a revoked permission, can't block every deployment from then on. Deleting the service or resource is the exception, and the plan's warning says so. The binding is what tracks the list, so a delete removes everything on it first, and fails if it can't. Something only a change to Loomplane's setup can reach, such as a provider plugin that's been taken out, stays on the list until it's back. One whose connection was deleted comes off it, since nothing can ever reach it again, with a message saying where to delete it yourself.

A resource never moves between connections. Its data would stay behind, so a plan that would provision one through another connection than the one that provisioned it has a problem, and the runner refuses it too. Choose the old connection again to keep it. One that was deleted outside Loomplane has no data left, so it's created again through the new connection.

A binding from before bindings recorded their connection names none, and the planner can't tell where it runs. Cloud Run and ECS find out as they deploy, since a Ctrl Plane instance's provider names its connection and region. Moving it right there would give it a new address that nothing the plan kept would get, so the step deploys nothing, records the connection on the binding and fails, telling you to plan again. That plan sees the move and replaces it properly, deploying again what uses it. Retrying the failed deployment doesn't move it: its plan didn't replace it, so the step fails again and says to plan again. Only a plan that replaces something moves it to another connection. When an instance nothing is bound to holds a service's name in Ctrl Plane, a new instance takes the next free one (`api-production-x1-2`, then `-3`), so nothing has to be removed first. An ECS service keeps the target group and listener it has. A new one gets a target group named after its own instance, so removing an old one in the same account leaves the new one's alone.
