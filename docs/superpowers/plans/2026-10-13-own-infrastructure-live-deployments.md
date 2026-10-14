# Own Infrastructure and Live Deployments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking. Execute in the current primary checkout under the user's instruction to continue until the full plan is done.

**Goal:** Deploy Forge services to chosen SSH/Docker servers and Kubernetes clusters, collect supported provider build logs, and keep dashboard state current through generated Forge WS/SSE clients with durable recovery.

**Architecture:** Extend the existing manifest, connection and executor contracts with per-service target references and remote image builds. Store authoritative events and build output in Postgres, expose Forge routes with stream bindings, and feed the generated client into the existing normalized cache. Preserve managed targets, pinned repository locksets and replacement ownership.

**Tech Stack:** Go, Forge, Grove/Postgres, Vault, Dispatch, Ctrl Plane, SSH, Docker API, Kubernetes client-go, OCI registries, React, Forge client-core/client-react, kind.

**Spec:** `docs/superpowers/specs/2026-10-13-own-infrastructure-live-deployments-design.md`

## Global Constraints

- Work in the primary `main` checkout. No task branches, additional worktrees, broad staging, cleanup or stash.
- Store credentials in Vault; organization scope applies to every connection and event.
- Builds execute on connected infrastructure. Runtime secrets stay out of build inputs and persisted Ctrl Plane specs.
- Use Forge routes, AsyncAPI metadata, generated WS/SSE clients and client-core realtime/cache primitives.
- Preserve source aliases, locksets, held sources, current managed targets and old-plan compatibility.
- Run `make lint`; format owned Go files with `gofmt` and dashboard files with `pnpm --dir apps/dashboard format`; inspect formatting, rerun lint and tests/build before exact-path commits and pushes. This repo has no `make l` or `make f` targets.
- Use rex-voice and humanizer for repository prose. No em dashes or co-author/tool attribution.
- All completion claims require current evidence. Keep the goal active while any required task or acceptance check is incomplete.

## Review Focus

1. Two services use different connections on one provider: execution must not collapse them to one environment default. Task 2 pins and Task 7 exercises this.
2. A connection rotates credentials but also changes placement: execution must accept the rotation and reject changed endpoint/namespace/host key until replanned. Tasks 3 and 7 cover it.
3. Commit order differs from allocated event IDs: replay must not skip a transaction. Task 4 uses an organization cursor row lock and tests concurrent commit order.
4. An old REST response arrives after a streamed update: the normalized cache must retain the newer entity and resynchronize after gaps. Tasks 5 and 10 cover it.
5. A provider repeats identical log text or delays final lines: collection must retain legitimate repeats, deduplicate provider IDs and drain the final range. Tasks 4 and 9 cover it.

## Task 1: Manifest placement and build contracts

Files: modify `pkg/manifest/manifest.go`, `pkg/manifest/schema.json`, `pkg/manifest/edit.go`; create `pkg/manifest/infrastructure_test.go`.

Interfaces produced:

```go
type Endpoint struct {
    HostPort int `json:"host_port,omitempty" yaml:"host_port,omitempty"`
    Hostname string `json:"hostname,omitempty" yaml:"hostname,omitempty"`
    IngressClass string `json:"ingress_class,omitempty" yaml:"ingress_class,omitempty"`
    TLSSecret string `json:"tls_secret,omitempty" yaml:"tls_secret,omitempty"`
}
// Settings gains Target string, Endpoint *Endpoint, HealthPath string.
// Build gains Builder string, Registry string, Image string.
// Placement gains Target *string and build/endpoint edits used by the form.
```

- [x] Write parsing/inheritance/edit tests. Parse a server target, a Kubernetes environment override, builder/registry references and a digest-pinned image; reject invalid IDs, host ports, paths and unpinned image references. For an environment provider change, inherited target/endpoint values from the previous provider must clear.
- [x] Run `go test ./pkg/manifest -run 'Infrastructure|Target|Build' -count=1`. Expected: the new manifest is refused for unknown fields, or inheritance/edit assertions fail.
- [x] Add typed fields, validation, schema definitions and comment-preserving edits. Preserve default behavior when fields are absent.
- [x] Run `go test ./pkg/manifest -count=1`. Expected: all cases pass, including existing alias/merge edits.
- [x] Format, lint, run `make test`, inspect/stage owned files and commit `feat: add owned infrastructure manifest settings`.

## Task 2: Per-action connection pinning and planner validation

Files: modify `internal/planning/plan.go`, `planner.go`, `service.go`, `internal/deployments/runner.go`, `internal/providers/catalog.go`; create `internal/planning/targets.go`, `targets_test.go`.

Consumes Task 1 fields. Produces `Action.ConnectionID`, `ServiceConfig.Target`, `ServiceConfig.Endpoint`, `ServiceConfig.HealthPath` and `ServiceConfig.BuildTarget`, plus a serializable build configuration holding builder/registry/image and reviewed connection settings. `ConnectionOption.Details` supplies safe checked settings. Action/configuration references are covered by the plan digest.

- [x] Write a real Compute regression with two services and two ready server connections. Required result:

```go
got := Compute(Input{Environment: "production", Graph: graphFixture,
    Manifest: twoTargets, Providers: targets, Connections: connections})
if len(got.Problems) != 0 || got.Actions[0].ConnectionID != "conn_a" ||
    got.Actions[1].ConnectionID != "conn_b" { t.Fatalf("targets: %+v", got) }
```

- [x] Add cases for a missing/wrong-cloud/not-ready target, wrong builder role, missing Kubernetes registry, host-port collision, disconnected private dependency, unsupported server scale and public cluster service without ingress. Hold a deployed source while another source changes target; only the moving source may be replaced.
- [x] Run `go test ./internal/planning -run 'Target|Infrastructure' -count=1`. Expected: unresolved fields or target validation failures.
- [x] Resolve explicit targets independently of managed environment defaults, snapshot placement settings, compare target/build/endpoint changes and route runner calls through each action's connection. Preserve fallback for older actions and original credentials for deletes/replacements. Keep new providers unavailable until their checkers/executors are wired.
- [x] Run `go test ./internal/planning ./internal/deployments -count=1`, then format/lint/full tests and commit `feat: pin deployment targets per service`.

## Task 3: SSH, Kubernetes and registry connections

Files: create clients under `internal/clouds/server`, `internal/clouds/kubernetes`, `internal/clouds/registry`; create `internal/connections/infrastructure.go` and tests; modify connection constants, checkers, service deletion protection and `extensions/connections/extension.go`.

Produces connection checkers implementing the existing `Parse([]byte, map[string]string) (Check, error)` and `Check(context.Context, []byte, map[string]string) (Check, error)` contract. SSH clients expose a pinned-key authenticated Docker HTTP transport. Kubernetes configuration accepts only self-contained credentials. Registry clients expose authenticated push/pull metadata.

- [x] Write checker tests using real parsers and a local SSH/API fixture: an unexpected host key is refused; kubeconfig with exec/file references/insecure TLS is refused; two namespace checks remain isolated; denied object permissions produce incomplete status; rotated credentials preserve identity.
- [x] Run `go test ./internal/clouds/server ./internal/clouds/kubernetes ./internal/clouds/registry ./internal/connections -count=1`. Expected: missing clients/checkers or refusal assertions fail.
- [x] Implement bounded dials, cancellation, SSH authentication and Docker transport; explicit Kubernetes REST configs and permission checks; registry credential parsing/checking. Never load ambient credentials. Delete protection consults active plan references and bindings.
- [x] Run the same focused tests, format/lint/full tests and commit `feat: connect servers clusters and image registries`.

## Task 4: Durable event and build-log storage

Files: create `internal/realtime/{event,pgstore,redact}.go` and tests; add Loomplane migration files in the existing migration group; modify planning, deployment, connection and analysis stores to append events with state writes.

Interfaces produced:

```go
type Event struct { ID, OrgID, ProjectID, Type string; Data json.RawMessage }
type LogInput struct {
    OrgID, ProjectID, DeploymentID, ActionID, Attempt, ProviderID, Stream, Message string
    Time time.Time
}
// PgStore implements forge.EventLog and authorized cursor-paged reads.
// State writers append within their existing database transaction.
```

- [x] Write PostgreSQL tests with two independent connections. Hold the cursor lock in one transaction while another writer starts; replay must return both committed events in order. Roll back a state write and require no event. Deduplicate the same provider ID, retain two identical lines with different provider IDs, and verify truncation/redaction.
- [x] Run `LOOMPLANE_TEST_DATABASE_URL=... go test -tags=integration ./internal/realtime -count=1`. Expected: missing store or ordering/record assertions fail.
- [x] Add the organization cursor, durable event rows, bounded log records, retention and literal-secret redaction. Use Postgres transactions for authoritative state events. Implement producer-owned replay and Conduit wake-up delivery, with durable catch-up after a missed wake-up.
- [x] Run focused integration tests, existing integration suite, format/lint/full tests and commit `feat: persist deployment events and build output`.

## Task 5: Forge WS/SSE routes and generated clients

Files: create `extensions/realtime` routes/extension/tests; wire it in `internal/platform/platform.go`; modify `apps/dashboard/.forge-client.yaml`, spec scripts, package generation commands and `vite.config.ts`; regenerate owned client/spec artifacts.

Consumes Task 4's events and EventLog. Produces `/api/v1/realtime/ws`, `/api/v1/realtime/events`, `/api/v1/deployments/:id/logs`, named Forge event contracts and generated stream clients/bindings.

- [x] Add live route tests for authentication, cross-organization denial, project log permission, WebSocket origin, cursor replay, expired cursor gap, slow consumer cancellation and cookie session revocation.
- [x] Run `go test ./extensions/realtime -count=1`. Expected: missing routes or contract assertions fail.
- [x] Register Forge WebSocket/EventStream routes with `WithStreamBinding`, `Emits`, WS/SSE message schemas and producer EventLog. Give snapshots an atomic high-water cursor; use identical entity/log payloads across transports. The WS envelope uses `event`, `data`, cursor and channel metadata.
- [x] Save OpenAPI/AsyncAPI documents, enable streaming for the Loomplane client, generate clients and validate snake_case/camelCase event decoding with real wire fixtures.
- [x] Run route tests, dashboard typecheck/tests, `pnpm --dir apps/dashboard check:client`, format/lint/build and commit `feat: expose Forge deployment realtime streams`.

## Task 6: Remote image builds and Forge Dockerfiles

Files: create `internal/deployments/imagebuild.go`, `imagebuild_test.go`, `internal/deployments/forgefile.go` and tests; extend Job with log emission and build credential access.

Consumes resolved BuildTarget and server/registry clients. Produces a digest-pinned artifact and checkpoint containing attempt, operation and image. A builder accepts only stored source artifacts from the approved lockset.

- [x] Test real tar/context processing: nested Forge app modules, go.work context, existing Dockerfile outside its Compose context, excluded examples and explicit example builds. Require source markers from both build streams, bounded lines, no runtime secrets in the build request, artifact reuse after checkpoint and distinct retry attempts.
- [x] Run `go test ./internal/deployments -run 'ImageBuild|Forgefile' -count=1`. Expected: missing build behavior.
- [x] Implement remote Docker build through SSH, build-output decoding, safe tar paths/context, registry push and final digest/checkpoint. Generate a Go Dockerfile from the detected Forge entrypoint when one is absent; fail unsupported contexts before rollout.
- [x] Run focused tests, format/lint/full tests and commit `feat: build Forge images on connected infrastructure`.

## Task 7: Server and Kubernetes executors

Files: create `internal/deployments/server.go`, `kubernetes.go` and tests; create connection-specific Ctrl Plane adapters under `internal/clouds/server` and `internal/clouds/kubernetes`; modify provider catalog and planning extension wiring.

Consumes Tasks 1-3 and 6. Produces the existing Executor Apply/Remove/Observe contract for `server` and `kubernetes`, integrated with Ctrl Plane's lifecycle and owned bindings.

- [x] Test Apply/Remove/Observe against strict Docker/Kubernetes API fixtures: ownership conflicts, ports, namespace, runtime secret references, fixed replicas, readiness timeout, failed replacement and retry cleanup with the old credential. Test placement change after approval and credential-only rotation separately.
- [x] Run `go test ./internal/deployments ./internal/clouds/server ./internal/clouds/kubernetes -count=1`. Expected: missing adapters or lifecycle assertions fail.
- [x] Implement one-container server lifecycle and namespace-scoped Deployment/Service/Secret/Ingress lifecycle. Support requested HPA only after permissions/metrics capability checks. Verify readiness, record digest revision and private/public addresses, and enforce reviewed settings before mutation.
- [x] Advertise providers available only after all executor/checker wiring exists. Run focused tests, format/lint/full tests/build and commit `feat: deploy to owned servers and Kubernetes`.

Verified: strict Docker and TLS Kubernetes API fixtures, PostgreSQL port reservations, worker readiness, HPA stop/start, rollout recovery and scoped Vault cleanup pass. `make lint`, `make test`, `make test-integration` (137.873s), `make build`, generated-client checks and focused race tests pass. Independent review findings were fixed with regressions. Live SSH/Docker and kind verification remains in Task 11.

## Task 8: Optional provider-plugin operation logs

Files: extend `pkg/provider/sdk` protocol/server and `internal/plugins` client; modify `internal/deployments/plugin.go`; add SDK/client/integration tests.

Produces an optional advertised log capability and cursor-paged operation log response. Existing v1 plugins with no capability remain compatible.

- [x] Run a real SDK handler with an operation that emits two identical lines with distinct IDs. Read pages, retry the cursor and restart the handler; require deduplication and an explicit restart gap. Run an old plugin that only reports progress.
- [x] Run `go test ./pkg/provider/sdk ./internal/plugins -count=1`. Expected: new capability/log endpoint absent.
- [x] Add optional capability discovery, bounded operation output, cursor pages and the runner collector. Persist output before browser delivery and retain progress fallback for old plugins.
- [x] Run focused/plugin integration tests, format/lint/full tests and commit `feat: collect provider plugin build logs`.

Verified: real SDK output, stable replay IDs, provider restart gaps, bounded final catch-up and legacy progress compatibility pass. Focused race tests, make lint/test/build, client drift and full integration (116.364s) pass. The final recovery review found no remaining material issue.

## Task 9: Managed integration build-log readers

Files: extend clients/executors for Vercel, Cloud Build, CodeBuild and Render; add strict HTTP/WebSocket fixture tests and the log capability catalog.

Consumes Task 4 storage and Task 6 log emission. Produces collectors scoped to a specific build/deploy operation, with provider cursor checkpoints and terminal catch-up.

- [x] Test Vercel live-follow frames, Cloud Logging entry IDs, CloudWatch forward tokens and Render build-log filtering. Test 403 log permission while deployment succeeds, rate-limit retry, delayed final output and restarting a collector from its checkpoint.
- [x] Run focused cloud/executor tests. Expected: raw build output is absent or incorrect cursor behavior.
- [x] Implement supported readers and declare streamed/polled/unavailable capabilities accurately. Verify Render build-log API scope before declaring support; runtime logs are not build output. Keep log-reader failure separate from deployment failure.
- [x] Run focused/full tests, format/lint/build and commit `feat: stream managed provider build logs`.

Verified: Vercel live-follow frames, Cloud Logging insertId pagination, CodeBuild forward-token replay, delayed final output and durable collection recovery pass. The real app test preserves deployment success when log access is denied. Render stays unavailable because its published build-log API cannot isolate one deployment. Format, make lint/test/build, focused race tests, client generation/drift and full integration (165.347s) pass. Live Vercel acceptance remains in Task 11.

## Task 10: Dashboard placement, infrastructure and live cache

Files: modify `components/deployment-targets.tsx`, `pages/infrastructure.tsx`, integration connection forms, `lib/use-runs.ts`, deployment page/log adapters and the root client provider; add `lib/realtime.ts` and focused UI tests.

Consumes generated Task 5 bindings and Tasks 1-3 placement fields. Produces a shared organization-scoped SubscriptionManager/StreamBinder connection, WS preference with SSE fallback, REST/gap recovery and compact target/builder/registry controls.

- [x] Write UI tests selecting separate service targets, saving builder/registry/endpoint choices and inspecting persisted request bodies. Replay a newer streamed entity before an older REST reply; require newer status to remain. Test WS loss, SSE fallback, stale label, resume gap, organization switch and read denial.
- [x] Run the new Vitest files. Expected: controls/stream bindings/status recovery absent.
- [x] Implement controls with the shared component system and ZeroState adapter. Wire generated streaming transports and bindings into the existing cache, replace live two-second polling with stream updates and disconnected REST fallback. Render persisted build logs with bounded follow/pause/filter/export behavior.
- [x] Regenerate/check clients, format/lint/full tests/build and commit `feat: show owned targets and live deployment output`.

Verified: separate per-service targets, builder/registry/endpoint saves, context inheritance, WS/SSE fallback, REST fencing, projection refresh, replay cursor monotonicity and denied-read cleanup pass. The signed-in infrastructure page reports Live via Forge WS. Desktop and 390px screenshots are saved, and the narrow page has no horizontal overflow. Format, make lint/test (209 dashboard tests)/build and generated-client checks pass. The real deployment log view and remaining live scenario matrix are Task 11.

## Task 11: Local Docker/SSH and kind acceptance

Files: create `tests/acceptance/infrastructure` harness/fixtures and scoped cleanup scripts; add `make acceptance-infrastructure`; update `docs/acceptance.md` and operations/provider guidance.

Consumes all prior tasks. Produces a repeatable real infrastructure run with recorded ownership, isolated database, TLS kubeconfig, SSH Docker builder, OCI registry and browser screenshots.

- [x] Create a uniquely named kind cluster/namespace, loopback registry and SSH/Docker builder fixture. Record all resources. Use explicit kubeconfig/context throughout. Seed an isolated organization/account and source fixture so the application API and authentication remain real.
- [x] Connect through the product, save targets, review/approve, build, roll out and exercise Forge HTTP/worker fixtures. Verify visible build marker output before completion on WS and SSE, then reload/reconnect to retained logs.
- [x] Run failure/retry, stopped API replica, worker restart, private dependency, denied access, stale cursor and replacement scenarios listed in the spec. Inspect actual container/pod/image/Secret/binding state as well as UI status.
- [x] Check desktop and 390px layouts and save screenshots. Clean only recorded test-owned resources, then verify their absence and survival of an unrelated sentinel resource.
- [x] Run `make acceptance-infrastructure`. Expected: all assertions pass, report names the source-fixture boundary and cleanup leaves no test deployment.
- [x] Run normal format/lint/tests/build/client gates, commit harness/docs and push.

Verified: final unattended run `loomplane-e2e-b4e249e419` passed in 738.05s. Real SSH/Docker, OCI and Kubernetes builds and rollouts, authenticated cold-node pull, API/worker loss, readiness failure, build failure/retry, access revocation, organization isolation, cursor expiry and truncation all passed. Product removal and fixture cleanup preserved 23 baseline containers and the ambient kubeconfig. The browser run passed separately with desktop/390px views, live WS/SSE stdout, second-browser updates and organization switching. Live Vercel retained 93 provider lines, served the interactive counter and was removed successfully. Format, lint, 227 dashboard tests, full Go tests, integration (162.637s), build, generated-client drift and Python fixture checks pass.

## Task 12: Completion audit and independent review

- [ ] Run `make lint`, `make test`, `make test-integration`, `make build`, `pnpm --dir apps/dashboard check:client` and the infrastructure acceptance harness after final edits. Inspect every exit status/output.
- [ ] Obtain one independent whole-change review using the requesting-code-review skill, with the spec, plan, Review Focus and ledger rulings. Resolve important findings with a failing regression and passing relevant/full checks.
- [ ] Match every spec completion criterion and E2E matrix row to current source, test output or live rendered/resource evidence. Missing evidence keeps this goal active.
- [ ] Commit/push exact owned files, verify remote SHA and clean/concurrent status, update this plan with evidence and report provider/environment verification limits accurately.
- [ ] Mark the goal complete only after the full audit passes.
