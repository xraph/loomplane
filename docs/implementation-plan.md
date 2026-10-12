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

1. The `@forge-go/client-*` packages move to 1.12.8 once pnpm's 24-hour release-age rule allows it.
2. The license, which Rex is choosing.
3. Upstream: a tagged authsome release so the pin can leave `main`; the authsome fixes listed in forge-integration.md (double-written JSON, the 500 on a taken slug, the SDK's organization list shape, the missing audit category, invitations without an expiry); dependency declarations on grove in the Dispatch and Ctrl Plane extensions; forgemux mounting `/`; the generator's pointer-slice type; a tag for the forge CLI so CI can pin something better than a commit.

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

1. Re-analyzing on push, which needs GitHub webhooks.
2. A repository picker when creating a project. The Integrations page lists what the App can read, but a project still takes a URL.
3. Detection limits: Python, Java and the rest are found through their Dockerfiles only. A Go command that imports the server's packages reads as a server, because classification follows imports. A Rust crate with both clap and axum is ambiguous.
4. The GitHub App flow has only been run against a fake GitHub that checks the App's JWT signature. Try it against a real App before relying on it.

## Environments and variables

Added after milestone 2, because deployments need configuration to deploy with.

- Every project has at least one environment, always. A new project's production and preview environments are created in the same transaction as the project. You can add others (staging, a QA environment), and deleting the last one is refused, with the project's row locked so two deletes can't race past the rule.
- Variables are set on three layers, each for every environment or for one: the organization (every project inherits these; an environment is named, so `production` means every project's production), a project, and one of its services (from the latest analysis). For a service in an environment, each key takes the most specific value: service beats project beats organization, and one environment beats every environment. The API's resolved view shows the winning value and what it overrides.
- Secret variables are encrypted in Vault and write-only: the API never returns their value, and the audit trail records keys, never values. Owners and admins set organization-wide variables; developers set their projects'.
- In the dashboard: environments on each project page, a Variables card that resolves an environment for the project or one service and lets you set, edit or override at that layer, and an organization-wide Variables page.

Left over: nothing reads the resolved variables yet. Deployments (milestone 4) will, and they'll be the only path that reveals a secret. A variable can't move to another layer or environment once set; delete it and set it again. Environments can't be renamed, because organization-wide variables name them.

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

1. Nothing happens after approval yet. Milestone 4 adds provisioning, builds and deploys to the same workflow, behind a new `DeploymentBuild`.
2. Adding or ignoring a resource from a plan saves the manifest from its parsed form, so comments in the YAML don't survive it. Edits made in the editor keep them.
3. There's no diff between manifest revisions in the dashboard, only the current one.

## Milestone 4: Real deployments

1. Cloud connections, with credentials in Vault.
2. Google Cloud Run as a Ctrl Plane provider. It runs long-running containers, which is the model Ctrl Plane's `Provider` interface describes.
3. Vercel as a Loomplane capability adapter. A Vercel deployment is an immutable build behind a URL, not an instance you start and stop, so forcing it into Ctrl Plane's instance model would misreport its state.
4. Builds in isolated workers, artifacts in Trove through Dispatch.
5. The deployment workflow from architecture.md, with its progress and history in the dashboard.

Done when a Next.js frontend deploys to Vercel and a Go backend to Cloud Run, from one monorepo.

## Milestones 5 and 6

AWS ECS and Fargate as a Ctrl Plane provider, then Render; managed databases; environment secrets; endpoints between services on different clouds; Bastion where you run a gateway; drift detection and recovery from partial failure. Then the CLI on Keysmith keys, a provider SDK, backups, upgrades, and MCP for agents.
