<p align="center">
  <img src="docs/assets/loomplane-logo.svg" width="96" height="96" alt="Loomplane's woven gold and ivory logo" />
</p>

<h1 align="center">Loomplane</h1>

<p align="center"><strong>Your infrastructure, woven together.</strong></p>

<p align="center">
  A deployment platform for the monorepos you already have.<br />
  Built with Go, React, and the Forge ecosystem.
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#what-you-can-use-today">Features</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="#roadmap">Roadmap</a> ·
  <a href="#documentation">Documentation</a>
</p>

![Interlaced charcoal, gold, and green threads forming a connected architectural grid](docs/assets/loomplane-cover.png)

<p align="center"><sub>Concept artwork inspired by the dashboard's woven environments.</sub></p>

Loomplane is being built to take your existing Go, Rust, TypeScript, and JavaScript monorepos from source to deployments across clouds. You bring the repository. Loomplane will discover its services, produce a plan you can review, and coordinate deployments from one dashboard. Your applications won't need Forge.

The foundation runs today: you can create an account, organize your projects, and watch a background job prepare production and preview environments. Repository discovery, deployment planning, and cloud deployments are the next milestones. A saved repository URL is metadata at this stage; Loomplane doesn't clone or deploy it yet.

## Quick start

You'll need:

- Go **1.26**.
- Node.js **24** and pnpm. The dashboard pins pnpm **11.5.0** through its `packageManager` field.
- Docker with Docker Compose for the local PostgreSQL database.
- The Forge CLI on your path if you want hot reload or client generation.

From the project root:

```bash
# Start PostgreSQL on localhost:55532.
make up

# On your first run, create your local database configuration.
cp config.local.yaml.example config.local.yaml

# Install dependencies, build the dashboard and API, and run both.
make setup
make run
```

Open [localhost:8780](http://localhost:8780), create an account, and sign in. Give your organization a name, then create your first project.

Already have `config.local.yaml`? Keep your settings and skip the copy. `make up` starts PostgreSQL only; `make run` builds Loomplane and starts both of its processes on your machine: the API, and the worker that analyzes repositories. Migrations apply automatically at startup.

### Run it all in Docker

No Go or Node on your machine? You don't need them for this:

```bash
make stack
```

That builds the image from `deployments/docker/Dockerfile` and starts it beside PostgreSQL on [localhost:8780](http://localhost:8780). Set `LOOMPLANE_PORT` for another port. The container shares the database `make dev` uses, so stop one before you start the other. `make down` stops both.

The image runs in development mode with the development secret key, which is fine on a laptop. Anywhere else, set `LOOMPLANE_ENVIRONMENT=production`, `LOOMPLANE_SECRET_KEY` and an https `LOOMPLANE_PUBLIC_URL`. Production refuses to start without them.

### Try the project flow

1. Choose **New project** and enter a name. A repository URL and description are optional.
2. Open the project. Its `production` and `preview` environments are there from the start; add others, like `staging`, from the Environments card. A project always keeps at least one.
3. It shows `initializing` until a Dispatch job finishes setting it up, then `ready`.
4. Open **Infrastructure** to inspect Ctrl Plane's persistence backend and registered providers.
5. Open **Members** and choose **Invite**. You get a link to pass on yourself, because Loomplane doesn't send email yet. Whoever opens it signs in or signs up with the invited address, then accepts.
6. Give a project a GitHub URL (any public monorepo works, such as `https://github.com/dubinc/dub`) and choose **Analyze** under **Architecture**. The worker downloads that commit and reads it. You get the services it builds, what they share and the databases they need, each linked to the line it came from.
7. Open **Variables** in the sidebar and set something every project should get, like `LOG_LEVEL`. On a project's page, the Variables card shows what it resolves to in each environment, for the whole project or one service, and lets you override it there.

`ready` means the project is set up. It doesn't mean an application has been deployed. The project page draws each environment as a thread: gold while setup runs, green when it's ready.

### Develop with hot reload

```bash
make dev
```

Use [localhost:5173](http://localhost:5173). Vite serves the dashboard and proxies `/api` and `/authsome` to the API on port `8780`, so your browser uses one origin. The Forge CLI reloads the Go app as you edit.

You can also run each side in a separate terminal:

```bash
make dev-api
make dev-dashboard
```

## What you can use today

| Capability            | What you get                                                                                                                   |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Accounts and sessions | Sign-up, sign-in, session refresh, and account controls through authsome.                                                      |
| Organizations         | Create an organization and switch the active organization on your session.                                                     |
| Members               | Invite people by link, change their roles, and remove them. Nobody grants a role above their own.                              |
| Projects              | Create, list, view, update, and delete projects through the API. The dashboard covers creation, listing, detail, and deletion. |
| Environments          | Every project has production and preview from the moment it exists, and at least one always. Add staging or any other. |
| Variables             | Organization, project and service layers, each for every environment or one; the most specific wins. Secrets are encrypted in Vault and never shown again. |
| Permissions           | Warden checks access in the service layer, with roles scoped to each organization.                                             |
| Audit events          | Chronicle records identity activity and project changes. An audit reader and dashboard are still planned.                      |
| Infrastructure status | Inspect Ctrl Plane's store and registered providers through Loomplane's guarded API.                                           |
| Repository discovery  | Go, Rust and Node monorepos, Dockerfiles and Compose files, read (never run) into services, packages and resources with evidence. |
| GitHub                | Public repositories as they are; private ones through a GitHub App, with a read-only token per repository per analysis.         |
| Dashboard             | React, shadcn/ui on Base UI, light and dark themes, and generated API clients with a shared normalized cache.                  |
| Persistence           | One PostgreSQL database, with a central migration pass over Loomplane and its library schemas.                                 |

The dashboard uses TanStack Router with typed file routes and automatic page splitting. It has a woven mark, Geist typography, and gold and green status colors. Its environment illustration respects reduced-motion preferences.

## Architecture

![Loomplane architecture: a React dashboard connects to the Forge application, whose identity, project, job, audit, and infrastructure services persist in PostgreSQL](docs/assets/loomplane-architecture.svg)

Loomplane runs as one Forge application. The same Go process serves the built React dashboard, handles the API, runs Dispatch jobs, and hosts Ctrl Plane. You don't need to deploy each library as another service.

| Component      | Responsibility                                                                                |
| -------------- | --------------------------------------------------------------------------------------------- |
| **Forge**      | HTTP routing, dependency injection, extension lifecycle, health, metrics, and OpenAPI.        |
| **Grove**      | The PostgreSQL pool and centralized migrations.                                               |
| **authsome**   | Users, sessions, organizations, memberships, and invitations.                                 |
| **Warden**     | Organization roles and authorization.                                                         |
| **Dispatch**   | Persistent background jobs. Deployment workflows will use its durable runtime.                |
| **Ctrl Plane** | The infrastructure lifecycle engine, accessed in-process through Loomplane's identity bridge. |
| **Chronicle**  | Identity and project audit events.                                                            |
| **Confy**      | Configuration files and environment variables, read into Loomplane's typed configuration.     |

Startup has three passes: register extensions, apply pending migrations, then start services. Job handlers exist before workers take work. Shutdown stops extensions in reverse order and closes PostgreSQL last.

Domain logic lives in `internal/`, without Forge imports in the project, security, configuration, and orchestration packages. `extensions/` connects those packages to Forge. You can follow the lifecycle and state ownership in [the architecture guide](docs/architecture.md).

### A project, from request to ready

```text
Create project
    |
    v
Resolve session and active organization
    |
    v
Check project:create with Warden
    |
    v
Save project as initializing + enqueue a Dispatch job
    |
    v
Create production and preview environments
    |
    v
Mark project ready + refresh the dashboard
```

The handler can safely run again if Dispatch retries it. If enqueueing fails during creation, the service attempts to remove the new project so it won't sit there waiting for a job that never arrived.

## Organization access

Your session selects the organization. Every project query and permission check uses that scope, and the dashboard sends `X-Loomplane-Org` to detect a stale tab. A mismatch returns `409`; the header cannot select a different organization.

| Organization membership | Loomplane role | Current project access                                                               |
| ----------------------- | -------------- | ------------------------------------------------------------------------------------ |
| `owner`                 | Owner          | Read, create, update, and delete. Also holds organization administration permission. |
| `admin`                 | Admin          | Read, create, update, and delete.                                                    |
| `member`                | Developer      | Read, create, and update.                                                            |
| Any other role          | Viewer         | Read only.                                                                           |

All four roles can read environments, infrastructure status and the member list. Owners and admins invite people, change roles and remove members, but never above their own role, and nobody changes their own role or removes themselves from the members page.

The permission catalog also reserves permissions for deployments, cloud accounts, and secrets, whose workflows are planned.

The API checks permissions even when the dashboard hides an action. Ctrl Plane and Dispatch's own HTTP APIs are disabled; Loomplane exposes the routes it authorizes. Ctrl Plane's identity bridge never grants `system:admin`.

## Configuration

Start with [config.yaml](config.yaml). Your machine's overrides go in `config.local.yaml`, which is ignored by Git. The environment variables below are wired explicitly in the base configuration.

| Variable                             | Default                     | Purpose                                                                          |
| ------------------------------------ | --------------------------- | -------------------------------------------------------------------------------- |
| `LOOMPLANE_DATABASE_URL`             | Required                    | PostgreSQL DSN. The local example points at Compose on `localhost:55532`.        |
| `LOOMPLANE_SECRET_KEY`               | Development key             | Exactly 32 random bytes, base64 encoded. Production rejects the development key. |
| `LOOMPLANE_HTTP_ADDR`                | `:8780`                     | API listen address.                                                              |
| `LOOMPLANE_PUBLIC_URL`               | `http://localhost:8780`     | Public application URL. Production requires HTTPS.                               |
| `LOOMPLANE_ENVIRONMENT`              | `development`               | `development`, `staging`, or `production`.                                       |
| `LOOMPLANE_DASHBOARD_DIR`            | `apps/dashboard/dist`       | Built dashboard directory.                                                       |
| `LOOMPLANE_AUTHSOME_APP_ID`          | Built-in development app ID | authsome application ID.                                                         |
| `LOOMPLANE_AUTHSOME_PUBLISHABLE_KEY` | Built-in development key    | Public authsome application key.                                                 |
| `LOOMPLANE_GITHUB_*`                 | Unset                       | The GitHub App for private repositories; [docs/github-app.md](docs/github-app.md) lists them. |
| `LOOMPLANE_WORKER_HTTP_ADDR`         | `:8781`                     | Where `loomplane-worker` serves its health checks.                               |

Generate your own secret with:

```bash
openssl rand -base64 32
```

Keep that value in your environment or local configuration. The process derives authsome's token encryption key from it. Database pool size, identity options, and worker concurrency are configured in YAML; the default worker concurrency is `4`, with a `1s` polling interval.

## API

Loomplane serves its OpenAPI document at `/openapi.json`. In development, you can explore it at `/swagger`.

| Method   | Route                               | Purpose                                             |
| -------- | ----------------------------------- | --------------------------------------------------- |
| `GET`    | `/api/v1/public-config`             | Public settings needed before sign-in.              |
| `GET`    | `/api/v1/session`                   | Current user, active organization, and permissions. |
| `GET`    | `/api/v1/projects`                  | List your organization's projects.                  |
| `POST`   | `/api/v1/projects`                  | Create a project and enqueue initialization.        |
| `GET`    | `/api/v1/projects/:id`              | Read one project.                                   |
| `PATCH`  | `/api/v1/projects/:id`              | Update its name, description, or repository URL.    |
| `DELETE` | `/api/v1/projects/:id`              | Delete a project and its environments.              |
| `GET`    | `/api/v1/projects/:id/environments` | List a project's environments.                      |
| `GET`    | `/api/v1/infrastructure`            | Read Ctrl Plane's store and provider status.        |

Except for `public-config`, these `/api/v1` routes require an authsome session. The guard accepts bearer tokens and authsome session cookies. Identity routes are mounted under `/authsome` by default. Health and metrics are at `/_/health` and `/_/metrics`.

For example, with a session token in `LOOMPLANE_SESSION_TOKEN`:

```bash
curl --fail-with-body http://localhost:8780/api/v1/projects \
  -H "Authorization: Bearer ${LOOMPLANE_SESSION_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data '{
    "name": "Storefront",
    "description": "Frontend, API, and background workers"
  }'
```

The response is `201 Created` with an `initializing` project. Poll its detail and environment endpoints to see setup finish. API JSON uses fields such as `repository_url`; the generated TypeScript client exposes their camelCase equivalents.

## Development and checks

| Command                 | What it does                                                               |
| ----------------------- | -------------------------------------------------------------------------- |
| `make setup`            | Download Go modules and install dashboard packages.                        |
| `make up` / `make down` | Start PostgreSQL, or stop everything Compose started. Data volume is kept. |
| `make stack`            | Build the image and run all of Loomplane in Docker on port 8780.           |
| `make image`            | Build the container image, tagged with the git version.                    |
| `make dev`              | Run the API and dashboard with hot reload.                                 |
| `make build`            | Build the dashboard, then `bin/loomplane` and `bin/loomplane-worker`.      |
| `make run`              | Build, then run the API and the analysis worker together.                  |
| `make test`             | Run Go and dashboard unit tests.                                           |
| `make test-integration` | Boot the app against PostgreSQL, including real background initialization. |
| `make lint`             | Run golangci-lint, dashboard type checks, and ESLint.                      |
| `make gen-client`       | Refresh specs from the running API and regenerate TypeScript clients.      |
| `make check-client`     | Check generated clients against their saved specs.                         |
| `make help`             | List available targets.                                                    |

Run `make up` before the integration suite. Each run creates and drops its own test database. You can point it at another PostgreSQL instance with `TEST_DATABASE_URL`.

The dashboard's formatter is `pnpm --dir apps/dashboard format`; Go source uses `gofmt`. There are no root `make f` or `make l` aliases.

### Generated clients

Dashboard requests go through Forge's generated clients and the shared query cache. If you change an API contract, start the API and run:

```bash
make gen-client
make check-client
```

Don't edit `apps/dashboard/src/client/forge` by hand. The handwritten runtime lives in `apps/dashboard/src/client/runtime.ts`. See [the dashboard guide](apps/dashboard/README.md) for query bindings, component setup, and SDK patches.

## Roadmap

| Milestone                    | Status                         | Scope                                                                                                                                       |
| ---------------------------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------- |
| 0. Ecosystem evaluation      | Documented                     | Pinned libraries, integration behavior, upstream gaps, and dependency license questions.                                                    |
| 1. Foundation                | Done                           | Identity, members and invitations, projects, environments, authorization, jobs, audit events, dashboard, container image, and CI.          |
| 2. Repository discovery      | Done                           | GitHub (public, and private through an App), a separate analysis worker, Go, Rust and Node detectors, and a versioned application graph.    |
| 3. Deployment planning       | Planned                        | Versioned `loomplane.yaml`, provider capabilities, reviewable actions, and approval tied to a plan revision.                                |
| 4. Real deployments          | Planned                        | Vault credentials, isolated builds, Trove artifacts, Cloud Run through Ctrl Plane, and a Vercel capability adapter.                         |
| 5. More infrastructure       | Planned                        | AWS ECS/Fargate, Render, managed databases, cross-cloud endpoints, and drift reporting.                                                     |
| 6. Automation and operations | Planned                        | CLI API keys, provider SDK, backups, upgrades, and MCP access.                                                                              |

The first planned deployment milestone is a Next.js frontend on Vercel and a Go backend on Google Cloud Run from one monorepo. Ctrl Plane's existing Docker, Kubernetes, and Nomad implementations don't establish Loomplane cloud deployment support; the infrastructure screen reports only what the running app has registered.

Cloud Run fits Ctrl Plane's container lifecycle. Vercel's immutable builds and URLs need a separate capability adapter. The distinction lets the future planner describe what each provider actually runs.

The [implementation plan](docs/implementation-plan.md) records the work and completion criteria for each milestone. Provider credentials, build isolation, and deployment approvals are planned there and in the architecture guide.

## Project layout

```text
loomplane/
├── cmd/loomplane/             The API and dashboard
├── cmd/loomplane-worker/      The repository analysis worker
├── internal/
│   ├── config/               Typed configuration and validation
│   ├── discovery/            Analyses, and the detectors in detect/
│   ├── environments/         Layered variables, with secrets in Vault
│   ├── graph/                The application graph
│   ├── infrastructure/       Ctrl Plane identity bridge
│   ├── orchestration/        Dispatch jobs and enqueue policy
│   ├── platform/             Forge application composition
│   ├── projects/             Project and environment domain logic
│   ├── repositories/         GitHub: checkouts, the App, connections
│   └── security/             Organization roles and authorization
├── pkg/discovery/            The public contract for detectors
├── extensions/               Forge adapters: identity, projects, discovery, environments, infra, web
├── examples/polyglot/        A sample monorepo the detector tests read
├── database/migrations/      Loomplane schema and library migration groups
├── apps/dashboard/           React application and generated clients
├── brand/                    Logo variants, icons, and brand guidelines
├── tests/integration/        Whole-application PostgreSQL tests
├── deployments/              Container image and the Docker Compose stack
├── scripts/                  Development process runner
└── docs/                     Architecture, plans, integration notes, artwork
```

## Documentation

| Guide                                              | Read it when you want to...                                                             |
| -------------------------------------------------- | --------------------------------------------------------------------------------------- |
| [Architecture](docs/architecture.md)               | Follow startup, state ownership, trust boundaries, and the planned deployment workflow. |
| [Forge integration](docs/forge-integration.md)     | Check exact library pins, adapters, limitations, SDK patches, and upstream findings.    |
| [Implementation plan](docs/implementation-plan.md) | Pick up a milestone or inspect what remains unfinished.                                 |
| [Product brief](docs/plan.md)                      | Understand the intended monorepo and multi-cloud workflows.                             |
| [Dashboard](apps/dashboard/README.md)              | Work on React components, API bindings, and client generation.                          |
| [Brand assets](brand/README.md)                    | Choose a logo variant, app icon, or color and rebuild the brand kit.                    |

## Contributing

Start with the implementation plan and keep your change focused. Domain behavior belongs in `internal/`; Forge wiring belongs in `extensions/`. If you change a dependency pin, update the integration guide too.

Before submitting a change, run `make lint`, `make test`, and `make build`. Run the integration suite for changes to identity, persistence, authorization, or job execution. Regenerate and check clients when the API changes.

## License and distribution

This checkout doesn't include a Loomplane license file. Several pinned dependencies also have unresolved license or distribution questions, recorded in [the integration guide](docs/forge-integration.md#licenses). Review those before redistributing the project.
