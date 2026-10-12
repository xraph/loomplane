# Federated sources: one application from many repositories

Status: design, approved in conversation on 2026-10-12. Not built yet.

A Loomplane project holds exactly one repository today, in a single `repository_url` column. This design gives a project any number of **sources**, each a GitHub repository (or a subtree of one), analyzes each on its own, and composes the results into one application graph. A plan pins one commit per source, and you can scope a plan so that only some sources move while the rest stay at what's deployed.

Some of those repositories will be monorepos. That's fine: everything the detectors already do inside a monorepo keeps working, and federation sits one level above it.

## What changes for you

You attach repositories to a project as sources, each with a short alias:

```
alias   repository        root        ref
web     acme/frontend                 main
api     acme/platform     services/   main
site    acme/platform     apps/www    main
```

Service IDs gain the alias, so `svc:go:cmd/server` becomes `svc:api:go:cmd/server`. The manifest says which source a service comes from:

```yaml
services:
  site: { repo: web, source: apps/site, kind: web-app }
  api:  { repo: api, source: go:cmd/server }
```

You can leave `repo:` out while the project has one source. Once it has two, planning asks you to fill it in.

Planning an environment pins a commit per source. Pass a scope to move only some of them:

```
POST /api/v1/projects/:id/plans  {"environment": "production", "scope": ["web"]}
```

Every source outside the scope stays at the commit deployed to that environment, and its services get no actions at all.

## What we have today

A project has one `repository_url` column (`database/migrations/migrations.go:80`) and nothing else. There's no repositories table. An analysis is one tarball at one SHA, the graph it builds has no repository in it, and a plan pins a single `analysis_id` and `commit_sha` (`internal/planning/plan.go:193`). Node IDs carry no repository either (`internal/graph/build.go:99,127`), so two repositories that each have `cmd/api` would collide, and variables, bindings and the manifest's `source` all key off those IDs.

Two things already work in our favour. GitHub access is scoped to the organization, not the project, so fetching several repositories needs no new authorization model. And the detectors already read every declared dependency and record each unit's name (Go module path, crate name, npm package name); they just drop the dependencies that don't resolve inside the checkout (`internal/discovery/detect/golang.go:354`, `node.go:343`, `rust.go:240`). Cross-repository edges need exactly that dropped data.

## Decisions

These were settled while designing, and the rest of the document follows from them.

1. A plan can move every source together or only a scoped subset. Out-of-scope sources stay at their deployed commit.
2. There's one manifest per project, stored in Loomplane as today. Repositories don't carry their own `loomplane.yaml`.
3. Federation happens at the graph layer. Git stays strictly per repository: one token, one tarball, one SHA each.
4. Aliases are immutable and never reused, even after a source is removed.
5. IDs are always alias-qualified. We considered giving an existing project's repository an empty alias so its IDs wouldn't change, and decided against two ID shapes. Manifests and plans haven't shipped and nothing deploys yet, so there's little to migrate.
6. Two sources publishing the same package name produce a note, not an edge.
7. `root` filters the graph after a whole-repository analysis. It does not narrow the checkout.
8. A held service ignores manifest edits as well as code changes.

## Data model

### Sources

```sql
CREATE TABLE loomplane_project_sources (
  id          TEXT PRIMARY KEY,
  org_id      TEXT NOT NULL,
  project_id  TEXT NOT NULL REFERENCES loomplane_projects(id) ON DELETE CASCADE,
  alias       TEXT NOT NULL,
  repository  TEXT NOT NULL,             -- owner/name
  root        TEXT NOT NULL DEFAULT '',  -- a subtree, '' for the whole repository
  ref         TEXT NOT NULL DEFAULT '',  -- the tracked branch, '' for the default
  created_by  TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL,
  removed_at  TIMESTAMPTZ,
  CONSTRAINT loomplane_project_sources_alias UNIQUE (project_id, alias)
);
CREATE INDEX loomplane_project_sources_repository ON loomplane_project_sources (repository);
```

An alias matches `^[a-z][a-z0-9-]{0,31}$` and can't be an ecosystem name (`go`, `rust`, `node`, `container`, and any added later), which keeps `svc:<alias>:<eco>:<path>` unambiguous. The unique constraint covers removed rows on purpose: an old plan's `web` must always mean the same repository.

`alias` and `repository` cannot change; `ref` and `root` can. If you attach one repository twice, the roots must not nest among live sources, so `services/` and `services/api` can't both be attached; otherwise one service would appear under two aliases. A unique constraint cannot express that check, so the service does it before inserting.

Removing a source sets `removed_at`. Its analyses stay, because applied plans reference them.

`loomplane_projects.repository_url` is dropped.

### Analyses

`loomplane_analyses` gains `source_id TEXT NOT NULL REFERENCES loomplane_project_sources(id)`. The existing `repository`, `ref` and `commit_sha` columns stay as a snapshot. "One active analysis" becomes one per source.

### Plans and what's deployed

```sql
CREATE TABLE loomplane_plan_sources (
  plan_id      TEXT NOT NULL REFERENCES loomplane_plans(id) ON DELETE CASCADE,
  source_id    TEXT NOT NULL REFERENCES loomplane_project_sources(id),
  analysis_id  TEXT NOT NULL REFERENCES loomplane_analyses(id),
  commit_sha   TEXT NOT NULL,
  in_scope     BOOLEAN NOT NULL,
  PRIMARY KEY (plan_id, source_id)
);

CREATE TABLE loomplane_environment_sources (
  environment_id  TEXT NOT NULL REFERENCES loomplane_environments(id) ON DELETE CASCADE,
  source_id       TEXT NOT NULL REFERENCES loomplane_project_sources(id),
  analysis_id     TEXT NOT NULL REFERENCES loomplane_analyses(id),
  commit_sha      TEXT NOT NULL,
  plan_id         TEXT NOT NULL REFERENCES loomplane_plans(id),
  deployed_at     TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (environment_id, source_id)
);
```

`loomplane_plans.analysis_id` and `commit_sha` are dropped in favour of the plan's rows in `loomplane_plan_sources`.

`loomplane_environment_sources` is what's actually deployed, kept apart from what a plan decided. Only a successful deployment writes it, and only for that plan's in-scope sources. That's milestone 4's executor. Until it exists the table stays empty, and every plan has to cover every source. Nothing records what is running yet, so planning makes no guesses about it.

### Migration

For each project with a non-empty `repository_url`, the migration creates one source. The alias is the repository name, slugified and cut to 32 characters, with `-src` appended when it clashes with an ecosystem name. Every `loomplane_variables.service` value is rewritten from `svc:<eco>:<path>` to `svc:<alias>:<eco>:<path>`, and every existing analysis gets that source's ID. Projects with no repository get no source.

Manifest revisions and plans are still uncommitted work at the time of writing, so the migration doesn't rewrite them. If either ships before this does, the migration must also add `repo:` to each manifest service (writing a new revision, not editing old ones) and move each plan's pair into `loomplane_plan_sources`.

## Identity

| Node | Today | Federated |
|---|---|---|
| Service | `svc:go:cmd/api` | `svc:api:go:cmd/api` |
| Package | `pkg:go:libs/x` | `pkg:api:go:libs/x` |
| Resource | `res:postgres` | `res:postgres`, shared across sources |

Resources stay project-wide, the way one repository's Postgres uses already collapse into one node. If a manifest needs two databases, it says so, as it would today.

Per-source graphs, the ones stored on each analysis, stay alias-free. Only the composed graph is qualified. That keeps an analysis independent of which project or alias it was attached under.

## The graph

### Detectors keep what they drop

`pkg/discovery.Unit` gains the dependencies the checkout doesn't satisfy:

```go
// Requires are the dependencies this unit declares that no unit in the
// checkout provides.
Requires []Requirement `json:"requires,omitempty"`

// Requirement is one declared dependency on something outside the checkout.
type Requirement struct {
	Name     string   `json:"name"`
	Version  string   `json:"version,omitempty"`
	// Git is the repository a Rust git dependency names.
	Git      string   `json:"git,omitempty"`
	Evidence Evidence `json:"evidence"`
}
```

Go records unmatched `require`s, Node records unmatched `dependencies` and `devDependencies`, and Rust records unmatched crates along with any `git` URL. `graph.Build` copies each unit's `Name` and `Requires` onto the service and package nodes built from it. `discovery.Evidence` gains `Source string`, which is empty in a per-source graph and set by `Compose`. `SchemaVersion` goes to 2.

### Compose

`internal/graph/compose.go` is a pure, deterministic function:

```go
type SourceRef struct {
	Alias, Repository, Root, CommitSHA, AnalysisID string
}

type Part struct {
	Source SourceRef
	Graph  *Graph
}

// Compose joins one graph per source into the application's graph.
func Compose(parts []Part) *Graph
```

`Graph` gains `Sources []SourceRef`, and `Service` and `Package` gain `Source string`. Compose works through the parts sorted by alias, in four steps.

First it applies each source's `root`. Services and packages whose path is outside the root are dropped, except a package that a kept service reaches through `uses` edges. That one stays, so `root: apps/web` doesn't cut `apps/web` off from `packages/ui`.

Then it qualifies every service and package ID with the alias, rewrites the edges to match, and sets `Source` on each node and its evidence.

Then it merges resources by kind. The evidence is unioned, the highest confidence wins, and `NeedsConfirmation` stays true only if every source's evidence for it was weak.

Last, it links across repositories. It indexes every node by `(ecosystem, name)` across all sources, and for each `Requires` entry that names a node in a *different* source it adds a `uses` edge:

- Certain, when the match is structural: a Rust `git` URL naming that source's repository, or a Go module path under `github.com/<that repository>`.
- Likely, when only the name matches, as with npm packages.
- Nothing, when two sources publish the name. A note says which sources and which name instead.

The edge goes to the library's package node, or to the unit's services if it built no package. Workspaces, build systems and notes are concatenated and tagged with their source.

An analysis from before this change has no `Requires`. It still composes, just without cross-repository edges, and Compose adds a note telling you to re-analyze that source.

### Where the composed graph comes from

Nothing stores the composed graph. The project's current graph is `Compose` over the latest succeeded analysis of each live source, and a plan's graph is `Compose` over its pinned analyses. `ServiceCatalog` (which validates variables), the planner and `GET /projects/:id/graph` all call the same function, so a composed graph cannot go stale.

## Analysis

`discovery.Service.Start` takes a source. The API becomes:

```
POST /api/v1/projects/:id/analyses  {"sources": ["api"], "ref": "release-2.4"}
```

Leaving out `sources` analyzes every live source, one analysis and one job each, so they run in parallel on the existing `loomplane-analysis` queue. `ref` overrides the tracked branch and is only allowed with exactly one source. The job payload is unchanged.

The runner still fetches the whole repository and runs every detector over all of it. The tarball endpoint GitHub gives us cannot fetch a subtree anyway, and workspace files (`go.work`, `pnpm-workspace.yaml`, the Cargo workspace) live at the top. If we narrowed the checkout to `apps/web`, the pnpm detector would never see `pnpm-workspace.yaml` at the top, would treat every `workspace:*` dependency in `apps/web/package.json` as something outside the checkout, and the edge from the web app to `packages/ui` that you'd expect to see in the dashboard would quietly disappear. So `root` is applied in `Compose`, and evidence paths stay relative to the repository, which keeps GitHub links working.

Adding a source checks access straight away, through `Connections.Token` and `Resolve` on the tracked ref. A repository no installation covers fails when you add it, with `source-inaccessible`. When one source's analysis fails later, only that source is affected: `Compose` builds from what succeeded and adds a note naming the source that's missing.

Source endpoints:

```
GET    /api/v1/projects/:id/sources
POST   /api/v1/projects/:id/sources
PATCH  /api/v1/projects/:id/sources/:alias     ref and root only
DELETE /api/v1/projects/:id/sources/:alias     sets removed_at
GET    /api/v1/projects/:id/graph              the composed graph, and each source's latest SHA and analysis state
```

Creating a project with a repository URL still works, and creates the first source.

## Planning

### Locksets

Creating a plan builds its lockset from the scope. An empty scope means every live source.

- An in-scope source pins its latest succeeded analysis. If it has none, planning fails with `no-analysis`.
- An out-of-scope source pins the environment's row in `loomplane_environment_sources`. If there's no row, planning fails with `never-deployed`, since you can't hold back something that was never deployed.

`Plan.AnalysisID` and `Plan.CommitSHA` become `Sources []PlanSource{Alias, SourceID, AnalysisID, CommitSHA, InScope}`. `revisionInput` digests the lockset sorted by source ID in place of the single pair. The audit event records the lockset. One open plan per environment stays, and a scoped plan supersedes an open full plan the way any new plan does.

### Computing

`compute` loads the pinned analyses and composes them, so recomputing a plan always yields the same graph. `planner.Input` keeps its single `*graph.Graph`, and the planner learns about federation in exactly three places.

Matching changes first. `match` narrows the graph's services to those whose `Source` equals the manifest service's `repo`, then applies today's rules. When `repo` is empty and the project has one source, that source is used. When `repo` is empty and there are more, it's an `ambiguous-repo` finding. The not-found message names the source and commit: "Analysis found no service at cmd/server in api@3f2c1a9."

A service whose repository is out of scope is held, which means it gets no action, even if the manifest changed it since it was deployed. If the manifest would change it, the planner adds a `held` warning saying the scope holds those changes back. Resources belong to no repository and are always planned, and the existing `needs` links already stop a resource being removed while a held service still needs it.

The last change is about impact. When an in-scope package has `uses` edges from services in out-of-scope sources, the planner adds a `cross-repo-impact` info finding naming them. It never widens the scope by itself. Builds resolve those dependencies through registries at whatever version each repository declares, so the edge tells you something might be affected and is not a build input.

### What the executor gets

`ServiceConfig` gains `Repo` and `CommitSHA`, so milestone 4's executor knows which tarball to build each service from. After a successful deployment it writes one `loomplane_environment_sources` row per in-scope source. Nothing else on the deployment path needs to know about federation.

Bindings stay keyed by manifest name per environment, which is already unique across the project. Variables keep keying by service ID, now alias-qualified, and are validated against the composed catalog.

## Dashboard

The project page swaps its single-repository header (`pages/project-detail.tsx:210-273`) and repository row (`:521`) for a sources panel. Each row shows the alias, repository, root, ref, latest SHA and analysis state, and has an Analyze action, with Analyze all at the top.

`ConnectRepository` (`components/architecture.tsx:300`) opens an add-source dialog in place of PATCHing `repositoryUrl`. The dialog uses the existing `useGithubRepositories` hook as a picker, fills the alias from the repository name, takes an optional root and ref, and shows the access check inline. The new-project dialog keeps its one repository field, which becomes the first source.

`ServiceGraph` draws one cluster per source and gives cross-repository `uses` edges their own style, dashed when Likely. `GraphCaption` shows a chip per source (`web@3f2c1a9 · api@9a1b02e`) in place of one `repository@sha`. `evidenceURL` and `commitURL` (`lib/architecture.ts:92`) look up `evidence.source` in `graph.sources`. The services table gets a Repo column, analysis history can be filtered by source, and the command menu gets "Analyze source…".

There's no plan screen yet, only mocks, so the scope picker waits for one. The API takes `scope` from the start.

## Errors

| Code | When |
|---|---|
| `source-inaccessible` | No installation covers the repository you're adding |
| `alias-taken` | A live or removed source in the project already used the alias |
| `alias-reserved` | The alias is an ecosystem name |
| `roots-overlap` | The same repository is attached twice with nested roots |
| `no-analysis` | An in-scope source has no succeeded analysis |
| `never-deployed` | An out-of-scope source has nothing deployed to the environment |
| `ambiguous-repo` | A manifest service has no `repo:` and the project has more than one source |
| `held` | Warning: the scope holds back manifest changes to a service |
| `cross-repo-impact` | Info: a package this plan moves is used by services in held sources |

## Testing

- `graph.Compose` gets table-driven golden tests: qualification, root filtering that keeps a used package outside the root, resource merging, Certain edges from a Go module path and a Rust git URL, a Likely edge from an npm name, an ambiguous name that gives a note and no edge, and a version 1 graph that gives a note.
- Each detector gets a fixture showing that unmatched dependencies land in `Requires` with the right line in the evidence.
- A pnpm workspace fixture analyzed with `root: apps/web` keeps its `packages/ui` dependency.
- Planner tests cover matching by `repo`, `ambiguous-repo`, a held service whose manifest entry changed, the impact finding, and a lockset digest that doesn't depend on the order sources were listed.
- `tests/integration/platform_test.go` covers the migration from `repository_url`, including the variable rewrite. `tests/integration/planning_test.go` covers scopes end to end: `never-deployed` while `loomplane_environment_sources` is empty, and a scoped plan once it has a row.
- `lib/architecture.ts` URL builders get unit tests with two sources.

## Rollout

Four phases, each shippable on its own.

1. Sources: the table, the migration, the source endpoints, and analyses per source. A project with one source behaves as it does today except that its IDs carry an alias.
2. Federation: `Requires` in the detectors, `graph.Compose`, root filtering, and `GET /projects/:id/graph`.
3. Planning: locksets, scopes, held services, the impact finding, and `loomplane_environment_sources`.
4. Dashboard: the sources panel, the add-source dialog, the clustered graph and links per source.

Phase 1 changes the data model without changing behaviour. If the migration is wrong, you find out while every project still has exactly one source, which is the easiest state to check.

## Not in this design

- Webhooks, which are still off (`docs/github-app.md:16`). The index on `repository` turns "a push to owner/name at ref" into one lookup when we add them.
- Reusing an analysis when the SHA has not moved. That is only safe once detectors carry a version, because otherwise a fix to a detector would never reach a graph we had already cached.
- Manifests per repository. One project manifest covers it for now.
- Hosts other than GitHub. `ParseGitHubURL` still decides what a repository is.
- Resolving versions across repositories. Cross-repository edges record the declared version as evidence and do not check it against the pinned commit.
