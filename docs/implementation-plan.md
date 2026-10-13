# Implementation plan

Where each milestone in the [brief](plan.md) stands, and what's next. [architecture.md](architecture.md) has the design these steps follow.

## Milestone 0: Forge ecosystem evaluation

Done. The results are in [forge-integration.md](forge-integration.md): the pinned versions, how each library is wired, and the gaps found in Forge, Grove, authsome, Dispatch and Ctrl Plane, each with its workaround. The license question there needs an answer before Loomplane is published.

## Milestone 1: Forge-native foundation

Done. You can run all of it with `make stack`, or the pieces with `make up` and `make dev`.

- The Forge app, with central migrations over one Postgres.
- Sign-up, sign-in and organizations through authsome, with sessions that slide and refresh.
- Warden roles per organization, mirrored from authsome membership, checked on every project action.
- Members and invitations. Admins invite by email and get a link to pass on, since Loomplane sends no mail yet. The link works once, for a week, and only for an account with that address. Owners and admins change roles and remove people, never above their own role. Someone who is removed lands back on onboarding instead of an organization they can't use.
- Projects and environments, with a Dispatch job that sets up a new project's environments.
- Ctrl Plane registered on Postgres, behind Loomplane's identity, with its status on `/api/v1/infrastructure`.
- Chronicle audit events for authsome, membership and project changes.
- The dashboard (shadcn on Base UI, authsome's components, the generated Forge client), served by the same process.
- A container image (`deployments/docker/Dockerfile`, `make image`) and a Compose profile that runs it beside Postgres (`make stack`).
- CI in `.github/workflows/ci.yml`: Go lint, unit and integration tests with the race detector, the dashboard's checks and build, `forge client check`, and the image build. It hasn't run on GitHub yet, so expect the first push to shake something out.
- Tests: Go unit tests, integration tests that boot the app against Postgres (twice, for the restart test), dashboard unit tests, and a browser run of the whole flow, invitations included.

Left over, none of it blocking Milestone 2:

1. The `@forge-go/client-*` packages move to 1.12.8 once pnpm's 24-hour release-age rule allows it. Done.
2. The license. Done: the GNU Affero General Public License, version 3.
3. Upstream: a tagged authsome release so the pin can leave `main`; the authsome fixes listed in forge-integration.md (double-written JSON, the 500 on a taken slug, the SDK's organization list shape, the missing audit category, invitations without an expiry); dependency declarations on grove in the Dispatch and Ctrl Plane extensions; forgemux mounting `/`; the generator's pointer-slice type; a tag for the forge CLI so CI can pin something better than a commit. Fixed after milestone 6, and merged to each library's main and pushed: authsome (organization responses written once, 409 on a taken slug, an audit category on every event, invitations that expire after a week, only the invited account may accept, a removed member's sessions lose the organization, a lazy auth middleware, and the UI SDK reading the paged organization list); Forge (`Handle("/")` serves `/`, a nil middleware fails fast instead of hanging, and `migrate` finds grove's migrations); grove (its registry provided as `forge.CentralMigrator`); Dispatch and Ctrl Plane (each declares its dependency on grove); Bastion (a gateway that proxies without the store packages, a token for the admin API, health checks that don't take a route down on a 404, and the trailing slash forwarded). The pointer-slice type was already fixed on Forge's main. None is tagged yet, so Loomplane still pins the releases it had and keeps its workarounds; moving its pins to these commits, or to tags made from them, is what lets them go.

## Milestone 2: Repository discovery

Done. Give a project a GitHub URL and choose **Analyze** on its page: you get its services, the packages they share and the resources they need, each with links to the lines it was read from, at the exact commit.

- Detectors in `internal/discovery/detect`, behind the contract in `pkg/discovery`. They read and never run anything:
  - Go: `go.work`, every `go.mod` (with the official parser), and each main package. The frameworks and drivers a main package reaches come from following its imports through the workspace.
  - Rust: Cargo workspaces and their member globs, binaries the way Cargo finds them, path and workspace dependencies, and features (which database `sqlx` speaks).
  - Node: npm, Yarn, pnpm and Bun workspaces, Turborepo, Nx and Lerna. A framework counts only when the package is an app built on it, so a library that imports Next.js stays a library. Prisma's provider is read from its schema.
  - Dockerfiles (ports, base image, which binary a build step builds) and Compose files (backing services, build contexts).
- Every finding carries its file, line and a confidence. Resources found only from a driver in a dependency list are marked to confirm before anything provisions them.
- The application graph in `internal/graph`: services, packages, resources and edges, independent of any cloud. Each successful analysis stores one version of it.
- Analysis is a Dispatch job on its own queue, run by `cmd/loomplane-worker`, with its progress (queued, downloading, reading) on the project page.
- GitHub: public repositories with no setup, through the tarball API, which pins the exact commit. Private ones go through a GitHub App ([github-app.md](github-app.md)), connected only once GitHub confirms the person connecting can see the installation, with a read-only token for one repository per analysis.
- Verified with unit tests, integration tests against a fake GitHub (private repositories included), a browser run of the whole flow, and analyses of real public monorepos: Tauri (Rust and TypeScript), OpenMeter (Go and TypeScript) and Dub (a Turborepo).

Where it differs from the plan above, and why:

1. An analysis is one idempotent Dispatch job, not a durable workflow. It's three short steps, each recorded on the analysis, and a failure on the last attempt fails the analysis with its reason. Durable workflows earn their keep in deployments, where approvals wait for days.
2. There's no throwaway container per analysis. Nothing from the repository runs, so the risk is the parsers, which are pure Go behind size and file-count limits, in a process that holds no sessions. Builds run code; they get real containers in milestone 4.
3. Weak findings are flagged, but there's no way yet to confirm or dismiss one. That arrives with the manifest in milestone 3, which is where a confirmed graph becomes the plan's input.

Left over:

1. Re-analyzing on push, which needs GitHub webhooks. Done after milestone 6: a push to a repository's default branch re-analyzes every source tracking it (one that names no branch, or names that one), in the organizations that connected the installation it came through. Sources are indexed by repository, so a push to `owner/name` maps to the sources that track it with one lookup.
2. A repository picker when creating a project. Done with the dashboard redesign: the new project dialog lists what the GitHub App can read, and a URL still works for a public repository.
3. Detection limits. Done after milestone 6: Python (pyproject.toml, requirements.txt, Pipfile, uv workspaces) and the JVM (Maven, and Gradle with version catalogs) have detectors of their own. A Go command that shares a server's packages, or a Rust binary that names clap and reaches axum only through its crate's library beside a server that shares it, reads as a tool, flagged for you to confirm. Each process in a Python project's Procfile is a service of its own, and so is a Celery app beside a web app, each built from the project with its own start command. Gradle builds are read without running them, through plain and map-notation dependencies, version catalogs, subprojects and allprojects blocks, and convention plugins in buildSrc or build-logic; a script that works its dependencies out in a loop or a condition can't be read without running it, which analysis never does, so those dependencies need declaring plainly or confirming in the manifest.
4. The GitHub App flow has only been run against a fake GitHub that checks the App's JWT signature. Try it against a real App before relying on it.
5. A cleanup migration. Done: `drop_superseded_columns` drops `loomplane_projects.repository_url`, `loomplane_plans.analysis_id` and `commit_sha`, and `loomplane_deployments.source`, which sources and plan locksets replaced. They were kept for a rollback, but no release ever had them without sources beside them, and its down migration puts them back filled from what replaced them. Restore now loads a backup at the schema it was taken at and migrates it up from there, so a backup from before the drop still restores.

## Environments and variables

Added after milestone 2, because deployments need configuration to deploy with.

- Every project has at least one environment, always. A new project's production and preview environments are created in the same transaction as the project. You can add others (staging, a QA environment), and deleting the last one is refused, with the project's row locked so two deletes can't race past the rule.
- Variables are set on three layers, each for every environment or for one: the organization (every project inherits these; an environment is named, so `production` means every project's production), a project, and one of its services (from the project's graph, composed from every repository). For a service in an environment, each key takes the most specific value: service beats project beats organization, and one environment beats every environment. The API's resolved view shows the winning value and what it overrides.
- Secret variables are encrypted in Vault and write-only: the API never returns their value, and the audit trail records keys, never values. Owners and admins set organization-wide variables; developers set their projects'.
- In the dashboard: environments on each project page, a Variables card that resolves an environment for the project or one service and lets you set, edit or override at that layer, and an organization-wide Variables page.

Left over: deployments read the resolved variables since milestone 4, and they're the only path that reveals a secret. A variable can't move to another layer or environment once set; delete it and set it again. Environments can't be renamed, because organization-wide variables name them.

## Milestone 3: Planning

Done. Analyze a project, open its Deploy tab, and you get a drafted `loomplane.yaml`. Save it, choose an environment and **Plan**: Loomplane works out what deploying it would do and waits for someone to approve or reject that, without touching a cloud.

- `loomplane.yaml` in `pkg/manifest`: services by where their source lives, the resources they need, and the target each runs on, with overrides per environment. It's versioned, refuses fields it doesn't know, and ships a JSON schema for editors. Nothing in it mentions Forge.
- Each project keeps its manifest as a history of revisions. Saving never edits one, so a plan names exactly the revision it was made from, and saving the same thing twice adds nothing.
- A project with an analysis and no manifest gets a draft: every deployable service on the first target that runs its kind, and every resource found with good evidence on a target that provisions it, preferring the cloud most of the services landed on. You review it before it's saved.
- The provider catalog in `pkg/provider` and `internal/providers`: what Vercel, Cloud Run, ECS on Fargate, Render and the managed databases run and provision, and which of Ctrl Plane or Loomplane would carry each action out. None of them has an adapter yet, so every plan says it's for review.
- The planner in `internal/planning` turns the graph, the manifest and the catalog into ordered actions (create, update, replace, delete, keep), with the variables each service gets for what it needs. Problems block a plan; warnings don't.
- Weak findings get answered here. A resource analysis wasn't sure about, or one the manifest doesn't provision, shows up as a warning on the plan, and you can add it or tell Loomplane the application doesn't use it. Either one saves a new manifest revision.
- Each plan runs as a Dispatch durable workflow: plan, then wait for a decision with a durable timer as the deadline (a week by default, `planning.approval_window`). Approving names the plan's revision. If what's deployed changed since the plan was made, the approval comes back stale instead of approved. A new plan for the same environment replaces the open one.
- Bindings record what runs each service and resource in an environment. Deployments will write them, and planning reads them to know what exists, so once something is deployed the next plan keeps it instead of creating it again.
- In the dashboard: the Deploy tab, with the manifest editor (a save that doesn't parse lists every problem with its line), the project's plans, and a review sheet with the findings, the evidence behind them, the actions step by step, and approve or reject.
- Verified with unit tests for the manifest, the planner and the draft, an integration test that drafts, saves, plans, approves across a restart, supersedes and rejects, and a browser run against OpenMeter.

Where it differs from the plan above, and why:

1. The deployment workflow runs in the API process, not in `loomplane-worker`. It only reads and writes Loomplane's own records, never a repository's code, so it doesn't need the worker's isolation. Builds will, and they'll get it in milestone 4.
2. The draft wasn't in the brief. Without it, the first thing you'd see after an analysis is an empty YAML file.

Left over:

1. Nothing happened after approval. Milestone 4 added provisioning, builds and deploys to the same workflow.
2. Adding or ignoring a resource from a plan saved the manifest from its parsed form, so comments in the YAML didn't survive it. Done after milestone 6: the API edits the YAML's node tree (`POST /projects/:id/manifest/edits`), so your comments and order stay where you put them.
3. There was no diff between manifest revisions. Done after milestone 6: History on the Deploy tab lists every revision and what each changed from the one before.

## Milestone 4: Real deployments

Done, apart from the acceptance run on real accounts. Done when a Next.js frontend deploys to Vercel and a Go backend to Cloud Run, from one monorepo, after someone approves the plan.

How it fits together:

- **Cloud connections** (done). An organization connects a Google Cloud project with a service account key, or a Vercel account with a token. Each is checked with its cloud before it's kept, and lives in Vault. A plan resolves which connection each target deploys with: the environment's choice if it has one, else the organization's only ready connection to that cloud. Several, with no choice, is a problem on the plan.
- **Builds run on the providers' own builders.** Dispatch has no container rung yet (only in-process and subprocess), and the brief rules out running a repository's build in our processes. So the Go service is built by Google Cloud Build, from a Dockerfile when the repository has one and with Google's buildpacks when it doesn't, into Artifact Registry. Vercel builds the Next.js app itself. Nothing from the repository runs on a Loomplane machine.
- **Sources are artifacts.** Each repository a plan moves is fetched once, as a tarball of the commit the plan pins for it, and stored in Trove through Dispatch's artifact plane. Each service's builder takes its own repository's tarball from there: uploaded to the project's Cloud Build bucket, or to Vercel file by file.
- **Cloud Run goes through Ctrl Plane.** It's a long-running container, which is Ctrl Plane's model. Ctrl Plane takes credentials only when a provider is constructed, so each Google Cloud connection gets its own provider, registered under a unique key that is also its `Info().Name` (Ctrl Plane looks instances up by that name, not by registry key). The provider reads its key from Vault on every call, so a replaced or deleted connection takes effect at once. `Deploy` and `Provision` wait for the revision to be ready, because Ctrl Plane records success as soon as they return.
- **Vercel is a Loomplane capability adapter**, as planned: a project, files, a deployment and its ready state, outside Ctrl Plane's instance model.
- **The deployment workflow continues after approval**, as `deployment-v2`. It fetches the source, then for each step of the plan builds and deploys its actions, passing each service the addresses of what it needs and its resolved variables (the one path that reveals a secret). Then it checks each public service answers, writes the bindings, and records the deployment. Long steps heartbeat, and keep the provider's operation ID in their heartbeat so a retry picks the operation up instead of starting another.
- **Progress** is a deployment record per approved plan, with one row per action, shown on the plan and on the Deploy tab.

Steps:

1. Cloud connections, in Vault, with the API and the Integrations card. Done.
2. Connections on plans, chosen per environment when there's more than one. Done.
3. Deployment records, one source artifact in Trove for each repository the plan moves, and the workflow after approval. A successful deployment also writes `loomplane_environment_sources`, which is what a scoped plan holds the other sources at. Done.
4. Cloud Build and the Cloud Run provider for Ctrl Plane. Done.
5. The Vercel adapter. Done.
6. Deployment progress in the dashboard: each step on the approved plan, recent deployments on the Deploy tab, and what runs in each environment on its card. Done.
7. The acceptance run on real accounts: a Google Cloud project and a Vercel account. Waiting on credentials.

Everything up to step 6 is tested against fakes that stand in for Google Cloud and Vercel over HTTP, including the full approval of a Next.js app on Vercel that finds its Go API on Cloud Run. The fakes check what a real cloud would refuse: tokens, file digests, missing files. They can't prove the real APIs agree, which is what step 7 is for.

Left over, for after the acceptance run:

1. Variables, secrets included, were set as plain environment variables on Cloud Run. Milestone 5 moved secrets to Secret Manager.
2. A Dockerfile was built from its own directory. Done after milestone 6: it builds from the context a Compose file gives it, or `build.context` in the manifest, on Cloud Build, CodeBuild and provider plugins alike.
3. Services found each other by public URL. Milestone 5 added private addresses on ECS and private Cloud Run services; a private service on another cloud stays unreachable, and the plan says so.
4. Ctrl Plane gaps worth fixing upstream: instances keep the provider's name rather than its registry key, providers get credentials only at construction, Deploy carries only images and variables, and nothing polls status after a call returns. Fixed on Ctrl Plane's main: instances record the registry key, a credentials resolver hands providers credentials per call, a deploy can carry a command, ports and a size, an optional readiness wait polls status after a call, `Config.DefaultProvider` is applied, and an opt-in Postgres advisory lock elects one scheduler.

## Milestone 5: Extended orchestration

Done, apart from the acceptance run on real accounts. Done when a polyglot application deploys across several providers and Loomplane keeps one consistent record of what runs where.

What it covers:

1. Recovery from partial failure. A failed deployment can be retried from the step that failed, with the plan that was approved; steps that succeeded stay as they are.
2. Drift detection. A Dispatch job on the cron leader compares every binding with what its cloud has live and records anything gone or changed. It only reads. The next plan creates or updates it back, and only an approved plan changes anything, so there's one authority. Bindings also record the commit each service was built from, and a plan compares it with the commit it pins for that service's own repository, so new code in one repository is a change a plan picks up and a held repository's services stay as they are. A scoped plan can't put back what drifted in a repository it holds, since it doesn't fetch that repository: it warns, or refuses when something it deploys needs a held service or resource that was deleted, and says which repository to add to the scope.
3. Environment secrets. On Cloud Run they live in Secret Manager and reach the service by reference, readable by its runtime account only; on Vercel they're sensitive variables. No secret is handed to Ctrl Plane, which stores what it's given in plain JSON.
4. A managed database. Postgres and MySQL on Cloud SQL, with a generated password kept in Vault, reached from Cloud Run through the Cloud SQL connector. A database is only reachable from services on its own cloud, so anything else needing it is a problem on the plan.
5. AWS connections: an access key, a region, and an optional ACM certificate, checked with STS and IAM's policy simulator.
6. ECS on Fargate. Images build on CodeBuild into ECR, from the tarball of the service's repository at its commit, repacked as the zip CodeBuild reads from S3; services with a Dockerfile build from it, the rest with buildpacks. Loomplane creates the CodeBuild role and the task execution role. Services run in the account's default VPC as ECS services that Ctrl Plane drives through a provider per connection, the way Cloud Run's works. Each environment gets one Application Load Balancer, and each public service a listener port of its own, since without a domain there's no host to route on and paths break apps that expect the root: the first gets 80, or 443 with the connection's certificate, then 8000 and up. A plan for an account without a certificate warns that its public services serve plain HTTP. Secrets go to Secrets Manager and reach the task by ARN.
7. RDS for Postgres and MySQL, in the default VPC and not publicly reachable. Each environment's databases share a security group that takes the database port from its ECS tasks' group and nothing else. As on Cloud SQL, the password is generated, kept in Vault before it's used, and reaches services in the address, through Secrets Manager. A binding records the engine and major version, so RDS's own minor upgrades aren't drift.
8. Secure endpoints between services. On ECS, each environment's services join a Cloud Map namespace through Service Connect, and a service that needs another on ECS gets `http://name:port`, so traffic between them stays in the VPC whether or not the one it needs is public; the environment's tasks group lets in itself for that. On Cloud Run, a private service lets in only the project's runtime account, which every service Loomplane deploys there runs as, and the plan warns that callers have to send a Google ID token. Setting who may call a service now replaces the policy, so a service that stops being public stops being public. A private service needed from another cloud, or on a target with no private network, is a problem on the plan: there's no way to reach it, so Loomplane won't wire an address that can't work. A private link between clouds would need a VPN between them, which is out of scope for now.
9. Gateways. A `gateways:` entry routes paths to services through one public address. Bastion has no image of its own, so Loomplane generates a small program on it with the routes baked in, builds it on Cloud Build or CodeBuild like any service, and runs it on Cloud Run or ECS; traffic never passes through Loomplane. It has no admin API, gets the addresses it routes to and none of the project's variables, and isn't rebuilt for a new commit, only for new routes. On ECS it reaches private services inside the environment; elsewhere a route to a private service is a problem on the plan, since a gateway can't present Cloud Run's ID token. [forge-integration.md](forge-integration.md) lists what we found wiring Bastion.

Left over: the acceptance run on real Google Cloud and AWS accounts, with milestone 4's, and private links between clouds, which arrived after milestone 6 (see below). Memorystore, Cloud Storage, ElastiCache and S3 got their adapters after milestone 6.

## Milestone 6: Operational maturity

Done. Done when Loomplane runs as a complete, independent, self-hosted platform.

1. API keys, on Keysmith. A key acts as the member who made it, in one organization, with their role, and can be limited to reading; it loses its rights when its maker does. Keys expire within a year, are managed from a session only, and are shown once.
2. `loomctl`, the CLI, on those keys and `pkg/client`, a typed Go client for the API: analyze, manifests, variables, plans, approvals that name the revision they showed, deployments followed step by step, retries, status and drift. CI uses `LOOMPLANE_URL` and `LOOMPLANE_API_KEY`.
3. The provider SDK. A provider plugin is a small HTTP service built with `pkg/provider/sdk`, or in any language that speaks the protocol; Loomplane adds it to the catalog when it starts and drives it like a built-in target, its cloud included. `examples/provider-plugin` is a working one, and [providers.md](providers.md) the guide.
4. Backups and restores: `loomplane backup` copies every table in one snapshot, and `loomplane restore` migrates an empty database and loads it in one transaction.
5. Upgrades: `loomplane migrations` and `migrate`, and Loomplane refuses to start against a database a newer Loomplane migrated, except in development, where branches share a database.
6. Observability: Loomplane's own metrics on `/_/metrics`, for deployments, steps, plans, decisions, drift, analyses, API keys and credential checks, by provider and outcome; and provider plugins on `/_/health`.
7. AI agents: `loomctl mcp` serves Loomplane's tools over MCP. Deploying is off unless you pass `--allow-deploy`, and a read-only key reads only.
8. Security testing: an authorization matrix over every route, fuzzers for each parser of untrusted input, govulncheck in CI, and the Go toolchain pinned to a release with every standard library fix. It found and fixed a deployments list that answered 200 for another organization's project, and a repacker that would have kept `../` entry names.
9. Documentation: the README, [manifest.md](manifest.md), [operations.md](operations.md), [security.md](security.md), [agents.md](agents.md) and [providers.md](providers.md), and the architecture guide brought up to date.

Left over:

1. The acceptance runs on real accounts, for milestones 4 and 5: a Google Cloud project, a Vercel account and an AWS account. Everything is tested against fakes that check what the real APIs would refuse, which isn't the same as the real APIs agreeing. `make acceptance` runs one now ([acceptance.md](acceptance.md)): it connects the accounts you set, deploys a repository and manifest you choose, checks every public address answers, and takes it all down again. It needs your accounts, so it waits on you.
2. A license for Loomplane. Done: the GNU Affero General Public License, version 3. The dependency license questions in [forge-integration.md](forge-integration.md#licenses) are the libraries' own, and go with the upstream work.
3. Render had no adapter. Done: a Render connection is an API key, and the adapter runs web services, private services, background workers and static sites, and provisions Postgres and Key Value. Render builds from the repository through its own GitHub connection, which you give access in Render once; Loomplane deploys the approved commit through it. Like the other adapters, it's tested against a fake that answers the way Render's API does, and the acceptance run will show whether the real one agrees. Targets can also be paused in the configuration now (`providers.paused`), which keeps their plans for review only.
4. Private links between clouds. Done: a `links:` entry joins an environment's Google Cloud network to its AWS VPC with an HA VPN (four tunnels with BGP), and Cloud Run services then reach private ECS services, through an internal load balancer, and RDS and ElastiCache. It's one way: a private Cloud Run service checks Google identity tokens, so AWS still can't call one. It's tested against fakes of both clouds, including taking a link down; the acceptance run will show whether the real APIs agree, and it costs money by the hour while it's up.
5. The dashboard had no screen for metrics. Done: Activity shows the organization's deployments, how often they succeed and how long they take, plan decisions, analyses and drift over 7, 30 or 90 days, counted from Loomplane's own records (`GET /api/v1/activity`). Backups stay a command, `loomplane backup`, since a backup holds every organization's data and doesn't belong behind one organization's page.
