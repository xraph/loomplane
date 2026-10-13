# Forge integration

This is the record of every Forge ecosystem library Loomplane builds on: the version you get, what Loomplane uses it for, how it's wired, and what we found it doesn't do. We checked each claim against the library's source at the pinned version, and most of them against a running Loomplane as well, so where a library falls short you'll find the gap here with its workaround and won't have to rediscover it.

If you change a pin, update this file in the same commit.

## Versions

Go 1.26 (the `go` directive is `1.26.0`; it's tested on go1.26.3).

| Library | Module | Pinned | License at that version |
|---|---|---|---|
| Forge | `github.com/xraph/forge` | v1.12.9-0.20261013040932-c8f5fcc38b7c (main) | Apache-2.0 file; see the note below |
| Grove | `github.com/xraph/grove`, `.../drivers/pgdriver`, `.../extension` | v1.7.3; the extension at v1.7.4-0.20261013042516-c28fa000e1b3 (main) | MIT |
| Authsome | `github.com/xraph/authsome` | v1.7.3-0.20261013045248-b3fd5f9edebc (main) | no license file; README says all rights reserved |
| Warden | `github.com/xraph/warden` | v1.7.2 | Apache-2.0 |
| Chronicle | `github.com/xraph/chronicle` | v1.8.0 | no license file |
| Dispatch | `github.com/xraph/dispatch` | v1.8.1-0.20261013040216-46004e7c0445 (main) | no license file (its SDKs say MIT) |
| Ctrl Plane | `github.com/xraph/ctrlplane` | v1.7.1-0.20261013050604-236f4beb621f (main) | Apache-2.0 |
| Confy | `github.com/xraph/confy` | v1.0.3 | MIT per README, no license file |

The dashboard (`apps/dashboard`) pins:

| Package | Version |
|---|---|
| `@forge-go/client-core`, `@forge-go/client-react` | 1.12.8 |
| `@authsome/ui-core`, `@authsome/ui-react`, `@authsome/ui-components` | 1.7.2, two of them patched |
| shadcn CLI | 4.21.4, preset `b1YmpVjlo` (nova, neutral, Geist) |
| `@base-ui/react` | 1.9, the primitives under every shadcn component |
| React, Vite, Tailwind, TypeScript | 19.3, 8.3, 4, 6.0 |
| pnpm | 11.5.0, declared in `packageManager` |

pnpm 11 refuses any package published in the last 24 hours, and the dashboard keeps that policy on. The Forge client sat at 1.12.7 for a day because of it, since 1.12.8 was too new when the lockfile was made; it's on 1.12.8 now. Expect the same wait whenever you take a Forge release on the day it ships. The `packageManager` field makes any pnpm switch to 11.5.0, so everyone resolves under the same policy.

There are no `replace` directives. tap builds against local checkouts of Forge and authsome; at the time of writing both of those checkouts sit exactly on the commits pinned above, so Loomplane gets the same code from the module proxy.

Authsome is pinned to a commit on its `main` because its latest tag, v1.7.2, imports `forge/extensions/dashboard/contributor`, a package Forge removed in v1.12.0. Nothing in released authsome compiles against Forge 1.12. Move to a tag as soon as authsome cuts one.

### Licenses

This needs sorting out before Loomplane is published as open source. Authsome's README says "Copyright (c) Xraph. All rights reserved." and the module ships no license, which means nobody outside Xraph may legally redistribute it. Dispatch, Chronicle and Confy ship no license file at these versions either. Forge's `LICENSE` is Apache-2.0, while its `LICENSE_FAQ.md` describes MIT for the core and a commercial license for the AI extension. Loomplane uses no Forge AI extension, so the commercial part doesn't touch it, but the two documents should agree.

## How the pieces fit

Loomplane is one Forge app (`internal/platform`). Library code runs inside Forge extensions: the official adapter where it fits, a small Loomplane extension where it doesn't.

| Extension | From | Depends on | Owns |
|---|---|---|---|
| `grove` | `grove/extension` | | The Postgres pool, and the single migration pass |
| `loomplane-identity` | `extensions/identity` | grove | authsome, Warden, Chronicle; the request guard |
| `dispatch` | `dispatch/extension` | grove | The job engine |
| `ctrlplane` | `ctrlplane/extension` | grove | Instance and deployment lifecycle |
| `loomplane-projects` | `extensions/projects` | identity, dispatch | The project API and its jobs |
| `loomplane-infrastructure` | `extensions/infrastructure` | identity, ctrlplane | Ctrl Plane's identity bridge, infrastructure status |
| `loomplane-web` | `extensions/web` | | Serving the built dashboard |

The app runs with Forge's central migrations (`forge.WithCentralMigrations`): Forge registers every extension, runs one migration pass over every library's schema, then starts every extension. Every library's own self-migration is turned off. [architecture.md](architecture.md) walks through the lifecycle.

---

## Forge

Forge runs the app itself: the router, DI through vessel, the extension lifecycle, health at `/_/health`, metrics at `/_/metrics`, OpenAPI at `/openapi.json` (with Swagger at `/swagger` in development), the CLI runner, and the generator behind the dashboard's TypeScript client.

`internal/platform/platform.go` builds the app with `forge.New`. Forge gets the env prefix `LOOMPLANE_FORGE_`, which nobody sets, because Loomplane reads its own `LOOMPLANE_` variables through `internal/config` and the two shouldn't both read them. The CLI is `cli.NewAppRunner` in `cmd/loomplane/main.go`, and that file stays alone in its package because `forge dev` compiles `main.go` by itself and anything in a sibling file comes up undefined.

Routes carry the cache contract the generated client reads: `forge.WithName` sets the operation ID, a `forge:"id"` struct tag marks each entity's ID, list wrappers implement `forge.ForgeEnvelope`, and the delete route declares `forge.WithEntity` and `forge.WithInvalidates("Environment[]")`.

What we found:

- Forge's router is forgemux, and `Router.Handle("/", h)` mounts only the wildcard `/*`, which doesn't match `/` itself (`internal/router/forgemux/mux.go:84` trims the trailing slash, then fails to parse the empty base). Fixed in Forge (c96a1075), so `extensions/web` mounts its catch-all with `Handle("/")` alone.
- Vessel starts a lazily provided service that has a `Start` method the first time anything resolves it (`vessel/type_registry.go:213`). Dispatch provides its engine and Ctrl Plane its `*app.CtrlPlane` that way. Resolve either from DI while extensions register and you start their workers before the migration pass has made their tables. Loomplane takes them from the extensions' own accessors (`dispatchext.Extension.Engine`, `cpext.Extension.CtrlPlane`).
- The CLI's `migrate` commands look for a `CentralMigrator`, and the grove extension contributes a `MigrationRegistry`, so with central migrations on they report nothing to do. `cmd/loomplane` turns them off. Migrations run on every start.
- A middleware that was nil during Register (authsome builds its own only when its engine starts) made `app.Start` hang instead of failing.
- The client generator types a Go slice of pointers as `T | null[]`, which TypeScript reads as `T | (null[])`. List responses use value slices to avoid it.

Config: `http_addr` (`LOOMPLANE_HTTP_ADDR`). `forge dev -p` and `PORT` override it in development.

Tests: `tests/integration` boots the whole app twice against Postgres.

## Grove

Grove holds the Postgres pool and runs every migration. Loomplane's own store (`internal/projects/pgstore.go`) writes SQL through the pgdriver pool directly.

Loomplane registers the official extension: `groveext.New` with `WithDSN`, a driver factory that opens the pool with Loomplane's settings (pool size, a 30 minute connection lifetime, a 5 minute idle time), `WithMigrations(migrations.All()...)` and `WithCentralMigrations()`. Its CRDT sync routes are off. `database/migrations` lists every group in one place: Loomplane, authsome, Warden, Chronicle, Dispatch and Ctrl Plane.

Limits: migrations aren't transactional, so each Loomplane statement is safe to re-run. Versions are fourteen digits.

Config: `database.url` (`LOOMPLANE_DATABASE_URL`, required), `database.pool_size`.

Tests: the integration tests migrate a fresh database, then start again on it and expect nothing to apply.

## Authsome

authsome handles accounts, sign-in, sessions, organizations and invitations. An organization in authsome is an organization in Loomplane, and a request acts in the session's active organization, which only a member can switch to.

`extensions/identity` builds authsome's engine itself, the way tap's identity extension does, and mounts its API at `/authsome` on a router of its own. It doesn't register authsome's Forge extension, which requires separate Chronicle and Warden extensions in DI, probes DI for Keysmith, Relay, Vault, Ledger and Herald, and runs its own migrations and config loading. The engine starts in Forge's Start phase, after the migration pass, and the seed step creates Loomplane's authsome app on first start. Plugins: password, organization, and Loomplane's Warden sync (`internal/security/sync.go`).

The guard on Loomplane's routes is authsome's session chain as authsome's own extension builds it: authenticate, rotate a token near expiry, slide the session on activity (`extensions/identity/sessions.go`). Without the last two a dashboard login would die at the token's one-hour lifetime.

What we found:

- The tagged v1.7.2 doesn't build against Forge 1.12 (above).
- Several organization handlers write their JSON twice, so a client can't parse the body even though the write succeeded. Fixed in authsome (e371f402); Loomplane's `FirstJSON` workaround is gone.
- Creating an organization with a slug that's taken answers 500, not 409. The dashboard checks the slug first with `GET /authsome/v1/orgs/check-slug`.
- `engine.AuthMiddleware()` is nil until the engine starts, so the guard looks it up per request.
- Most of authsome's hook events (`session.create`, `device.register`, `org.create`, `rbac.role.assign`, `org.invitation.accept`) carry no audit category, and Chronicle refuses an event without one. Every one of them was logged as `audit record failed` and never stored. `extensions/identity/audit.go` hands authsome an emitter that files them under `auth`; an event that names its own category keeps it.
- Sign-up doesn't sign you in; the dashboard sends you to sign in afterwards.
- authsome's own invitation endpoint sets no expiry, so its links never lapse. Loomplane creates invitations itself instead (`POST /api/v1/invitations` in `extensions/identity/members.go`): a week's expiry, no role above your own (authsome checks that too), and the link handed back to you, because nothing sends mail yet.
- Accepting (`POST /authsome/v1/orgs/invitations/accept`) adds the account with the invited email, whoever is signed in. The dashboard asks before accepting and tells you when the invitation went to a different account.
- Removing a member leaves their session alone, still naming the organization. `GET /api/v1/session` answers 403 for someone who isn't a member any more, which the dashboard treats like a session with no organization.

Config: `identity.base_path`, `identity.app_id`, `identity.publishable_key`, `identity.require_email_verification`, and `secret_key`, from which authsome's token encryption key is derived with HKDF (`internal/security/keys.go`).

Tests: `tests/integration` signs up, creates organizations and switches between them over HTTP, and takes an invitation from creation through acceptance to removal.

### authsome's UI SDK

The dashboard's own components are shadcn on Base UI. A shadcn preset code carries no base library, so it's `components.json` (`"style": "base-nova"`) that keeps `shadcn apply` and `shadcn add` on Base UI. authsome's components bring their own Radix primitives with them, which is fine: they're authsome's dependency, not the dashboard's.

The dashboard signs you in with `@authsome/ui-components` (`SignInForm`, `SignUpForm`, `OrgSwitcher`, `UserButton`) over `@authsome/ui-react`. Tailwind scans those components (`@source` in `src/index.css`), so they render in the shadcn preset's tokens. The SDK keeps the session in local storage, because it only restores a session from its own storage.

Two patches, both in `apps/dashboard/patches`:

- `@authsome/ui-core`: tap's patch. It fixes the client config URL dropping the `/authsome` base path, and adds a cross-tab lock around refresh, so two tabs can't spend the same rotating refresh token.
- `@authsome/ui-react`: `useOrganizations` reads `{ items, total }`, but the authsome commit Loomplane pins pages its organization list as `{ organizations, next_cursor }`. Unpatched, `OrgSwitcher` lists nothing. authsome's own SDK source still has the old shape, so this is a bug to fix upstream.

## Warden

Warden makes every authorization decision. Each organization is a Warden tenant holding the role catalog in `internal/security/catalog.go`: owner, admin, developer and viewer, each including everything the one below it may do.

The identity extension builds a `warden.Engine` on Warden's Postgres store and hands it to authsome too. `security.Authorizer` writes the catalog into a tenant, assigns roles, and checks permissions. The authsome plugin `security.Sync` mirrors membership into Warden: a new organization gets the catalog and its creator the owner role, and members get, change and lose their role as authsome adds, changes and removes them. authsome only logs a hook's error, so `Authorizer.Require` also repairs a member who has no role on their next request.

Every check happens in the service layer (`internal/projects/service.go`), not only at the HTTP edge, so the CLI and agents will go through the same checks.

`Authorizer.EnsureTenant` brings an organization's roles up to the current catalog. It used to run only when a role was assigned, so an upgrade that grew a role (milestone 2 gave developers `analysis:run`) never reached organizations whose members already had one. Every check now ensures the tenant first, once per organization per process.

Limits: Warden's own HTTP API isn't mounted.

Tests: `internal/security` runs Warden's real engine over its memory store, including the whole membership lifecycle through the sync plugin. `tests/integration` checks that one organization can't see another's projects.

## Chronicle

Chronicle keeps the audit trail. authsome records sign-ups, sign-ins and organization changes through it, and Loomplane records project creates, updates and deletes in the organization's stream.

The identity extension builds it with `chronicle.New` over Chronicle's Postgres store. authsome gets it through `chronicleadapter`, wrapped so its events have a category (see Authsome above). Loomplane's events set the category, app and tenant explicitly (`extensions/projects/audit.go`, and `member.invite` in `extensions/identity/members.go`), because Chronicle rejects an event without a category. Recording never fails the change it describes: a failed record is logged.

Limits: no read API or audit screen yet.

## Vault

Vault keeps the values of secret variables. Loomplane uses its library, not its Forge extension: `secret.NewService` over Vault's Postgres store (`store/postgres`, on the same grove database) with an AES-256-GCM encryptor whose key is derived from `secret_key`. Vault's migration group runs in the central pass with the others, and its secrets live under their own Vault app, `loomplane-variables` (`internal/environments/vault.go`, wired in `extensions/environments`).

Every `Set` archives the previous value as a version, so changing a secret keeps a history Loomplane doesn't expose yet. Reads, writes and deletes are by key.

What we found:

- Vault's README says tenant isolation is by design and cross-tenant reads are structurally impossible. For secrets they aren't: `vault_secrets` is unique by key and app, with no tenant column, and the store never reads the tenant from the context. Loomplane builds every key as `<organization>/<variable ID>` from its own records, so a caller can't name another organization's secret. Worth fixing upstream, or the README should say secrets are app-scoped only.
- `secret.ListOpts` can't filter by key prefix, so the sweep that removes orphaned secrets pages through the whole `loomplane-variables` app and filters by organization itself.
- Its runtime config has app-wide values with per-tenant overrides. That doesn't reach Loomplane's layers (organization, project, service, each per environment), so the variables themselves are Loomplane's, and Vault holds only the secret values.

Tests: `internal/environments` runs the real Vault secret service over Vault's memory store. `tests/integration` checks the stored secret is encrypted and that deleting an environment or a project leaves no secret behind.

## Dispatch

Dispatch runs Loomplane's background work, on two queues. `loomplane`, which the API process works, has `loomplane.project.initialize`: it gives a new project its production and preview environments, then marks it ready. `loomplane-analysis`, which only `loomplane-worker` works, has `loomplane.repository.analyze`. The API enqueues analyses without registering their handler, which Dispatch allows (`engine.Enqueue` takes a job name), so an analysis can't run in the API process even by accident.

Loomplane registers the official extension with its own queue added (`WithQueues`), and with Dispatch's HTTP routes and self-migration off. The projects extension registers its job on the engine while extensions register; Dispatch only works its queue once everything has started. `internal/orchestration` enqueues every job, because Dispatch ignores the queue, retry and timeout options on a job definition and only honours the ones passed at enqueue.

What we found:

- No job deduplication. Every Loomplane handler is idempotent so a retried or duplicated job does no harm.
- The extension declared no dependency on grove. Fixed in Dispatch (46004e7), so `internal/platform` no longer adds one.
- On a fresh database the engine tries to register its worker row before the tables exist and logs a warning; the heartbeat registers it again a moment later.
- Dispatch has two workflow engines. The older `workflow` package runs a whole workflow synchronously in the caller and resumes every running workflow inside `engine.Start`, so an approval waiting for a day would hold startup. Deployment workflows use the durable runtime (`durable/runtime`), which has leases, durable timers and signals; an approval is a signal. Its handlers have to be given to the extension when it's built, before the planning extension exists, so `internal/platform` builds them (`orchestration.NewDeployments`) and the planning extension binds the planner to them while it registers.
- Every Dispatch app competes for cron leadership, there's no way to opt out, and a stopped app keeps the lease until it expires. Followers only look for a leader every half lease, which by default is 30 seconds. The API runs with a 10-second lease and `loomplane-worker`, which schedules nothing, with 5, so a leader that stops is replaced in seconds and the drift check keeps its schedule.
- The durable worker runs on the context `Start` was given. `forge.App.Run` passes `context.Background()`, so the server is fine, but start the app with a timeout and the worker stops the moment that context is cancelled, with nothing in the logs. Plans then sit in "planning" for good. The integration tests start the app the way `Run` does for this reason.
- The HTTP API needs a Forge auth registry and Warden, and its writes answer 503 unless durable delivery is configured, which is Postgres-only. It's off; job state will be shown through Loomplane's API.

Config: `orchestration.concurrency`, `orchestration.poll_interval`.

Tests: the service tests run the initialization twice and expect no change; the integration tests wait for a real job to make a project ready, and check that an analysis queued with no worker running stays queued.

## Ctrl Plane

Ctrl Plane is registered now so the instance and deployment lifecycle has its owner from the first milestone, though nothing deploys yet.

Loomplane registers the official extension on its own Postgres, with self-migration and the HTTP routes off. The routes stay off because Ctrl Plane's handlers check the tenant but never call the authorization hook, so any member of an organization could change its instances through them. Loomplane will reach Ctrl Plane in-process, with claims built from the caller (`infrastructure.WithPrincipal`). Its auth provider is `infrastructure.AuthProvider`, which resolves authsome sessions and asks Warden, and never hands out `system:admin`, which Ctrl Plane's default development provider gives to any token. `extensions/infrastructure` refuses to start if Ctrl Plane ended up on its in-memory store, which it falls back to with only a warning when it can't find the database.

What we found:

- Only Docker, Kubernetes and Nomad providers exist. There's no Vercel, Cloud Run, ECS or Render provider, despite the docs listing AWS and GCP as "interface defined".
- Instance drift correction is a stub. The reconciler only reconciles datacenter bootstrap services, and the health, telemetry and certificate workers are no-ops. Its scheduler isn't leader-elected, so two replicas run every worker twice.
- `Config.DefaultProvider` is never applied; every create needs a provider or datacenter.
- The extension declared no dependency on grove. Fixed in Ctrl Plane (9068e10), so `internal/platform` no longer adds one.

The provider adapters Loomplane needs are planned in [implementation-plan.md](implementation-plan.md).

Tests: `internal/infrastructure` covers the auth bridge. `GET /api/v1/infrastructure` reports Ctrl Plane's store and providers, and the integration tests check it says Postgres.

## Confy

Confy loads `config.yaml`, `config.local.yaml` and the `LOOMPLANE_` variables, and `internal/config` reads the result into a typed `Config`, the way tap does.

Watch out for confy's quirks: an environment variable can't reach a snake_case key by name, so each one is wired in the YAML as `"${LOOMPLANE_X:-}"`; YAML `yes`, `no`, `on` and `off` become booleans even when quoted; and any key with `url` in its name logs an error when it's empty, which is why `public_url` carries its default in the file.

Tests: `internal/config`.

## Trove

`internal/artifacts` keeps each deployment's sources, one tarball per repository it moves at the commit its plan pins, in a Trove bucket (`storage.url`: `file://` on disk, `mem://` in tests). Builders read it back from there, so every builder gets the same bytes.

## Bastion

Loomplane doesn't run Bastion itself. A `gateways:` entry in `loomplane.yaml` becomes a small Go program on Bastion (`internal/gateway`), generated with the routes baked in and built on the cloud's own builder like any service, so application traffic never passes through Loomplane. Each route's target arrives in a variable, the way any service finds what it needs. The program pins Bastion and Forge at their main commits, with its own `go.sum`; the embedded files are named `.txt` so Go doesn't treat them as part of this module, and one test builds the generated program with `-mod=readonly`, and another runs it in front of two upstreams and checks what each receives.

What we found wiring it, and where it stands:

- `bastion.New` registered routes but never wired the proxy, and `bastion/extension.New` pulled in Mongo, Postgres and SQLite stores. Bastion now has `gateway.New`, which proxies without the stores, and the program uses it.
- The admin REST API has no authentication of its own. It's only registered when the dashboard is enabled, so the gateway disables the dashboard, OpenAPI aggregation and discovery. Bastion can now require a token for it (`WithAdminToken`), but the gateway has no use for the API at all.
- Active health checks are on by default against `/_/health`. An app that doesn't serve that path gets its route taken down after three probes, so the gateway turns health checks off: each route has one target, already load balanced by its cloud. Bastion now only fails a probe on a server error unless you set a path, but there's still nothing to fail over to, so they stay off.
- Forge's old router trimmed a trailing slash before Bastion saw the request, so `/docs/` reached the service as `/docs`. Forge's current router keeps it and Bastion now forwards what the client sent, so the program's hook that put it back is gone.
- On Forge's current router, the catch-all at `/*path` didn't answer `/` itself, so a route at the root 404ed there. Fixed in Bastion (23c004a); the test that runs the gateway is what found it.
- Retries are on by default now: a GET, HEAD or OPTIONS that gets a 502, 503 or 504 is retried, up to three attempts and within a budget of 20% of a target's requests.

FARP isn't used: a gateway's routes come from the manifest, not from live discovery.

## Keysmith

Loomplane's API keys, for the CLI, CI and scripts, are Keysmith keys (`internal/apikeys`), on Keysmith's Postgres store, migrated with everything else. A key is `lp_live_` and 64 hex characters, shown once, stored as a hash. Keysmith's tenant is the organization and its app the authsome app; the member who made a key is its `CreatedBy`, and whether it may write is in its metadata, since Keysmith refuses scopes nobody registered.

A key acts as its maker, in its organization: the identity guard turns it into the same `security.Principal` a session gives, so every route's Warden check applies unchanged, and a member who leaves takes their keys' rights with them. A read-only key fails any permission whose action isn't `read`. Keys can't manage keys. Every key expires, after 90 days unless you choose up to a year.

authsome has API keys of its own, backed by Keysmith when it's given one, but they belong to a user with no organization and need an `X-App-ID` header, which doesn't fit an organization-scoped API. Its middleware only treats `ask_`, `sk_` and `pk_` tokens as keys, so `lp_` never reaches it: the guard checks a Loomplane key before authsome sees the request.

## Evaluated, not wired yet

| Library | When |
|---|---|
| Relay, Herald | When deployments exist to notify about |
| FARP | Live API discovery, if gateways ever route by schema instead of by path |
| Forge dashboard extension | Possibly, for an operator view of Dispatch and Ctrl Plane. The product dashboard is `apps/dashboard`. |

## The dashboard's client

`apps/dashboard` calls Loomplane and authsome only through clients `forge client generate` writes from `specs/` (`.forge-client.yaml`): one for `/api`, one for authsome's organization endpoints. Everything under `src/client/forge` is generated; `pnpm check:client` fails when it drifts. `src/client/runtime.ts` routes each path to its client, puts the session token on every request, and renews the session through the authsome SDK on a 401. Components use the generated bindings through `useQuery` and `useMutation` from `@forge-go/client-react`, over one normalized cache keyed to the user and organization.

To regenerate after a backend change, run Loomplane, then `make gen-client`.

A Base UI `Button` given `render={<a />}` keeps `role="button"`, which is wrong for something that navigates. For a link that looks like a button, put `cn(buttonVariants())` on a real `<a>` or `Link`; `cn` matters, because it's what lets the variant's border win over the base style's.

Two things to know when you add an endpoint:

- The cache is shared by both clients and keyed by type name and ID. authsome already has `Member` and `Invitation` entities, so Loomplane's are `OrgMember` and `OrgInvitation`. Reusing a name lets one API's record overwrite the other's, and the role field means different things in each.
- The generator only types a mutation's result when the result is the entity. `invitations.create` returns the invitation plus its link (`forge.WithoutEntity`), so its binding returns `unknown` and the dashboard casts it to `CreatedInvitation`.

CI installs the forge CLI at a pinned commit (`FORGE_CLI` in `.github/workflows/ci.yml`), because the CLI has no tags yet. Move the pin when you regenerate with a newer CLI.
