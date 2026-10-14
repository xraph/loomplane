# Infrastructure deployment acceptance

Date: October 14, 2026

You can connect an SSH/Docker server or Kubernetes namespace, choose a target for each Forge service, review the destination, and deploy. Source builds use a connected Docker builder and an OCI registry when the target needs one. Existing digest-pinned images skip the build.

This record covers the approved [design](../specs/2026-10-13-own-infrastructure-live-deployments-design.md) and [implementation plan](2026-10-13-own-infrastructure-live-deployments.md). The repeatable commands and fixture setup are in [acceptance](../../acceptance.md); connection requirements are in [providers](../../providers.md).

## Runs and checks

- Browser infrastructure run `loomplane-e2e-a3d82aab68`: passed in 2360.03 seconds, including manual browser checkpoints. The second API replayed 668 distinct build lines without repeating the acknowledged marker. Cleanup completed and preserved all 23 containers recorded before setup.
- Final unattended infrastructure run `loomplane-e2e-b4e249e419`: passed in 738.05 seconds from the final code. The second API replayed 281 distinct build lines. Real readiness and build failures, retries, access boundaries, cursor recovery and product-driven removal all passed. Fixture cleanup preserved all 23 baseline containers and the ambient kubeconfig.
- Live Vercel: public `aurorascharff/astro-demo` at `71eae662d03453c3b8333f59de368783828395d0`, deployment `dep_01m4xzp8exemtrstav1ffrk2ar`, retained 93 provider build lines. The Islands counter changed from zero to one. Removal `dep_01m4y08pxtehsbtb2z3f8kj8pt` succeeded, its binding disappeared and the old address returned `404 DEPLOYMENT_NOT_FOUND`. The test project remains with its production service disabled.
- Final repository checks: `make lint`, `make test` (227 dashboard tests), `make build`, and `pnpm --dir apps/dashboard check:client` passed. Full integration passed in 162.637 seconds on the serialized final rerun. Formatting uses owned Go `gofmt`/`goimports` and `pnpm --dir apps/dashboard format`; this repository has no `make l` or `make f` target. Python fixture checks use `uvx ruff check` and `uvx ruff format --check`.
- Independent review covered the full change and follow-up fixes. No Important findings remain. Regressions first reproduced lost log tails, snapshot races, registry challenge forwarding, orphan port reservations, stale retry timestamps and temporary credential reads after failed-rollout cleanup.

## Scenario evidence

| Approved scenario | Evidence |
| --- | --- |
| Connect server and namespace | Real SSH authentication, pinned host key, Docker identity and TLS Kubernetes checks persisted ready connections through the application API. Connection tests cover credential redaction, rejected host keys and unsafe kubeconfigs. |
| Different service targets | The saved manifest and reviewed plan named the Docker server, Kubernetes connection, builder and registry. Per-action target tests cover multiple connections and organization scope. The rendered plan showed all selected destinations. |
| Build and roll out to kind | Real Docker builds and OCI pushes produced digest-pinned images. Two Kubernetes replicas became ready, and the Forge HTTP response confirmed the runtime secret was present. A second image absent from the node cache was pulled from an authenticated registry through the selected external pull Secret. |
| Deploy to SSH/Docker | Three owned containers ran on the selected daemon. The reviewed public port answered and the worker reported `PRIVATE_DEPENDENCY_READY` over the owned private network. |
| Follow build output | Actual `LOOMPLANE_BUILD_MARKER` stdout appeared in the generated client's log view while the deployment still showed Building, captured separately for WS and SSE. |
| Force SSE fallback | The isolated proxy rejected WS upgrades. The same generated bindings delivered live build output and deployment status through SSE. |
| Disconnect and reconnect | Reload recovered retained output. Backend WS/SSE tests checked acknowledged-cursor replay and deduplication. Repeated identical provider lines remain distinct when their provider identities differ. |
| API and worker loss | The first real API and its deployment worker stopped during the build. The second API resumed durable activity and replayed committed logs, then completed the rollout. |
| Build failure and retry | A real HTTP 503 build dependency failed Docker's build. Error output persisted in the UI; restoring the dependency and retrying succeeded with four retained attempt ranges. |
| Failed replacement | Readiness failure preserved the original running container and binding. Retry became ready before removal of the original container. The final harness also checks the terminal error is a readiness failure. |
| Another browser changes status | A zero-action plan created and approved in a second browser changed the first browser's overview, waiting count, activity and completed detail without reload. |
| Revoke access and change organization | Revoking a real read API key closed its WS feed and denied log reads. Cross-organization reads were denied. Switching organizations in the dashboard cleared the old sidebar projects, deployments, activity and infrastructure. |
| Cursor expiry and bounded output | An expired cursor delivered an explicit gap and current snapshot. Advancing the isolated attempt budget produced a visible truncation record. Separate regressions page through more than 10,000 records and retain the current 2,000-line tail. |
| Remove the application | Product-driven removal deleted all owned containers, Kubernetes workloads, Services, runtime Secrets and bindings. The external image-pull Secret retained its UID; the unrelated registry and namespace ConfigMap survived. Harness cleanup then removed only its recorded kind cluster, builder and network. |

Desktop and 390px rollout views were checked. The narrow rollout had a 390px document and scroll width. Screenshots also cover target review, failure, live WS/SSE markers, replay, cleanup, another browser's approval, organization isolation and the Vercel counter/removal.

## Completion criteria

| Criterion | Implementation and verification |
| --- | --- |
| Both target kinds connect and run Forge apps | `internal/connections/infrastructure.go`, `internal/clouds/server`, `internal/clouds/kubernetes`, the dashboard target picker and real acceptance rollouts. |
| Pinned targets and sources preserve ownership | `internal/planning/targets.go`, approved action/lockset digests, connection-use foreign keys, owned object labels and scoped cleanup. Unit, Postgres and live replacement tests cover the boundaries. Delete plans show extant recorded connections; unreachable legacy connections preserve their warning and cleanup behavior. |
| Supported build output and honest limitations | Durable image-builder/provider collectors, operation cursors and log availability. Real Docker and Vercel evidence; GCP, AWS and plugin adapters have API fixture tests. |
| Forge WS/SSE with replay and cache recovery | `extensions/realtime/routes.go`, generated OpenAPI/AsyncAPI clients, client-core stream bindings and the Postgres event log. Wire decoder, transaction ordering, replay, auth and cache tests plus the live browser evidence. |
| Views converge and disclose disconnects | Shared scoped realtime cache, snapshot fencing, retained-log paging and low-frequency REST recovery. Live second-browser, API loss, WS failure and organization-switch checks. |
| Real infrastructure failure/retry/cleanup passes | `make acceptance-infrastructure` passed from final code, with owned resource inspection, preserved unrelated sentinels and recorded fixture cleanup. The rendered browser run also passed. |
| Format, lint, tests, build and generation pass | Final repository and fixture checks recorded above passed before the final live acceptance run, which also passed. |

## Verification limits and configuration

The infrastructure harness replaces GitHub source retrieval with a local source server and uses an authenticated SSH channel bridge to its isolated Docker daemon. Authentication, storage, build output, Docker builds, registry push/pull, Kubernetes API, readiness and browser streams are real. The isolated database advances cursor floors and log budgets to exercise expiry and truncation without waiting seven days or storing filler.

Private Kubernetes images require an existing namespace `image_pull_secret`. Its credentials and lifecycle belong to you; Loomplane checks credential-free metadata and references it without reading, replacing or deleting the Secret. Docker receives a direct registry token or a token minted through the approved issuer. Basic-only native Docker authentication is refused with setup instructions. Configure registry CA trust on the builder and cluster nodes yourself.

Local acceptance used Docker 29.8.2, kind 0.33 and Kubernetes 1.36.1 on arm64. Production ingress controllers, HPA metrics, network reachability and private CA arrangements remain specific to your infrastructure; API fixtures cover their adapter paths. GCP and AWS build-log readers were not exercised against live accounts. Render build logs remain unavailable because its API cannot isolate the exact deployment. Log-reader failure does not change the deployment's authoritative outcome.

Interrupted runs are excluded from passing evidence. Their recorded fixtures and isolated databases were removed before rerunning. A concurrent database timeout stopped a durable worker; its health check reported failure, and the development API recovered after restart. Run the acceptance suite separately from a busy integration suite. No global Docker pruning, default kubeconfig changes or unrelated development-process shutdown was used.
