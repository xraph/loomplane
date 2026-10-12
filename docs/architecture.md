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
  ├─ grove                      Postgres pool, one migration pass
  ├─ loomplane-identity         authsome engine, Warden, Chronicle, the guard
  ├─ dispatch                   job engine, queue "loomplane"
  ├─ ctrlplane                  lifecycle engine, routes off
  ├─ loomplane-projects         /api/v1/projects, the initialize job
  ├─ loomplane-discovery        /api/v1/.../analyses, /api/v1/github; queues analyses
  ├─ loomplane-infrastructure   Ctrl Plane's identity bridge, /api/v1/infrastructure
  └─ loomplane-web              apps/dashboard/dist on every other path

cmd/loomplane-worker          same config, no HTTP API, no migrations
      │
      ▼
forge.App
  ├─ grove                      the same Postgres
  ├─ dispatch                   queue "loomplane-analysis" only
  └─ loomplane-discovery-worker the analysis job: fetch, read, store the graph
```

The API never runs an analysis. It queues one on `loomplane-analysis`, a queue only the worker works, so code nobody vetted is read in a process that holds no sessions and serves nothing but health checks. Start `loomplane` first: it owns the schema, and the worker doesn't migrate.

Starting goes in three passes, which is what `forge.WithCentralMigrations` buys:

1. Forge registers every extension in dependency order. Nothing reads or writes the database yet. Engines get built, routes get registered, and the grove extension collects every migration group.
2. One pass applies every pending migration: Loomplane's own, then authsome, Warden, Chronicle, Dispatch and Ctrl Plane. Each library's self-migration is off, so nothing else touches the schema.
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
  │ /api/v1/*     guard: authsome session chain → Principal        │
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
| Projects and environments | Loomplane | `loomplane_projects`, `loomplane_environments` |
| Repository analyses, each with one version of the application graph | Loomplane | `loomplane_analyses` |
| GitHub App installations each organization connected | Loomplane | `loomplane_github_installations` |
| Job queue, cron, workflow history | Dispatch | `dispatch_*` |
| Instances, deployments, releases, domains | Ctrl Plane | `cp_*` |

Coming with deployments:

| State | Owner |
|---|---|
| The manifest (`loomplane.yaml`) | Loomplane |
| Approved plans, and the map from each Loomplane service to the Ctrl Plane instance or provider resource that runs it | Loomplane |
| Progress of each deployment workflow | Dispatch's durable runtime |
| The lifecycle state of what's deployed | Ctrl Plane, for providers it manages; Loomplane's capability adapter, for providers Ctrl Plane can't model (Vercel) |

Projects belong to an organization by its authsome ID, with no foreign key into authsome's tables, because Loomplane never writes those.

### One reconciler per resource

Two controllers mutating the same resource fight. So each resource type gets exactly one reconciliation authority:

- Ctrl Plane reconciles the instances it manages. Its instance drift correction is a stub today, so until that's filled in upstream, Loomplane's drift detection (milestone 5) reads provider status through Ctrl Plane and reports, without writing.
- Loomplane reconciles its own records against the approved plan, through Dispatch cron jobs.
- Dispatch owns workflow progress and retries. A workflow step asks Ctrl Plane to act; it never edits Ctrl Plane's records.

A failed provider call surfaces as a failed Dispatch activity with the provider's error. After its retries are spent, the workflow ends in an explicit failed state that the deployment's history shows, and Ctrl Plane's instance state says what's actually running.

## Trust boundaries

```
 untrusted              │ Loomplane process                 │ external
 ───────────────────────┼───────────────────────────────────┼──────────────
 browser, CLI, agents ──┼─► guard ──► service ──► Warden    │
                        │      │                            │
                        │      └─► authsome (sessions)      │
                        │                                   │
                        │ secret_key ─HKDF─► token keyring  │
                        │ (future) Vault ─► provider APIs ──┼─► Vercel, GCP, AWS, Render
                        │                                   │
 analysis worker ───────┼── one commit's tarball, read only ◄┼── GitHub (a token for
                        │                                   │   one repo, one hour)
 build workers ◄────────┼── (future) source, never creds    │
```

Who you are comes from your authsome session. The dashboard holds it in the SDK's local storage, and sends it as a bearer token; authsome also sets an HttpOnly, SameSite=Lax cookie that Loomplane's guard accepts. Every route under `/api/v1` except `/public-config` sits behind the guard.

The organization you act in also comes from the session, never from the browser, and authsome only lets a member switch a session to an organization. The dashboard sends `X-Loomplane-Org` with the organization it believes it's in, and a mismatch answers 409, so a stale tab finds out instead of acting in the wrong place. The header never selects anything.

Whether you may do something is Warden's call, made in the service layer for every method. The dashboard hides what your role can't do, but the API checks regardless. Organization roles map to Warden roles: owner to owner, admin to admin, member to developer, and anything else to viewer, so an unexpected role never grants more than reading.

Ctrl Plane and Dispatch don't expose their own HTTP APIs. Ctrl Plane's auth provider is bound to authsome and Warden and never grants `system:admin`.

Repository analysis reads code, and never runs it: no build scripts, no package managers, nothing from the repository. A checkout is one commit's tarball, extracted with hard limits (size, file count, regular files only, no paths outside the checkout, no dependency or build output directories) into a directory the worker deletes afterwards. For a private repository, the worker mints a GitHub App installation token limited to that one repository and to reading it, and stores it nowhere. An organization's analyses only ever use installations that organization connected, and connecting one requires GitHub to confirm the person connecting can see it ([github-app.md](github-app.md)).

Secrets: `secret_key` never leaves the process. authsome's token encryption key is derived from it with HKDF under its own label. Cloud provider credentials will live in Vault (milestone 4), referenced by ID, never in `loomplane.yaml` or logs, and never handed to build workers. Repository builds will run in isolated workers that get the source and nothing else. A Dispatch worker only decides what runs where; it doesn't sandbox anything, so the isolation has to come from the worker's own container. Loomplane's own API keys (Keysmith, milestone 6) are a different thing from cloud credentials and are never stored together.

## Deployment, as it will run

The deployment workflow is a Dispatch durable workflow. Each step is an activity, so it's retried, and a crash resumes from the last completed step. Activities pass their idempotency key to provider APIs.

| Step | Where it runs |
|---|---|
| Validate the repository revision; load and validate `loomplane.yaml` | Loomplane activities |
| Resolve service dependencies; check cloud connections and provider capabilities | Loomplane activities, credentials from Vault |
| Generate the plan | Loomplane's planner |
| Wait for approval | a durable signal, with a durable timer as the deadline |
| Check the approved plan's revision still matches | Loomplane activity |
| Provision dependencies; build artifacts | isolated build workers; artifacts in Trove through Dispatch |
| Deploy each service | Ctrl Plane, or a Loomplane capability adapter for providers Ctrl Plane can't model |
| Configure endpoints and connections; verify health | Ctrl Plane network and health, plus Loomplane checks |
| Record the result, or end in an explicit failure | Loomplane records; Dispatch keeps the history |
